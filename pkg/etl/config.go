package etl

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds optional ETL component flags.
// All are enabled by default for full indexing behavior.
type Config struct {
	EnableMaterializedViewRefresh bool
	EnablePgNotifyListener        bool
	EnableScheduledReleases       bool

	// BlockStreamEnabled consumes blocks via the CoreService.StreamBlocks gRPC
	// stream instead of polling GetBlocks. Requires a stream client set with
	// SetBlockStreamClient; falls back to polling if unset or unsupported.
	BlockStreamEnabled bool

	// CanonicalTxOrder makes the polling block reader request each block's
	// transactions in the order the chain executed them. When false (default),
	// core returns them in its priority order: ManageEntity first, Creates
	// before other actions. The StreamBlocks source always reads chain order.
	CanonicalTxOrder bool

	// DataTypes controls which entity types the entity manager will index.
	// If nil (default), all entity types are enabled.
	// If non-nil (even if empty), only listed types are enabled.
	// Populated from OPENAUDIO_ETL_ENTITY_MANAGER_DATA_TYPES env var (comma-separated).
	DataTypes *[]string

	// VerifyPlaySigners drops Plays transactions whose envelope signature does
	// not recover to a known validator node: no etl_plays rows are written and
	// no plays hooks fire. The transaction itself is still recorded.
	VerifyPlaySigners bool

	// GenesisMigrationEndHeight is the last block written by genesis-writer
	// (genesis_migration_end_height in the chain's genesis app_state). Plays at
	// or below it are exempt from VerifyPlaySigners. Zero means the chain has
	// no genesis-migration range.
	GenesisMigrationEndHeight int64
}

// DefaultConfig returns config with all optional components enabled.
func DefaultConfig() Config {
	return Config{
		EnableMaterializedViewRefresh: true,
		EnablePgNotifyListener:        true,
		EnableScheduledReleases:       true,
		DataTypes:                     nil,
		VerifyPlaySigners:             true,
	}
}

// ReadDataTypesEnv reads OPENAUDIO_ETL_ENTITY_MANAGER_DATA_TYPES and sets DataTypes accordingly.
// If the env var is not set, DataTypes remains nil (all types enabled).
// If set to empty string, DataTypes is an empty slice (no entity types enabled).
// If set to a comma-separated list, only those types are enabled.
func (c *Config) ReadDataTypesEnv() {
	val, ok := os.LookupEnv("OPENAUDIO_ETL_ENTITY_MANAGER_DATA_TYPES")
	if !ok {
		return
	}
	if val == "" {
		empty := []string{}
		c.DataTypes = &empty
		return
	}
	parts := strings.Split(val, ",")
	types := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			types = append(types, t)
		}
	}
	c.DataTypes = &types
}

// ReadBackgroundJobsEnv lets the periodic, wall-clock-driven components be
// turned off independently of indexing:
//
//	OPENAUDIO_ETL_MV_REFRESH_ENABLED
//	OPENAUDIO_ETL_SCHEDULED_RELEASES_ENABLED
//	OPENAUDIO_ETL_PG_NOTIFY_ENABLED
//
// Each defaults to enabled and is disabled by setting it to "false".
//
// These are worth disabling while catching up on a large backlog. The
// materialized view refresher, for instance, rebuilds mv_dashboard_* every two
// minutes by aggregating the whole of etl_transactions — measured at 8.7s per
// view against 32.7M rows, and growing with the table, while the views
// themselves are only tens of kilobytes of dashboard analytics that nothing
// reads during a replay.
func (c *Config) ReadBackgroundJobsEnv() {
	disabled := func(key string) bool {
		v, ok := os.LookupEnv(key)
		return ok && strings.EqualFold(strings.TrimSpace(v), "false")
	}
	if disabled("OPENAUDIO_ETL_MV_REFRESH_ENABLED") {
		c.DisableMaterializedViewRefresh()
	}
	if disabled("OPENAUDIO_ETL_SCHEDULED_RELEASES_ENABLED") {
		c.DisableScheduledReleases()
	}
	if disabled("OPENAUDIO_ETL_PG_NOTIFY_ENABLED") {
		c.DisablePgNotifyListener()
	}
}

// ReadPlaySignerEnv applies the play signer settings:
//
//	OPENAUDIO_ETL_VERIFY_PLAY_SIGNERS            "false" disables the check
//	OPENAUDIO_ETL_GENESIS_MIGRATION_END_HEIGHT   sets GenesisMigrationEndHeight
func (c *Config) ReadPlaySignerEnv() error {
	if v, ok := os.LookupEnv("OPENAUDIO_ETL_VERIFY_PLAY_SIGNERS"); ok && strings.EqualFold(strings.TrimSpace(v), "false") {
		c.VerifyPlaySigners = false
	}
	if v, ok := os.LookupEnv("OPENAUDIO_ETL_GENESIS_MIGRATION_END_HEIGHT"); ok && strings.TrimSpace(v) != "" {
		h, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || h < 0 {
			return fmt.Errorf("OPENAUDIO_ETL_GENESIS_MIGRATION_END_HEIGHT: invalid height %q", v)
		}
		c.GenesisMigrationEndHeight = h
	}
	return nil
}

// IsDataTypeEnabled returns true if the given entity type should be indexed.
func (c *Config) IsDataTypeEnabled(entityType string) bool {
	if c.DataTypes == nil {
		return true
	}
	for _, t := range *c.DataTypes {
		if strings.EqualFold(t, entityType) {
			return true
		}
	}
	return false
}

// DisableMaterializedViewRefresh disables the periodic MV refresh (for minimal indexing).
func (c *Config) DisableMaterializedViewRefresh() { c.EnableMaterializedViewRefresh = false }

// DisablePgNotifyListener disables the PostgreSQL LISTEN-based pubsub (for minimal indexing).
func (c *Config) DisablePgNotifyListener() { c.EnablePgNotifyListener = false }

// DisableScheduledReleases disables the periodic publish-scheduled-releases task.
func (c *Config) DisableScheduledReleases() { c.EnableScheduledReleases = false }
