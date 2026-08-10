package server

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	v1storage "github.com/OpenAudio/go-openaudio/pkg/api/storage/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/server/signature"
	"github.com/OpenAudio/go-openaudio/pkg/registrar"
	"github.com/erni27/imcache"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixture signature below recovers to an EIP-55 checksummed wallet and is signed
// for this cid/track pair. Its timestamp is from 2020, so a request that gets past the
// access_authorities check fails on the age check instead -- that's how these tests
// tell "authorized" apart from "rejected".
const (
	testSigCid     = "QmP4b7jYPeb4tbdCpd1qkP4zkDteb5zMZa8Yk46whtNYv2"
	testSigTrackID = "220350"
	testSigParam   = "%7B%22data%22%3A%20%22%7B%5C%22trackId%5C%22%3A%20220350%2C%20%5C%22cid%5C%22%3A%20%5C%22QmP4b7jYPeb4tbdCpd1qkP4zkDteb5zMZa8Yk46whtNYv2%5C%22%2C%20%5C%22timestamp%5C%22%3A%201596159123000%2C%20%5C%22shouldCache%5C%22%3A%201%7D%22%2C%20%22signature%22%3A%20%220x0f0627a064bd2c3f8add3214e10c85c727ba9a73fe8b1d689c6322f872dff5c263f2adbb156fadcb600b19787c704a0d608e5a4d0bb50748bbe3c2ba6ad262291c%22%7D"
)

func TestRequireRegisteredSignatureWithUnregisteredNode(t *testing.T) {
	ss := testNetwork[0]

	// The track is ungated: the tables exist but hold no row for this cid.
	ensureTrackAccessTables(t, ss)
	ss.trackAccessInfoCache.Remove(testSigCid)

	// Empty list of signers means no node's signature will be valid
	origPeers := ss.Config.Peers
	defer func() {
		ss.Config.Peers = origPeers
	}()
	ss.Config.Signers = []registrar.Peer{}

	cid := "QmP4b7jYPeb4tbdCpd1qkP4zkDteb5zMZa8Yk46whtNYv2"
	signature := "%7B%22data%22%3A%20%22%7B%5C%22trackId%5C%22%3A%20220350%2C%20%5C%22cid%5C%22%3A%20%5C%22QmP4b7jYPeb4tbdCpd1qkP4zkDteb5zMZa8Yk46whtNYv2%5C%22%2C%20%5C%22timestamp%5C%22%3A%201596159123000%2C%20%5C%22shouldCache%5C%22%3A%201%7D%22%2C%20%22signature%22%3A%20%220x0f0627a064bd2c3f8add3214e10c85c727ba9a73fe8b1d689c6322f872dff5c263f2adbb156fadcb600b19787c704a0d608e5a4d0bb50748bbe3c2ba6ad262291c%22%7D"
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tracks/cidstream/%s?signature=%s", cid, signature), nil)

	rec := httptest.NewRecorder()
	c := ss.echo.NewContext(req, rec)
	c.SetPath("/tracks/cidstream/:cid")
	c.SetParamNames("cid")
	c.SetParamValues(cid)

	// Handle the request
	h := ss.requireRegisteredSignature(func(c echo.Context) error {
		return c.String(http.StatusOK, "test")
	})
	h(c)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "signer not in list of registered nodes")
}

func TestRequireRegisteredSignatureWithOldTimestamp(t *testing.T) {
	ss := testNetwork[0]

	// The track is ungated: the tables exist but hold no row for this cid.
	ensureTrackAccessTables(t, ss)
	ss.trackAccessInfoCache.Remove(testSigCid)

	// Make sure the wallet is registered as a signer
	origPeers := ss.Config.Peers
	defer func() {
		ss.Config.Peers = origPeers
	}()
	ss.Config.Signers = []registrar.Peer{{Host: "discovery.node", Wallet: "0xF2974c76a6EFaf0338c69e467d843D54e6a91fdE"}}

	cid := "QmP4b7jYPeb4tbdCpd1qkP4zkDteb5zMZa8Yk46whtNYv2"
	signature := "%7B%22data%22%3A%20%22%7B%5C%22trackId%5C%22%3A%20220350%2C%20%5C%22cid%5C%22%3A%20%5C%22QmP4b7jYPeb4tbdCpd1qkP4zkDteb5zMZa8Yk46whtNYv2%5C%22%2C%20%5C%22timestamp%5C%22%3A%201596159123000%2C%20%5C%22shouldCache%5C%22%3A%201%7D%22%2C%20%22signature%22%3A%20%220x0f0627a064bd2c3f8add3214e10c85c727ba9a73fe8b1d689c6322f872dff5c263f2adbb156fadcb600b19787c704a0d608e5a4d0bb50748bbe3c2ba6ad262291c%22%7D"
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tracks/cidstream/%s?signature=%s", cid, signature), nil)

	rec := httptest.NewRecorder()
	c := ss.echo.NewContext(req, rec)
	c.SetPath("/tracks/cidstream/:cid")
	c.SetParamNames("cid")
	c.SetParamValues(cid)

	// Handle the request
	h := ss.requireRegisteredSignature(func(c echo.Context) error {
		return c.String(http.StatusOK, "test")
	})
	h(c)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "signature too old")
}

