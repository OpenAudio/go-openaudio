package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"net/http/httptest"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/server/signature"
	"github.com/OpenAudio/go-openaudio/pkg/registrar"
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

// setupAccessAuthorityFixture points the fixture cid at a track whose only access
// authority is authority, and clears the surrounding state the check depends on.
//
// core_sound_recordings and core_management_keys belong to core, which shares a database with
// mediorum in production. The mediorum unit-test database only runs mediorum's own
// migrations, so the tables have to be created here.
func setupAccessAuthorityFixture(t *testing.T, ss *MediorumServer, authority string) {
	t.Helper()

	for _, stmt := range []string{
		`create table if not exists core_sound_recordings(
			id serial primary key,
			sound_recording_id text not null,
			track_id text not null,
			cid text not null unique,
			encoding_details text
		)`,
		`create table if not exists core_management_keys(
			id serial primary key,
			track_id text not null,
			address text not null
		)`,
	} {
		require.NoError(t, ss.crud.DB.Exec(stmt).Error)
	}

	require.NoError(t, ss.crud.DB.Exec(
		`insert into core_sound_recordings (sound_recording_id, track_id, cid) values (?, ?, ?)
		 on conflict (cid) do update set track_id = excluded.track_id`,
		"sr_"+testSigTrackID, testSigTrackID, testSigCid).Error)
	require.NoError(t, ss.crud.DB.Exec(
		`insert into core_management_keys (track_id, address) values (?, ?)`,
		testSigTrackID, authority).Error)

	// requireRegisteredSignature memoizes the track lookup per cid for 5 minutes.
	ss.trackAccessInfoCache.Remove(testSigCid)

	origSigners, origPeers := ss.Config.Signers, ss.Config.Peers
	ss.Config.Signers = []registrar.Peer{}
	ss.Config.Peers = []registrar.Peer{}

	t.Cleanup(func() {
		ss.Config.Signers, ss.Config.Peers = origSigners, origPeers
		ss.trackAccessInfoCache.Remove(testSigCid)
		ss.crud.DB.Exec(`delete from core_management_keys where track_id = ?`, testSigTrackID)
		ss.crud.DB.Exec(`delete from core_sound_recordings where cid = ?`, testSigCid)
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
