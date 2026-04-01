package main

// domainTable defines a domain table to track, with its block-number column
// and an optional filter for "active" rows.
type domainTable struct {
	Name           string
	BlocknumCol    string // column holding blocknumber (empty if none)
	ActiveFilter   string // WHERE clause for "active" rows (empty = all rows)
	EntityType     string // maps to etl_manage_entities.entity_type (empty = skip cross-ref)
	Action         string // maps to etl_manage_entities.action (empty = skip cross-ref)
	CreatedAtCol   string // fallback column for time-based counting (empty = use blocknumcol)
}

// tables is the full list of domain tables we snapshot and diff.
var tables = []domainTable{
	{Name: "users", BlocknumCol: "blocknumber", ActiveFilter: "is_current = true", EntityType: "User", Action: "Create"},
	{Name: "tracks", BlocknumCol: "blocknumber", ActiveFilter: "is_current = true", EntityType: "Track", Action: "Create"},
	{Name: "playlists", BlocknumCol: "blocknumber", ActiveFilter: "is_current = true", EntityType: "Playlist", Action: "Create"},
	{Name: "follows", BlocknumCol: "blocknumber", ActiveFilter: "is_current = true"},
	{Name: "saves", BlocknumCol: "blocknumber", ActiveFilter: "is_current = true"},
	{Name: "reposts", BlocknumCol: "blocknumber", ActiveFilter: "is_current = true"},
	{Name: "subscriptions", BlocknumCol: "blocknumber", ActiveFilter: "is_current = true"},
	{Name: "comments", BlocknumCol: "blocknumber", ActiveFilter: "is_delete = false"},
	{Name: "grants", BlocknumCol: "blocknumber", ActiveFilter: "is_current = true"},
	{Name: "developer_apps", BlocknumCol: "blocknumber", ActiveFilter: "is_current = true"},
	{Name: "muted_users", BlocknumCol: "blocknumber", ActiveFilter: "is_delete = false"},
	{Name: "associated_wallets", BlocknumCol: "blocknumber", ActiveFilter: "is_current = true AND is_delete = false"},
	{Name: "dashboard_wallet_users", BlocknumCol: "blocknumber", ActiveFilter: "is_delete = false"},
	{Name: "shares", BlocknumCol: "blocknumber"},
	{Name: "track_downloads", BlocknumCol: "blocknumber"},
	{Name: "events", BlocknumCol: "blocknumber", ActiveFilter: "is_deleted = false"},
	{Name: "encrypted_emails", BlocknumCol: "", CreatedAtCol: "created_at"},
	{Name: "email_access", BlocknumCol: "", CreatedAtCol: "created_at"},
	{Name: "reactions", BlocknumCol: "blocknumber"},
	{Name: "notification", BlocknumCol: "blocknumber"},
}
