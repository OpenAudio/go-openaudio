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
