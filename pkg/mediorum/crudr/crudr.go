package crudr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/httputil"
	"go.uber.org/zap"

	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionDelete = "delete"
)

const (
	LocalStreamName  = "ops"
	GlobalStreamName = "global"
)

var (
	errDuplicateOp = errors.New("duplicate op")
)

type Crudr struct {
	DB *gorm.DB

	host    string
	logger  *zap.Logger
	typeMap map[string]reflect.Type

	mu        sync.Mutex
	callbacks []func(op *Op, records interface{})
}

// create ops table if it does not exist
func migrateOps(db *gorm.DB) error {
	// de-partition ops if necessary
	hasPart := false
	db.Raw(`SELECT EXISTS (SELECT FROM information_schema.tables where table_name   = 'ops_1')`).Scan(&hasPart)
	if hasPart {
		departitoinDDL := `
		BEGIN;
			alter table ops rename to ops_part;
			create table ops as select * from ops_part;
			alter table ops add primary key (ulid);
			drop table ops_part;
		COMMIT;
		`
		if err := db.Exec(departitoinDDL).Error; err != nil {
			return err
		}
	}

	// create ops
	opDDL := `
	BEGIN;

		CREATE TABLE IF NOT EXISTS ops (
			"ulid" TEXT primary key,
			"host" TEXT,
			"action" TEXT,
			"table" TEXT,
			"data" JSONB);

		ALTER TABLE ops ADD COLUMN IF NOT EXISTS core_tx_hash TEXT;
		ALTER TABLE ops ADD COLUMN IF NOT EXISTS core_tx_status TEXT;
		ALTER TABLE ops ADD COLUMN IF NOT EXISTS core_tx_error TEXT;
		ALTER TABLE ops ADD COLUMN IF NOT EXISTS core_attempted_at TIMESTAMPTZ;
		ALTER TABLE ops ADD COLUMN IF NOT EXISTS core_confirmed_at TIMESTAMPTZ;

	COMMIT;
	`
	if err := db.Exec(opDDL).Error; err != nil {
		return err
	}
	if err := db.Exec(`
		CREATE INDEX CONCURRENTLY IF NOT EXISTS ops_core_pending_idx
		ON ops(host, core_tx_status, ulid)
		WHERE core_tx_status IN ('pending', 'error')
	`).Error; err != nil {
		return err
	}

	return nil
}

func New(selfHost string, db *gorm.DB, logger *zap.Logger) *Crudr {
	selfHost = httputil.RemoveTrailingSlash(strings.ToLower(selfHost))

	err := migrateOps(db)
	if err != nil {
		panic(err)
	}

	err = db.AutoMigrate(&Cursor{})
	if err != nil {
		panic(err)
	}

	c := &Crudr{
		DB:      db,
		host:    selfHost,
		logger:  logger.With(zap.String("module", "mediorum_ops")),
		typeMap: map[string]reflect.Type{},
	}

	return c
}

// RegisterModels accepts a instance of a GORM model and registers it
// to work with Op apply.
func (c *Crudr) RegisterModels(tables ...interface{}) *Crudr {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range tables {
		tableName := c.tableNameFor(t)
		c.typeMap[tableName] = reflect.TypeOf(t)
	}
	return c
}

func (c *Crudr) AddOpCallback(cb func(op *Op, records interface{})) {
	c.mu.Lock()
	c.callbacks = append(c.callbacks, cb)
	c.mu.Unlock()
}

func (c *Crudr) callOpCallbacks(op *Op, records interface{}) {
	for _, cb := range c.callbacks {
		cb(op, records)
	}
}

func (c *Crudr) Create(data interface{}, opts ...withOption) error {
	op := c.newOp(ActionCreate, data, opts...)
	return c.doOp(op)
}

func (c *Crudr) Update(data interface{}, opts ...withOption) error {
	op := c.newOp(ActionUpdate, data, opts...)
	return c.doOp(op)
}

func (c *Crudr) Patch(data interface{}, opts ...withOption) error {
	opts = append(opts, WithTransient())
	op := c.newOp(ActionUpdate, data, opts...)
	return c.doOp(op)
}

