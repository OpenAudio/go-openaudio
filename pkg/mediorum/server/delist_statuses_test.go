package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type MockedDelistResponse struct {
	Result struct {
		Tracks []jsonDelistStatus `json:"tracks"`
		Users  []jsonDelistStatus `json:"users"`
	} `json:"result"`
	Timestamp string `json:"timestamp"`
	Signature string `json:"signature"`
}

func TestPollDelistStatuses(t *testing.T) {
	ss := testNetwork[0]

	// Double-check we're in test mode before truncating tables
	assert.Equal(t, "test", ss.Config.Env)

	mockResponse := MockedDelistResponse{
		Result: struct {
			Tracks []jsonDelistStatus `json:"tracks"`
			Users  []jsonDelistStatus `json:"users"`
		}{
			Tracks: []jsonDelistStatus{
				{
					CreatedAt: time.Now().Format(TimeFormat),
					aliasDelistStatus: &aliasDelistStatus{
						TrackID:  1,
						OwnerID:  100,
						TrackCID: "trackCid1",
						Delisted: true,
						Reason:   "ACR",
					},
				},
				{
					CreatedAt: time.Now().Format(TimeFormat),
					aliasDelistStatus: &aliasDelistStatus{
						TrackID:  2,
						OwnerID:  100,
						TrackCID: "trackCid2",
						Delisted: true,
						Reason:   "DMCA",
					},
				},
				{
					CreatedAt: time.Now().Add(time.Hour + time.Minute).Format(TimeFormat),
					aliasDelistStatus: &aliasDelistStatus{
						TrackID:  1,
						OwnerID:  100,
						TrackCID: "trackCid1",
						Delisted: false,
						Reason:   "MANUAL",
					},
				},
				{
					CreatedAt: time.Now().Format(TimeFormat),
					aliasDelistStatus: &aliasDelistStatus{
						TrackID:  3,
						OwnerID:  200,
						TrackCID: "trackCid3",
						Delisted: true,
						Reason:   "DMCA",
					},
				},
			},
			Users: []jsonDelistStatus{
				{
					CreatedAt: time.Now().Format(TimeFormat),
					aliasDelistStatus: &aliasDelistStatus{
						UserID:   100,
						Delisted: true,
						Reason:   "STRIKE_THRESHOLD",
					},
				},
				{
					CreatedAt: time.Now().Add(time.Hour + time.Minute).Format(TimeFormat),
					aliasDelistStatus: &aliasDelistStatus{
						UserID:   100,
						Delisted: false,
						Reason:   "COPYRIGHT_SCHOOL",
					},
				},
				{
					CreatedAt: time.Now().Format(TimeFormat),
					aliasDelistStatus: &aliasDelistStatus{
						UserID:   300,
						Delisted: true,
						Reason:   "STRIKE_THRESHOLD",
					},
				},
			},
		},
		Timestamp: time.Now().Format(time.RFC3339Nano),
		Signature: "testSignature",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()
	ctx := context.Background()

	assertBlacklisted := func(cid string, want bool) {
		t.Helper()
		got, err := ss.isCidBlacklisted(ctx, cid)
		require.NoError(t, err)
		assert.Equal(t, want, got, cid)
	}

	// Nothing should be delisted yet
	assertBlacklisted("trackCid1", false)
	assertBlacklisted("trackCid2", false)
	assertBlacklisted("trackCid3", false)

	// Poll delisted tracks and users
	assert.NoError(t, ss.pollDelistStatuses(ctx, "tracks", server.URL, "testWallet"))
	assert.NoError(t, ss.pollDelistStatuses(ctx, "users", server.URL, "testWallet"))

	// Verify that the delist statuses were inserted into the database
	assertBlacklisted("trackCid1", false)
	assertBlacklisted("trackCid2", true)
	assertBlacklisted("trackCid3", true)
}

// hideDelistStatuses renames track_delist_statuses out from under the delist
// lookup, so the query fails at the driver rather than returning zero rows.
// Stands in for the table being absent or the database being unreachable --
// which, before the lookup propagated its error, was indistinguishable from
// "this cid is not delisted".
func hideDelistStatuses(t *testing.T, ss *MediorumServer) {
	t.Helper()

	ctx := context.Background()
	_, err := ss.pgPool.Exec(ctx, `alter table track_delist_statuses rename to track_delist_statuses_hidden`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := ss.pgPool.Exec(context.Background(), `alter table track_delist_statuses_hidden rename to track_delist_statuses`)
		require.NoError(t, err, "the delist table must be restored or every later test in this package fails")
	})
}

func TestIsCidBlacklistedReturnsErrorWhenQueryFails(t *testing.T) {
	ss := testNetwork[1]

	hideDelistStatuses(t, ss)

	blacklisted, err := ss.isCidBlacklisted(context.Background(), "someCid")

	require.Error(t, err, "a failed lookup must not be reported as a completed one")
	assert.False(t, blacklisted)
}

// ensureNotDelistedRequest runs the middleware over the fixture cid and reports
// whether the wrapped handler was reached.
func ensureNotDelistedRequest(t *testing.T, ss *MediorumServer, cid string) (*httptest.ResponseRecorder, bool) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/content/"+cid, nil)
	rec := httptest.NewRecorder()
	c := ss.echo.NewContext(req, rec)
	c.SetPath("/content/:cid")
	c.SetParamNames("cid")
	c.SetParamValues(cid)

	served := false
	h := ss.ensureNotDelisted(func(c echo.Context) error {
		served = true
		return c.String(http.StatusOK, "blob")
	})
	if err := h(c); err != nil {
		ss.echo.HTTPErrorHandler(err, c)
	}

	return rec, served
}

// The point of the change: a lookup that never ran must not be read as
// "not delisted". Serving here would make every delisted cid on this node
// available for as long as the database is unreachable.
func TestEnsureNotDelistedDeniesWhenLookupFails(t *testing.T) {
	ss := testNetwork[1]

	hideDelistStatuses(t, ss)

	rec, served := ensureNotDelistedRequest(t, ss, "someCid")

	assert.False(t, served, "the blob handler must not run when the delist status is unknown")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "unable to verify delist status")
}

// The counterweight: a working lookup that finds no row still serves, so
// failing closed does not turn into denying everything.
func TestEnsureNotDelistedServesWhenNotDelisted(t *testing.T) {
	ss := testNetwork[1]

	rec, served := ensureNotDelistedRequest(t, ss, "notDelistedCid")

	assert.True(t, served)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestEnsureNotDelistedBlocksDelistedCid(t *testing.T) {
	ss := testNetwork[1]

	ctx := context.Background()
	_, err := ss.pgPool.Exec(ctx,
		`insert into track_delist_statuses ("createdAt", "trackId", "ownerId", "trackCid", delisted, reason)
		 values ($1, $2, $3, $4, $5, $6)`,
		time.Now(), 9001, 42, "delistedCid", true, "DMCA")
	require.NoError(t, err)
	t.Cleanup(func() {
		ss.pgPool.Exec(context.Background(), `delete from track_delist_statuses where "trackCid" = $1`, "delistedCid")
	})

	rec, served := ensureNotDelistedRequest(t, ss, "delistedCid")

	assert.False(t, served)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