// fixtureSignerWallet returns the checksummed wallet the fixture signature recovers to.
func fixtureSignerWallet(t *testing.T) string {
	t.Helper()

	decoded, err := url.QueryUnescape(testSigParam)
	require.NoError(t, err)
	sig, err := signature.ParseFromQueryString(decoded)
	require.NoError(t, err)
	require.NotEqual(t, strings.ToLower(sig.SignerWallet), sig.SignerWallet,
		"fixture wallet must be mixed-case for the casing tests to mean anything")

	return sig.SignerWallet
}

// ensureTrackAccessTables creates the tables the cidstream access check reads.
//
// sound_recordings and management_keys belong to core, which shares a database with
// mediorum in production. The mediorum unit-test database only runs mediorum's own
// migrations, so the tables have to be created here. Since the check fails closed,
// a server missing these tables denies every request -- so even the tests that
// expect an ungated track need the tables to exist and simply hold no rows for the
// cid under test.
func ensureTrackAccessTables(t *testing.T, ss *MediorumServer) {
	t.Helper()

	for _, stmt := range []string{
		`create table if not exists sound_recordings(
			id serial primary key,
			sound_recording_id text not null,
			track_id text not null,
			cid text not null unique,
			encoding_details text
		)`,
		`create table if not exists management_keys(
			id serial primary key,
			track_id text not null,
			address text not null
		)`,
	} {
		require.NoError(t, ss.crud.DB.Exec(stmt).Error)
	}
}

// setupAccessAuthorityFixture points the fixture cid at a track whose only access
// authority is authority, and clears the surrounding state the check depends on.
func setupAccessAuthorityFixture(t *testing.T, ss *MediorumServer, authority string) {
	t.Helper()

	ensureTrackAccessTables(t, ss)

	require.NoError(t, ss.crud.DB.Exec(
		`insert into sound_recordings (sound_recording_id, track_id, cid) values (?, ?, ?)
		 on conflict (cid) do update set track_id = excluded.track_id`,
		"sr_"+testSigTrackID, testSigTrackID, testSigCid).Error)
	require.NoError(t, ss.crud.DB.Exec(
		`insert into management_keys (track_id, address) values (?, ?)`,
		testSigTrackID, authority).Error)

	// requireRegisteredSignature memoizes the track lookup per cid for 5 minutes.
	ss.trackAccessInfoCache.Remove(testSigCid)

	origSigners, origPeers := ss.Config.Signers, ss.Config.Peers
	ss.Config.Signers = []registrar.Peer{}
	ss.Config.Peers = []registrar.Peer{}

	t.Cleanup(func() {
		ss.Config.Signers, ss.Config.Peers = origSigners, origPeers
		ss.trackAccessInfoCache.Remove(testSigCid)
		ss.crud.DB.Exec(`delete from management_keys where track_id = ?`, testSigTrackID)
		ss.crud.DB.Exec(`delete from sound_recordings where cid = ?`, testSigCid)
	})
}

func serveAccessAuthorityRequest(t *testing.T, ss *MediorumServer) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tracks/cidstream/%s?signature=%s", testSigCid, testSigParam), nil)
	rec := httptest.NewRecorder()
	c := ss.echo.NewContext(req, rec)
	c.SetPath("/tracks/cidstream/:cid")
	c.SetParamNames("cid")
	c.SetParamValues(testSigCid)

	h := ss.requireRegisteredSignature(func(c echo.Context) error {
		return c.String(http.StatusOK, "test")
	})
	h(c)

	return rec
}

// The signer wallet recovered from a stream signature is always EIP-55 checksummed,
// while access_authorities are stored lowercase. An exact-match comparison rejects
// every such request; this is the regression that broke gated streaming.
func TestRequireRegisteredSignatureWithLowercaseAccessAuthority(t *testing.T) {
	ss := testNetwork[1]

	setupAccessAuthorityFixture(t, ss, strings.ToLower(fixtureSignerWallet(t)))

	rec := serveAccessAuthorityRequest(t, ss)

	body := rec.Body.String()
	assert.NotContains(t, body, "access_authorities",
		"checksummed signer should match its lowercase access authority")
	// Getting as far as the age check proves the access_authorities check passed.
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, body, "signature too old")
}

