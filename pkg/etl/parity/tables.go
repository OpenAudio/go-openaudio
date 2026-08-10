package main

// aggCheck is a whole-table aggregate evaluated identically on both databases.
//
// Row counts alone cannot see inside a row. A table can carry the right number
// of rows while a column that was supposed to be derived during indexing is
// empty in every one of them -- which is exactly how a real bug escaped every
// prior parity run: tracks came out 1,955,896 rows in the reference against
// 1,955,877 indexed (a plausible-looking delta), while
// playlists_containing_track was populated on 943,784 reference rows and 0
// indexed rows. Those columns gate album-purchase access to tracks.
//
// Each check is one aggregate over one scan, so a whole table's worth of
// column-level assertions costs about the same as counting its rows.
type aggCheck struct {
	Name string // label printed in the report
	Expr string // aggregate expression, must be valid against both schemas
}

// compareTable defines how to compare a domain table across two databases.
type compareTable struct {
	Name       string
	IDCols     []string          // primary key column(s)
	Columns    []string          // columns to compare
	Where      string            // filter for rows to compare (applied to ETL db)
	ProdWhere  string            // filter for the reference lookup (defaults to Where)
	KnownDiffs []string          // columns with known legacy/Go divergence (reported separately)
	CastCols   map[string]string // column -> cast expression for SELECT (e.g. "save_type" -> "save_type::text")

	// SampleCol is an integer column used to take a deterministic sample of
	// wide tables: mod(abs(col), N) = k. It must be reproducible and it must
	// select the same rows on both sides, which rules out TABLESAMPLE (its
	// block sampling depends on physical layout, and the two databases were
	// written independently). Empty means the table is always compared whole.
	SampleCol string

	// NoBlockNumber marks tables that carry no blocknumber column, so neither
	// the ETL-side boundary filter nor the reference-ahead cutoff applies.
	NoBlockNumber bool

	Aggregates []aggCheck
}

// jsonbPopulated builds a filter that counts rows where a jsonb column holds
// real content.
//
// `col IS NOT NULL` is wrong here: a jsonb column holding the JSON literal
// `null` is SQL NOT NULL, so the naive form counts rows that carry nothing. On
// this dataset that mistake once inflated a count by roughly 400x. Compare the
// jsonb type instead, and treat an empty object/array as unpopulated too.
func jsonbPopulated(col string) string {
	return "CASE WHEN " + col + " IS NULL THEN false" +
		" WHEN jsonb_typeof(" + col + ") = 'null' THEN false" +
		" WHEN " + col + " = '{}'::jsonb THEN false" +
		" WHEN " + col + " = '[]'::jsonb THEN false" +
		" ELSE true END"
}

// countWhere is a count(*) restricted to a predicate.
func countWhere(pred string) string {
	return "count(*) FILTER (WHERE " + pred + ")"
}

// countJSONB counts rows whose jsonb column holds real content.
func countJSONB(col string) string {
	return countWhere(jsonbPopulated(col))
}

// textPopulated counts rows whose text column is neither NULL nor empty.
func textPopulated(col string) string {
	return countWhere(col + " IS NOT NULL AND " + col + " <> ''")
}