func (c *Crudr) Delete(data interface{}, opts ...withOption) error {
	op := c.newOp(ActionDelete, data, opts...)
	return c.doOp(op)
}

func (c *Crudr) newOp(action string, data interface{}, opts ...withOption) *Op {
	tableName := c.tableNameFor(data)

	j := jsonArrayMarshal(data)

	op := &Op{
		ULID:         ulid.Make().String(),
		Host:         c.host,
		Action:       action,
		Table:        tableName,
		Data:         j,
		CoreTxStatus: CoreTxStatusPending,
	}
	for _, opt := range opts {
		opt(op)
	}
	if op.Transient {
		op.CoreTxStatus = CoreTxStatusLocal
	}

	return op
}

func (c *Crudr) doOp(op *Op) error {
	// apply locally
	err := c.ApplyOp(op)
	if err != nil {
		c.logger.Warn("apply failed", zap.Any("op", op), zap.Error(err))
		return err
	}

	return nil
}

func jsonArrayMarshal(data interface{}) []byte {
	j, err := json.Marshal(data)
	// panic here because data is always provided by app dev
	if err != nil {
		panic(err)
	}

	// ensure array
	if j[0] != '[' {
		j = append([]byte{'['}, j...)
		j = append(j, ']')
	}

	return j
}

// tableNameFor finds the struct at the heart of a thing
// and gets the gorm table name for it.
// will continually unwrap slices / pointers till it gets
// to the named struct type
func (c *Crudr) tableNameFor(obj interface{}) string {
	t := reflect.TypeOf(obj)
	for t.Kind() != reflect.Struct {
		t = t.Elem()
	}
	typeName := t.Name()
	return c.DB.NamingStrategy.TableName(typeName)
}

func (c *Crudr) KnownType(op *Op) bool {
	_, ok := c.typeMap[op.Table]
	return ok
}

func (c *Crudr) ValidateOp(op *Op) error {
	_, err := c.recordsForOp(op)
	return err
}

func (c *Crudr) recordsForOp(op *Op) (interface{}, error) {
	if op == nil {
		return nil, errors.New("op is nil")
	}
	switch op.Action {
	case ActionCreate, ActionUpdate, ActionDelete:
	default:
		return nil, fmt.Errorf("unknown action: %s", op.Action)
	}
	elemType, ok := c.typeMap[op.Table]
	if !ok {
		return nil, fmt.Errorf("no type registered for %s", op.Table)
	}

	// deserialize op.Data to proper go type
	records := reflect.New(reflect.SliceOf(elemType)).Interface()
	err := json.Unmarshal(op.Data, records)
	if err != nil {
		return nil, fmt.Errorf("invalid crud data: %v %s", err, op.Data)
	}
	return records, nil
}

func (c *Crudr) ApplyOp(op *Op) error {
	records, err := c.recordsForOp(op)
	if err != nil {
		return err
	}
	// create op + records in a db transaction
	err = c.DB.Transaction(func(tx *gorm.DB) error {
		if c.shouldPersistOp(op) {
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(op)
			if res.Error != nil {
				return res.Error
			}

			// if ulid already in ops table
			// with belt+suspenders we see every event twice
			// so no need to log anything here
			if res.RowsAffected == 0 {
				return errDuplicateOp
			}
		}

		switch op.Action {
		case ActionCreate:
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(records)
			if res.RowsAffected == 0 {
				c.logger.Debug("create had no effect", zap.String("ulid", op.ULID))
				return nil
			}
			err = res.Error
		case ActionUpdate:
			res := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(records)
			err = res.Error
		case ActionDelete:
			err = tx.Delete(records).Error
		}

		return err
	})

	if err == errDuplicateOp {
		// belt+suspenders: just move on
		if err := updateDuplicateOpCoreState(c.DB.WithContext(context.Background()), op); err != nil {
			return err
		}
		return nil
	} else if err != nil {
		return err
	}

	// notify any local (in memory) subscribers
	c.callOpCallbacks(op, records)

	return nil
}

func (c *Crudr) shouldPersistOp(op *Op) bool {
	if op.Transient {
		return false
	}
	if op.Host == c.host {
		return true
	}
	return !isLegacyTransientUploadRetryOp(op)
}