func TestRequireRegisteredSignatureWithUnrelatedAccessAuthority(t *testing.T) {
	ss := testNetwork[1]

	setupAccessAuthorityFixture(t, ss, "0x1234567890123456789012345678901234567890")

	rec := serveAccessAuthorityRequest(t, ss)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "signer not authorized for this track (access_authorities)")
}

// failEveryQuery points the server's gorm handle at an already-cancelled context,
// so every query it issues fails at the driver rather than returning zero rows.
// Stands in for the whole database being unreachable. The returned func restores
// the working handle early, for tests that need to run queries afterwards; it is
// also registered as cleanup, and calling it twice is harmless.
func failEveryQuery(t *testing.T, ss *MediorumServer) (restore func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	orig := ss.crud.DB
	ss.crud.DB = orig.WithContext(ctx)
	restore = func() { ss.crud.DB = orig }
	// Registered after any fixture cleanup, so LIFO ordering restores the working
	// handle before the fixture tries to delete its rows.
	t.Cleanup(restore)

	return restore
}

// hideManagementKeys renames management_keys out from under the queries that read
// it, leaving sound_recordings intact. This reproduces the failure empirically
// found while renaming these tables: the cid -> track_id lookup still succeeds, so
// only the access-authority queries error.
func hideManagementKeys(t *testing.T, ss *MediorumServer) {
	t.Helper()

	require.NoError(t, ss.crud.DB.Exec(`alter table management_keys rename to management_keys_hidden`).Error)
	t.Cleanup(func() {
		ss.crud.DB.Exec(`alter table management_keys_hidden rename to management_keys`)
	})
}

// A failed lookup must deny rather than fall through to the validator-signature
// branch. Zero rows and a failed query are indistinguishable in the result, so
// treating the failure as "no access authorities" hands a gated track to any
// registered signer -- the whole point of failing closed.
func TestRequireRegisteredSignatureDeniesWhenTrackLookupFails(t *testing.T) {
	ss := testNetwork[1]

	// Gate the track behind an authority that is not the fixture signer...
	setupAccessAuthorityFixture(t, ss, "0x1234567890123456789012345678901234567890")
	// ...while registering the fixture signer as a validator, so a fail-open
	// lookup would sail straight through the no-access-authorities branch.
	ss.Config.Signers = []registrar.Peer{{Host: "validator.node", Wallet: fixtureSignerWallet(t)}}

	failEveryQuery(t, ss)

	rec := serveAccessAuthorityRequest(t, ss)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "unable to verify track access")
	assert.Contains(t, body, "track access lookup failed")
	// "signature too old" here would mean the request got past authorization.
	assert.NotContains(t, body, "signature too old")
}

// Same fail-closed requirement for the management_keys count: an errored query
// must not read as "this track is ungated".
func TestRequireRegisteredSignatureDeniesWhenManagementKeyCountFails(t *testing.T) {
	ss := testNetwork[1]

	setupAccessAuthorityFixture(t, ss, "0x1234567890123456789012345678901234567890")
	ss.Config.Signers = []registrar.Peer{{Host: "validator.node", Wallet: fixtureSignerWallet(t)}}

	hideManagementKeys(t, ss)

	rec := serveAccessAuthorityRequest(t, ss)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "track access lookup failed")
}

// The per-signer authority check runs after the cached lookup, so it needs its
// own guard.
func TestRequireRegisteredSignatureDeniesWhenAccessAuthorityLookupFails(t *testing.T) {
	ss := testNetwork[1]

	setupAccessAuthorityFixture(t, ss, strings.ToLower(fixtureSignerWallet(t)))

	// Prime the cache so the gated verdict survives, then break only the query
	// that decides whether this signer is one of the authorities.
	ss.trackAccessInfoCache.Set(testSigCid, trackAccessInfo{TrackID: testSigTrackID, ManagementKeyCount: 1},
		imcache.WithExpiration(5*time.Minute))
	hideManagementKeys(t, ss)

	rec := serveAccessAuthorityRequest(t, ss)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "access authority lookup failed")
}

