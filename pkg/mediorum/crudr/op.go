package crudr

import (
	"encoding/json"
	"fmt"
	"time"
)

type Op struct {
	ULID   string          `json:"ulid" gorm:"column:ulid"`
	Host   string          `json:"host"`
	Action string          `json:"action"` // create, update, delete
	Table  string          `json:"table"`
	Data   json.RawMessage `json:"data"`

	Transient bool `json:"transient" gorm:"-"`

	CoreTxHash      string     `json:"-" gorm:"column:core_tx_hash"`
	CoreTxStatus    string     `json:"-" gorm:"column:core_tx_status"`
	CoreTxError     string     `json:"-" gorm:"column:core_tx_error"`
	CoreAttemptedAt *time.Time `json:"-" gorm:"column:core_attempted_at"`
	CoreConfirmedAt *time.Time `json:"-" gorm:"column:core_confirmed_at"`
}

type withOption = func(op *Op)

const (
	CoreTxStatusLegacy    = "legacy"
	CoreTxStatusPending   = "pending"
	CoreTxStatusConfirmed = "confirmed"
	CoreTxStatusLocal     = "local"
	CoreTxStatusError     = "error"
)

func WithTransient() withOption {
	return func(op *Op) {
		op.Transient = true
	}
}

func (op Op) String() string {
	return fmt.Sprintf("%s: %s %s %s", op.Host, op.Action, op.Table, op.Data)
}

type Cursor struct {
	Host     string `json:"host" gorm:"primaryKey"`
	LastULID string `json:"last_ulid" gorm:"column:last_ulid"`
}
