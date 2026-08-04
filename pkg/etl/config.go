package etl

import (
	"os"
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

	// DataTypes controls which entity types the entity manager will index.
	// If nil (default), all entity types are enabled.
	// If non-nil (even if empty), only listed types are enabled.
	// Populated from OPENAUDIO_ETL_ENTITY_MANAGER_DATA_TYPES env var (comma-separated).
	DataTypes *[]string
}

// DefaultConfig returns config with all optional components enabled.
func DefaultConfig() Config {
	return Config{
		EnableMaterializedViewRefresh: true,
		EnablePgNotifyListener:        true,
		EnableScheduledReleases:       true,
		DataTypes:                     nil,
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
