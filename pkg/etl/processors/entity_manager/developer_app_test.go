package entity_manager

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDeveloperAppCreate_TxType(t *testing.T) {
	h := DeveloperAppCreate()
	if h.EntityType() != EntityTypeDeveloperApp {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeDeveloperApp)
	}
	if h.Action() != ActionCreate {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionCreate)
	}
}

func TestDeveloperAppCreate_StatelessValidation(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		wantErr  string
	}{
		{
			name:     "missing metadata",
			metadata: "",
			wantErr:  "metadata is required",
		},
		{
			name:     "missing name",
			metadata: `{"address":"0xapp1"}`,
			wantErr:  "name is required",
		},
		{
			name:     "name too long",
			metadata: `{"address":"0xapp1","name":"` + strings.Repeat("x", CharacterLimitAppName+1) + `"}`,
			wantErr:  "name exceeds",
		},
		{
			name:     "description too long",
			metadata: `{"address":"0xapp1","name":"App","description":"` + strings.Repeat("x", CharacterLimitAppDescription+1) + `"}`,
			wantErr:  "description exceeds",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := &Params{
				UserID:     UserIDOffset + 1,
				EntityID:   1,
				EntityType: EntityTypeDeveloperApp,
				Action:     ActionCreate,
				Signer:     "0xabc",
			}
			if tt.metadata != "" {
				params.RawMetadata = tt.metadata
				params.Metadata = mustParseJSON(tt.metadata)
			}
			err := validateDeveloperAppCreate(context.Background(), params)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !IsValidationError(err) {
				t.Fatalf("expected ValidationError, got %T: %v", err, err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestDeveloperAppCreate_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xappowner", "appowner")

	meta := `{"address":"0xnewapp","name":"My App","description":"A great app"}`
	params := buildParams(t, pool, EntityTypeDeveloperApp, ActionCreate, uid, 1, "0xAppOwner", meta)
	mustHandle(t, DeveloperAppCreate(), params)

	var name string
	err := pool.QueryRow(context.Background(),
		"SELECT name FROM developer_apps WHERE address = '0xnewapp' AND is_current = true").Scan(&name)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if name != "My App" {
		t.Errorf("name = %q", name)
	}
}

func TestDeveloperAppCreate_RejectsDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xappowner", "appowner")

	meta := `{"address":"0xdupapp","name":"App"}`
	mustHandle(t, DeveloperAppCreate(), buildParams(t, pool, EntityTypeDeveloperApp, ActionCreate, uid, 1, "0xAppOwner", meta))
	mustReject(t, DeveloperAppCreate(), buildParams(t, pool, EntityTypeDeveloperApp, ActionCreate, uid, 1, "0xAppOwner", meta), "already exists")
}

func TestDeveloperAppUpdate_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xappowner", "appowner")

	createMeta := `{"address":"0xupdateapp","name":"Original"}`
	mustHandle(t, DeveloperAppCreate(), buildParams(t, pool, EntityTypeDeveloperApp, ActionCreate, uid, 1, "0xAppOwner", createMeta))

	updateMeta := `{"address":"0xupdateapp","name":"Updated"}`
	mustHandle(t, DeveloperAppUpdate(), buildParams(t, pool, EntityTypeDeveloperApp, ActionUpdate, uid, 1, "0xAppOwner", updateMeta))

	var name string
	err := pool.QueryRow(context.Background(),
		"SELECT name FROM developer_apps WHERE address = '0xupdateapp' AND is_current = true").Scan(&name)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if name != "Updated" {
		t.Errorf("name = %q", name)
	}
}

func TestDeveloperAppUpdate_RejectsWrongOwner(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	seedUser(t, pool, uid, "0xappowner", "appowner")
	seedUser(t, pool, uid2, "0xother", "other")

	createMeta := `{"address":"0xownedapp","name":"Mine"}`
	mustHandle(t, DeveloperAppCreate(), buildParams(t, pool, EntityTypeDeveloperApp, ActionCreate, uid, 1, "0xAppOwner", createMeta))

	updateMeta := `{"address":"0xownedapp","name":"Stolen"}`
	mustReject(t, DeveloperAppUpdate(), buildParams(t, pool, EntityTypeDeveloperApp, ActionUpdate, uid2, 1, "0xOther", updateMeta), "does not match user")
}

func TestDeveloperAppDelete_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xappowner", "appowner")

	createMeta := `{"address":"0xdeleteapp","name":"ToDelete"}`
	mustHandle(t, DeveloperAppCreate(), buildParams(t, pool, EntityTypeDeveloperApp, ActionCreate, uid, 1, "0xAppOwner", createMeta))

	deleteMeta := `{"address":"0xdeleteapp"}`
	mustHandle(t, DeveloperAppDelete(), buildParams(t, pool, EntityTypeDeveloperApp, ActionDelete, uid, 1, "0xAppOwner", deleteMeta))

	var isDelete bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_delete FROM developer_apps WHERE address = '0xdeleteapp' AND is_current = true").Scan(&isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isDelete {
		t.Error("expected is_delete = true")
	}
}

// mustParseJSON is a test helper.
func mustParseJSON(s string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}
