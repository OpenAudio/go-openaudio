package etl

// Config holds optional ETL component flags.
// All are enabled by default for full indexing behavior.
type Config struct {
	EnableMaterializedViewRefresh bool
	EnablePgNotifyListener        bool
}

// DefaultConfig returns config with all optional components enabled.
func DefaultConfig() Config {
	return Config{
		EnableMaterializedViewRefresh: true,
		EnablePgNotifyListener:        true,
	}
}

// DisableMaterializedViewRefresh disables the periodic MV refresh (for minimal indexing).
func (c *Config) DisableMaterializedViewRefresh() { c.EnableMaterializedViewRefresh = false }

// DisablePgNotifyListener disables the PostgreSQL LISTEN-based pubsub (for minimal indexing).
func (c *Config) DisablePgNotifyListener() { c.EnablePgNotifyListener = false }