var compareTables = []compareTable{
	{
		Name:   "users",
		IDCols: []string{"user_id"},
		Columns: []string{
			"handle", "name", "bio", "location",
			"profile_picture_sizes",
			"cover_photo_sizes",
			"is_verified", "is_deactivated",
			"wallet", "allow_ai_attribution",
		},
		Where:     "is_current = true",
		SampleCol: "user_id",
		// profile_picture and cover_photo were immutable in the legacy indexer but are updatable here
		KnownDiffs: []string{"profile_picture", "cover_photo"},
		Aggregates: []aggCheck{
			{"current", countWhere("is_current")},
			{"current_verified", countWhere("is_current AND is_verified")},
			{"current_deactivated", countWhere("is_current AND is_deactivated")},
			{"current_unavailable", countWhere("is_current AND NOT is_available")},
			{"with_handle", textPopulated("handle")},
			{"with_wallet", textPopulated("wallet")},
			{"with_bio", textPopulated("bio")},
			{"with_profile_picture_sizes", textPopulated("profile_picture_sizes")},
			{"with_cover_photo_sizes", textPopulated("cover_photo_sizes")},
			{"with_playlist_library", countJSONB("playlist_library")},
			{"with_artist_pick", countWhere("artist_pick_track_id IS NOT NULL")},
			{"allow_ai_attribution", countWhere("allow_ai_attribution")},
		},
	},
	{
		Name:   "tracks",
		IDCols: []string{"track_id"},
		Columns: []string{
			"owner_id", "title", "genre", "mood", "tags", "description",
			"cover_art", "cover_art_sizes",
			"is_unlisted", "is_delete",
			"track_cid", "preview_cid", "orig_file_cid",
			"duration", "is_downloadable", "is_available",
			"is_stream_gated", "is_download_gated",
			"is_scheduled_release", "is_playlist_upload",
			"playlists_containing_track",
		},
		Where:     "is_current = true AND is_delete = false",
		SampleCol: "track_id",
		Aggregates: []aggCheck{
			{"current", countWhere("is_current")},
			{"current_deleted", countWhere("is_current AND is_delete")},
			{"current_unlisted", countWhere("is_current AND is_unlisted")},
			{"current_unavailable", countWhere("is_current AND NOT is_available")},
			{"with_track_cid", textPopulated("track_cid")},
			{"with_preview_cid", textPopulated("preview_cid")},
			{"with_orig_file_cid", textPopulated("orig_file_cid")},
			{"with_cover_art_sizes", textPopulated("cover_art_sizes")},
			{"duration_positive", countWhere("duration > 0")},
			{"stream_gated", countWhere("is_stream_gated")},
			{"download_gated", countWhere("is_download_gated")},
			{"downloadable", countWhere("is_downloadable")},
			{"scheduled_release", countWhere("is_scheduled_release")},
			{"playlist_upload", countWhere("is_playlist_upload")},
			// The two checks this whole exercise exists for. Both are derived
			// during indexing rather than copied off the entity, both were
			// empty for every indexed row, and row counts saw nothing.
			{"in_playlists", countWhere("coalesce(cardinality(playlists_containing_track), 0) > 0")},
			{"in_playlists_entries", "coalesce(sum(coalesce(cardinality(playlists_containing_track), 0)), 0)"},
			{"previously_in_playlists", countJSONB("playlists_previously_containing_track")},
			{"with_stem_of", countJSONB("stem_of")},
			{"with_remix_of", countJSONB("remix_of")},
			{"with_stream_conditions", countJSONB("stream_conditions")},
			{"with_download_conditions", countJSONB("download_conditions")},
			{"with_ai_attribution", countWhere("ai_attribution_user_id IS NOT NULL")},
			{"with_pinned_comment", countWhere("pinned_comment_id IS NOT NULL")},
		},
	},
	{
		Name:   "playlists",
		IDCols: []string{"playlist_id"},
		Columns: []string{
			"playlist_owner_id", "playlist_name", "description",
			"is_album", "is_private",
			"playlist_image_sizes_multihash",
			"is_stream_gated", "is_scheduled_release",
		},
		Where:     "is_current = true AND is_delete = false",
		SampleCol: "playlist_id",
		// Legacy indexer bug: playlist_image_multihash got the sizes_multihash value during create
		KnownDiffs: []string{"playlist_image_multihash"},
		Aggregates: []aggCheck{
			{"current", countWhere("is_current")},
			{"current_deleted", countWhere("is_current AND is_delete")},
			{"current_albums", countWhere("is_current AND is_album")},
			{"current_private", countWhere("is_current AND is_private")},
			{"with_name", textPopulated("playlist_name")},
			{"with_image_sizes_multihash", textPopulated("playlist_image_sizes_multihash")},
			{"with_contents", countJSONB("playlist_contents")},
			// A playlist whose contents indexed as an empty track list is a
			// playlist with nothing in it. Counting rows cannot tell.
			{"with_track_ids", countWhere(
				"CASE WHEN jsonb_typeof(playlist_contents -> 'track_ids') = 'array'" +
					" THEN jsonb_array_length(playlist_contents -> 'track_ids') > 0 ELSE false END")},
			{"track_id_entries", "coalesce(sum(CASE WHEN jsonb_typeof(playlist_contents -> 'track_ids') = 'array'" +
				" THEN jsonb_array_length(playlist_contents -> 'track_ids') ELSE 0 END), 0)"},
			{"stream_gated", countWhere("is_stream_gated")},
			{"with_stream_conditions", countJSONB("stream_conditions")},
		},
	},
	{
		Name:      "follows",
		IDCols:    []string{"follower_user_id", "followee_user_id"},
		Columns:   []string{"is_delete"},
		Where:     "is_current = true",
		ProdWhere: "is_current = true",
		SampleCol: "follower_user_id",
		Aggregates: []aggCheck{
			{"current", countWhere("is_current")},
			{"current_deleted", countWhere("is_current AND is_delete")},
			{"current_active", countWhere("is_current AND NOT is_delete")},
		},
	},
	{
		Name:      "saves",
		IDCols:    []string{"user_id", "save_item_id", "save_type"},
		Columns:   []string{"is_delete"},
		Where:     "is_current = true",
		ProdWhere: "is_current = true",
		CastCols:  map[string]string{"save_type": "save_type::text"},
		SampleCol: "user_id",
		Aggregates: []aggCheck{
			{"current", countWhere("is_current")},
			{"current_deleted", countWhere("is_current AND is_delete")},
			{"current_tracks", countWhere("is_current AND save_type::text = 'track'")},
			{"current_playlists", countWhere("is_current AND save_type::text = 'playlist'")},
			{"current_albums", countWhere("is_current AND save_type::text = 'album'")},
		},
	},
	{
		Name:      "reposts",
		IDCols:    []string{"user_id", "repost_item_id", "repost_type"},
		Columns:   []string{"is_delete"},
		Where:     "is_current = true",
		ProdWhere: "is_current = true",
		CastCols:  map[string]string{"repost_type": "repost_type::text"},
		SampleCol: "user_id",
		Aggregates: []aggCheck{
			{"current", countWhere("is_current")},
			{"current_deleted", countWhere("is_current AND is_delete")},
			{"current_tracks", countWhere("is_current AND repost_type::text = 'track'")},
			{"current_playlists", countWhere("is_current AND repost_type::text = 'playlist'")},
			{"current_albums", countWhere("is_current AND repost_type::text = 'album'")},
		},
	},
	{
		Name:      "subscriptions",
		IDCols:    []string{"subscriber_id", "user_id"},
		Columns:   []string{"is_delete"},
		Where:     "is_current = true",
		ProdWhere: "is_current = true",
		SampleCol: "subscriber_id",
		Aggregates: []aggCheck{
			{"current", countWhere("is_current")},
			{"current_deleted", countWhere("is_current AND is_delete")},
			{"current_active", countWhere("is_current AND NOT is_delete")},
		},
	},
	{
		Name:      "comments",
		IDCols:    []string{"comment_id"},
		Columns:   []string{"user_id", "entity_id", "entity_type", "text", "is_delete"},
		Where:     "is_delete = false",
		SampleCol: "comment_id",
		Aggregates: []aggCheck{
			{"deleted", countWhere("is_delete")},
			{"visible", countWhere("is_visible")},
			{"edited", countWhere("is_edited")},
			{"members_only", countWhere("is_members_only")},
			{"with_text", textPopulated("text")},
			{"with_track_timestamp", countWhere("track_timestamp_s IS NOT NULL")},
		},
	},
	{
		Name:      "grants",
		IDCols:    []string{"user_id", "grantee_address"},
		Columns:   []string{"is_approved", "is_revoked"},
		Where:     "is_current = true",
		SampleCol: "user_id",
		Aggregates: []aggCheck{
			{"current", countWhere("is_current")},
			{"current_approved", countWhere("is_current AND is_approved")},
			{"current_revoked", countWhere("is_current AND is_revoked")},
		},
	},
	{
		Name:    "developer_apps",
		IDCols:  []string{"address"},
		Columns: []string{"user_id", "name", "description", "is_delete"},
		Where:   "is_current = true",
		Aggregates: []aggCheck{
			{"current", countWhere("is_current")},
			{"current_deleted", countWhere("is_current AND is_delete")},
			{"with_description", textPopulated("description")},
			{"with_image_url", textPopulated("image_url")},
		},
	},
	{
		Name:    "muted_users",
		IDCols:  []string{"user_id", "muted_user_id"},
		Columns: []string{"is_delete"},
		Where:   "is_delete = false",
		Aggregates: []aggCheck{
			{"deleted", countWhere("is_delete")},
			{"active", countWhere("NOT is_delete")},
		},
	},
	{
		Name:      "associated_wallets",
		IDCols:    []string{"user_id", "wallet"},
		Columns:   []string{"chain", "is_delete"},
		Where:     "is_current = true AND is_delete = false",
		ProdWhere: "is_current = true AND is_delete = false",
		SampleCol: "user_id",
		Aggregates: []aggCheck{
			{"current", countWhere("is_current")},
			{"current_deleted", countWhere("is_current AND is_delete")},
			{"current_eth", countWhere("is_current AND chain::text = 'eth'")},
			{"current_sol", countWhere("is_current AND chain::text = 'sol'")},
		},
	},
	{
		Name:    "dashboard_wallet_users",
		IDCols:  []string{"user_id", "wallet"},
		Columns: []string{"is_delete"},
		Where:   "is_delete = false",
		Aggregates: []aggCheck{
			{"deleted", countWhere("is_delete")},
			{"active", countWhere("NOT is_delete")},
		},
	},

	// --- tables that had no parity coverage at all until now ---

	{
		Name:   "playlist_tracks",
		IDCols: []string{"playlist_id", "track_id"},
		// created_at/updated_at are deliberately excluded: migrated rows take
		// their timestamps from block time, which is being fixed separately.
		Columns:       []string{"is_removed"},
		Where:         "true",
		SampleCol:     "playlist_id",
		NoBlockNumber: true,
		Aggregates: []aggCheck{
			{"removed", countWhere("is_removed")},
			{"active", countWhere("NOT is_removed")},
			{"distinct_playlists", "count(DISTINCT playlist_id)"},
			{"distinct_tracks", "count(DISTINCT track_id)"},
		},
	},
	{
		Name:      "playlist_routes",
		IDCols:    []string{"owner_id", "slug"},
		Columns:   []string{"playlist_id", "title_slug", "collision_id", "is_current"},
		Where:     "is_current = true",
		ProdWhere: "is_current = true",
		SampleCol: "playlist_id",
		Aggregates: []aggCheck{
			{"current", countWhere("is_current")},
			{"with_slug", textPopulated("slug")},
			{"with_title_slug", textPopulated("title_slug")},
			{"collisions", countWhere("collision_id > 0")},
			{"distinct_playlists", "count(DISTINCT playlist_id)"},
		},
	},
	{
		Name:      "track_routes",
		IDCols:    []string{"owner_id", "slug"},
		Columns:   []string{"track_id", "title_slug", "collision_id", "is_current"},
		Where:     "is_current = true",
		ProdWhere: "is_current = true",
		SampleCol: "track_id",
		Aggregates: []aggCheck{
			{"current", countWhere("is_current")},
			{"with_slug", textPopulated("slug")},
			{"with_title_slug", textPopulated("title_slug")},
			{"collisions", countWhere("collision_id > 0")},
			{"distinct_tracks", "count(DISTINCT track_id)"},
		},
	},
	{
		Name:      "comment_mentions",
		IDCols:    []string{"comment_id", "user_id"},
		Columns:   []string{"is_delete"},
		Where:     "true",
		SampleCol: "comment_id",
		Aggregates: []aggCheck{
			{"deleted", countWhere("is_delete")},
			{"active", countWhere("NOT is_delete")},
			{"distinct_comments", "count(DISTINCT comment_id)"},
		},
	},
	{
		Name:      "comment_reactions",
		IDCols:    []string{"comment_id", "user_id"},
		Columns:   []string{"is_delete"},
		Where:     "true",
		SampleCol: "comment_id",
		Aggregates: []aggCheck{
			{"deleted", countWhere("is_delete")},
			{"active", countWhere("NOT is_delete")},
			{"distinct_comments", "count(DISTINCT comment_id)"},
		},
	},
	{
		Name:    "events",
		IDCols:  []string{"event_id"},
		Columns: []string{"event_type", "user_id", "entity_type", "entity_id", "is_deleted"},
		Where:   "true",
		CastCols: map[string]string{
			"event_type":  "event_type::text",
			"entity_type": "entity_type::text",
		},
		Aggregates: []aggCheck{
			{"deleted", countWhere("is_deleted")},
			{"active", countWhere("NOT is_deleted")},
			{"with_entity", countWhere("entity_id IS NOT NULL")},
			{"with_end_date", countWhere("end_date IS NOT NULL")},
			{"with_event_data", countJSONB("event_data")},
		},
	},
	{
		Name:          "email_access",
		IDCols:        []string{"email_owner_user_id", "receiving_user_id", "grantor_user_id"},
		Columns:       []string{"encrypted_key"},
		Where:         "true",
		SampleCol:     "email_owner_user_id",
		NoBlockNumber: true,
		Aggregates: []aggCheck{
			{"with_encrypted_key", textPopulated("encrypted_key")},
			{"distinct_owners", "count(DISTINCT email_owner_user_id)"},
			{"distinct_receivers", "count(DISTINCT receiving_user_id)"},
			{"distinct_grantors", "count(DISTINCT grantor_user_id)"},
		},
	},
	{
		Name:          "encrypted_emails",
		IDCols:        []string{"email_owner_user_id"},
		Columns:       []string{"encrypted_email"},
		Where:         "true",
		SampleCol:     "email_owner_user_id",
		NoBlockNumber: true,
		Aggregates: []aggCheck{
			{"with_encrypted_email", textPopulated("encrypted_email")},
			{"distinct_owners", "count(DISTINCT email_owner_user_id)"},
		},
	},
	{
		Name:      "track_downloads",
		IDCols:    []string{"parent_track_id", "track_id", "txhash"},
		Columns:   []string{"user_id"},
		Where:     "true",
		SampleCol: "track_id",
		Aggregates: []aggCheck{
			{"distinct_tracks", "count(DISTINCT track_id)"},
			{"distinct_users", "count(DISTINCT user_id)"},
			{"with_country", textPopulated("country")},
			{"with_city", textPopulated("city")},
		},
	},
}