// A denial caused by a transient query failure must not be memoized: the next
// request, once the database recovers, has to see the real gating state.
func TestRequireRegisteredSignatureDoesNotCacheFailedLookup(t *testing.T) {
	ss := testNetwork[1]

	setupAccessAuthorityFixture(t, ss, strings.ToLower(fixtureSignerWallet(t)))

	restore := failEveryQuery(t, ss)
	rec := serveAccessAuthorityRequest(t, ss)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	restore()

	_, cached := ss.trackAccessInfoCache.Get(testSigCid)
	assert.False(t, cached, "a failed lookup must not populate the track access cache")

	// The signer is this track's access authority, so the recovered request gets
	// as far as the age check.
	rec = serveAccessAuthorityRequest(t, ss)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "signature too old")
}

// serveTrackRequest exercises serveTrack directly. serveTrack is dev-only, so the
// env is flipped for the duration of the call.
func serveTrackRequest(t *testing.T, ss *MediorumServer) *httptest.ResponseRecorder {
	t.Helper()

	origEnv := ss.Config.Env
	ss.Config.Env = "dev"
	defer func() { ss.Config.Env = origEnv }()

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tracks/%s/stream?signature=%s", testSigTrackID, testSigParam), nil)
	rec := httptest.NewRecorder()
	c := ss.echo.NewContext(req, rec)
	c.SetPath("/tracks/:trackId/stream")
	c.SetParamNames("trackId")
	c.SetParamValues(testSigTrackID)

	if err := ss.serveTrack(c); err != nil {
		ss.echo.HTTPErrorHandler(err, c)
	}

	return rec
}

func TestServeTrackRejectsUnrelatedSigner(t *testing.T) {
	ss := testNetwork[1]

	setupAccessAuthorityFixture(t, ss, "0x1234567890123456789012345678901234567890")

	rec := serveTrackRequest(t, ss)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "signer not authorized to access")
}

func TestServeTrackDeniesWhenCidLookupFails(t *testing.T) {
	ss := testNetwork[1]

	setupAccessAuthorityFixture(t, ss, strings.ToLower(fixtureSignerWallet(t)))
	failEveryQuery(t, ss)

	rec := serveTrackRequest(t, ss)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "unable to verify track access")
	assert.Contains(t, body, "track lookup failed")
	// A swallowed error would have surfaced as "track not found" instead.
	assert.NotContains(t, body, "track not found")
}

func TestServeTrackDeniesWhenAccessAuthorityLookupFails(t *testing.T) {
	ss := testNetwork[1]

	setupAccessAuthorityFixture(t, ss, strings.ToLower(fixtureSignerWallet(t)))
	hideManagementKeys(t, ss)

	rec := serveTrackRequest(t, ss)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "access authority lookup failed")
	assert.NotContains(t, body, "signer not authorized to access")
}

// streamTrackSignature signs the fixture track with key, the way a client would.
func streamTrackSignature(t *testing.T, key *ecdsa.PrivateKey) *v1storage.StreamTrackSignature {
	t.Helper()

	data := &v1storage.StreamTrackSignatureData{
		TrackId:   testSigTrackID,
		Timestamp: 1596159123000,
	}
	sig, dataHash, err := common.GeneratePlaySignature(key, data)
	require.NoError(t, err)

	return &v1storage.StreamTrackSignature{Signature: sig, DataHash: dataHash, Data: data}
}

// streamTrackGRPC returns before it touches the stream on every path tested here,
// so a nil stream is enough -- connect exposes no way to build a real one outside
// a served request.
func streamTrackGRPCRequest(t *testing.T, ss *MediorumServer) error {
	t.Helper()

	origEnv := ss.Config.Env
	ss.Config.Env = "dev"
	defer func() { ss.Config.Env = origEnv }()

	req := &v1storage.StreamTrackRequest{Signature: streamTrackSignature(t, generateTestPrivateKey(1))}

	return ss.streamTrackGRPC(context.Background(), req, nil)
}

func TestStreamTrackGRPCDeniesWhenCidLookupFails(t *testing.T) {
	ss := testNetwork[1]

	setupAccessAuthorityFixture(t, ss, "0x1234567890123456789012345678901234567890")
	failEveryQuery(t, ss)

	err := streamTrackGRPCRequest(t, ss)

	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err), "a failed lookup must not read as CodeNotFound")
	assert.Contains(t, err.Error(), "unable to verify track access")
}

func TestStreamTrackGRPCDeniesWhenAccessAuthorityLookupFails(t *testing.T) {
	ss := testNetwork[1]

	setupAccessAuthorityFixture(t, ss, "0x1234567890123456789012345678901234567890")
	hideManagementKeys(t, ss)

	err := streamTrackGRPCRequest(t, ss)

	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err), "a failed lookup must not read as CodePermissionDenied")
	assert.Contains(t, err.Error(), "unable to verify track access")
}
