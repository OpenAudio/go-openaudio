package crudr

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/httputil"
	"github.com/OpenAudio/go-openaudio/pkg/lifecycle"
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
	DB           *gorm.DB
	myPrivateKey *ecdsa.PrivateKey

	host       string
	logger     *zap.Logger
	typeMap    map[string]reflect.Type
	httpClient *http.Client

	peerClients []*PeerClient
	coreWrites  bool

	mu        sync.Mutex
	callbacks []func(op *Op, records interface{})

	lc *lifecycle.Lifecycle
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

func New(selfHost string, myPrivateKey *ecdsa.PrivateKey, peerHosts []string, db *gorm.DB, parentLifecycle *lifecycle.Lifecycle, logger *zap.Logger, httpClient *http.Client) *Crudr {
	selfHost = httputil.RemoveTrailingSlash(strings.ToLower(selfHost))

	err := migrateOps(db)
	if err != nil {
		panic(err)
	}

	err = db.AutoMigrate(&Cursor{})
	if err != nil {
		panic(err)
	}

	if httpClient == nil {
		httpClient = &http.Client{}
	}

	c := &Crudr{
		DB:           db,
		myPrivateKey: myPrivateKey,

		host:       selfHost,
		logger:     logger.With(zap.String("module", "crud")),
		typeMap:    map[string]reflect.Type{},
		httpClient: httpClient,

		peerClients: make([]*PeerClient, len(peerHosts)),
		lc:          lifecycle.NewFromLifecycle(parentLifecycle, "crudr lifecycle"),
	}

	for idx, peerHost := range peerHosts {
		c.peerClients[idx] = NewPeerClient(peerHost, c, selfHost)
	}

	return c
}

func (c *Crudr) SetCoreWritesEnabled(enabled bool) {
	c.coreWrites = enabled
}

func (c *Crudr) StartClients() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.peerClients {
		p.Start(c.lc)
	}
}

// used for testing
func (c *Crudr) ForceSweep() {
	c.mu.Lock()
	peers := make([]*PeerClient, len(c.peerClients))
	copy(peers, c.peerClients)
	c.mu.Unlock()
	for _, p := range peers {
		p.doSweep(context.Background())
	}
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
		CoreTxStatus: CoreTxStatusLocal,
	}
	for _, opt := range opts {
		opt(op)
	}
	if c.coreWrites && !op.Transient {
		op.CoreTxStatus = CoreTxStatusPending
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

	// broadcast if this host is origin...
	if op.Host == c.host && !op.SkipBroadcast {
		msg, _ := json.Marshal(op)
		c.broadcast(msg)
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

func (c *Crudr) GetOutboxSizes() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	sizes := make(map[string]int)
	for _, p := range c.peerClients {
		sizes[p.Host] = len(p.outbox)
	}
	return sizes
}

func (c *Crudr) GetPercentNodesSeeded() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var nCaughtUp int
	var nPeers = len(c.peerClients)
	for _, p := range c.peerClients {
		if p.Seeded {
			nCaughtUp++
		}
	}

	return (float64(nCaughtUp) / float64(nPeers)) * 100
}

// UpdatePeers reconciles the crudr peer client list with a new set of peer hosts.
// New hosts get a PeerClient created and started; removed hosts are dropped.
func (c *Crudr) UpdatePeers(newHosts []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Build a set of current peer hosts
	currentHosts := make(map[string]*PeerClient, len(c.peerClients))
	for _, p := range c.peerClients {
		currentHosts[p.Host] = p
	}

	// Normalize and deduplicate new hosts (excluding self)
	newHostSet := make(map[string]bool, len(newHosts))
	for _, h := range newHosts {
		h = httputil.RemoveTrailingSlash(strings.ToLower(h))
		if h == c.host {
			continue
		}
		newHostSet[h] = true
	}

	// Start clients for newly added hosts
	for h := range newHostSet {
		if _, exists := currentHosts[h]; !exists {
			p := NewPeerClient(h, c, c.host)
			p.Start(c.lc)
			c.peerClients = append(c.peerClients, p)
			c.logger.Info("added new crudr peer", zap.String("host", h))
		}
	}

	// Remove clients for hosts no longer in the set and stop their goroutines
	kept := make([]*PeerClient, 0, len(newHostSet))
	for _, p := range c.peerClients {
		if newHostSet[p.Host] {
			kept = append(kept, p)
		} else {
			p.Stop()
			c.logger.Info("removed crudr peer", zap.String("host", p.Host))
		}
	}
	c.peerClients = kept
}