func isLegacyTransientUploadRetryOp(op *Op) bool {
	if op.Table != "uploads" || op.Action != ActionUpdate {
		return false
	}

	var rows []struct {
		Status     string            `json:"status"`
		ErrorCount int               `json:"error_count"`
		Results    map[string]string `json:"results"`
	}
	if err := json.Unmarshal(op.Data, &rows); err != nil || len(rows) == 0 {
		return false
	}

	for _, row := range rows {
		if row.Status != "busy" && row.Status != "error" {
			return false
		}
		if row.ErrorCount <= 5 {
			return false
		}
		if row.Results["320"] != "" {
			return false
		}
	}

	return true
}

func updateDuplicateOpCoreState(tx *gorm.DB, op *Op) error {
	updates := map[string]interface{}{}
	if op.CoreTxHash != "" {
		updates["core_tx_hash"] = op.CoreTxHash
	}
	if op.CoreTxStatus != "" {
		updates["core_tx_status"] = op.CoreTxStatus
		updates["core_tx_error"] = op.CoreTxError
	}
	if op.CoreTxError != "" {
		updates["core_tx_error"] = op.CoreTxError
	}
	if op.CoreAttemptedAt != nil {
		updates["core_attempted_at"] = op.CoreAttemptedAt
	}
	if op.CoreConfirmedAt != nil {
		updates["core_confirmed_at"] = op.CoreConfirmedAt
	}
	if len(updates) == 0 {
		return nil
	}
	return tx.Model(&Op{}).Where("ulid = ?", op.ULID).Updates(updates).Error
}

func (c *Crudr) PendingCoreOps(ctx context.Context, limit int) ([]*Op, error) {
	if limit <= 0 {
		limit = 100
	}

	var ops []*Op
	err := c.DB.WithContext(ctx).
		Where("host = ? AND core_tx_status IN ?", c.host, []string{CoreTxStatusPending, CoreTxStatusError}).
		Order("ulid asc").
		Limit(limit).
		Find(&ops).Error
	return ops, err
}

func (c *Crudr) MarkCoreAttempted(ctx context.Context, op *Op, txHash string, attemptedAt time.Time) error {
	return c.DB.WithContext(ctx).Model(&Op{}).Where("ulid = ?", op.ULID).Updates(map[string]interface{}{
		"core_tx_hash":      txHash,
		"core_tx_status":    CoreTxStatusPending,
		"core_tx_error":     "",
		"core_attempted_at": attemptedAt,
	}).Error
}

func (c *Crudr) MarkCoreConfirmed(ctx context.Context, op *Op, txHash string, confirmedAt time.Time) error {
	return c.DB.WithContext(ctx).Model(&Op{}).Where("ulid = ?", op.ULID).Updates(map[string]interface{}{
		"core_tx_hash":      txHash,
		"core_tx_status":    CoreTxStatusConfirmed,
		"core_tx_error":     "",
		"core_confirmed_at": confirmedAt,
		"core_attempted_at": confirmedAt,
	}).Error
}

func (c *Crudr) MarkCoreError(ctx context.Context, op *Op, txHash string, attemptedAt time.Time, err error) error {
	errString := ""
	if err != nil {
		errString = err.Error()
	}
	return c.DB.WithContext(ctx).Model(&Op{}).Where("ulid = ?", op.ULID).Updates(map[string]interface{}{
		"core_tx_hash":      txHash,
		"core_tx_status":    CoreTxStatusError,
		"core_tx_error":     errString,
		"core_attempted_at": attemptedAt,
	}).Error
}
func (c *Crudr) MarkCoreRejected(ctx context.Context, op *Op, rejectedAt time.Time, err error) error {
	errString := ""
	if err != nil {
		errString = err.Error()
	}
	return c.DB.WithContext(ctx).Model(&Op{}).Where("ulid = ?", op.ULID).Updates(map[string]interface{}{
		"core_tx_hash":      "",
		"core_tx_status":    CoreTxStatusRejected,
		"core_tx_error":     errString,
		"core_attempted_at": rejectedAt,
	}).Error
}
