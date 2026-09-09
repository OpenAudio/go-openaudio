package integration_tests

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/bdragon300/tusgo"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	storagev1 "github.com/OpenAudio/go-openaudio/pkg/api/storage/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/OpenAudio/go-openaudio/pkg/core/server"
	"github.com/OpenAudio/go-openaudio/pkg/integration_tests/utils"
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
)

// Content authorization end to end, on a devnet where both AuthEnforcement
// and ContentAuthEnforcement are active from height 1 (upgrades.go).
//
// The hole this closes: a track's cid fields were unchecked client metadata,
// so anyone could name a gated track's cid on a decoy they own and stream it.
// Now a validator attests the cids it transcodes to the user the upload was
// made for, and consensus rejects a track naming a cid its user holds no
// claim to. Unit tests pin each piece; this is the only place the whole chain
// runs: tus upload with an asserted user → transcode → attestation committed
// before the upload reads done → track create validated against the claim at
// the mempool of a different node.
func TestContentAuth(t *testing.T) {
	ctx := context.Background()
	require.NoError(t, utils.WaitForDevnetHealthy())

	// Uploads go to one node, writes to another: claims are consensus state,
	// so a node that never saw the bytes must still enforce them.
	storageNode := utils.ContentOne
	storageHost := utils.ContentOneRPC
	chainNode := utils.DiscoveryOne

	owner := newContentAuthActor(t)
	thief := newContentAuthActor(t)
	createUser(t, ctx, chainNode, owner)
	createUser(t, ctx, chainNode, thief)

	t.Run("AudioUploadWithoutUserIsRejectedAtCreate", func(t *testing.T) {
		_, resp, err := tusUploadAudio(ctx, storageHost, "./assets/anxiety-upgrade.mp3", "")
		require.Error(t, err, "an audio upload that names no user can never be attested, so create must refuse it")
		if resp != nil {
			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		}
	})

	uploadID, _, err := tusUploadAudio(ctx, storageHost, "./assets/anxiety-upgrade.mp3", fmt.Sprint(owner.id))
	require.NoError(t, err)
	upload := waitForUploadDone(t, ctx, storageNode, uploadID)
	trackCID := upload.TranscodeResults["320"]
	require.NotEmpty(t, trackCID, "transcode produced no 320 cid")
	require.NotEmpty(t, upload.OrigFileCid)

	trackID := nextTrackID()
	cids := map[string]any{
		"track_cid":     trackCID,
		"orig_file_cid": upload.OrigFileCid,
	}

	t.Run("OwnerCreatesTrackNamingAttestedCids", func(t *testing.T) {
		_, err := sendManageEntity(ctx, chainNode, owner, "Track", trackID, "Create",
			trackMetadata(owner, "Anxiety Upgrade", cids))
		require.NoError(t, err, "the upload reads done only after the attestation committed, so the create must pass")
	})

	t.Run("AnotherUserCannotNameTheSameCid", func(t *testing.T) {
		_, err := sendManageEntity(ctx, chainNode, thief, "Track", nextTrackID(), "Create",
			trackMetadata(thief, "Decoy", cids))
		require.Error(t, err, "naming someone else's cid on your own track is the attack")
		require.Contains(t, err.Error(), "was not uploaded for user")
	})

	t.Run("UnattestedCidGoesToFirstAsserter", func(t *testing.T) {
		// Audio that reached storage without an attestation (an upload before
		// the node ran the gate) is publishable by whoever names it first, and
		// theirs alone from then on.
		unattested := map[string]any{"track_cid": "QmYwAPJzv5CZsnA625s3Xf2nemtYgPpHdWEz79ojWnPbdG"}
		_, err := sendManageEntity(ctx, chainNode, owner, "Track", nextTrackID(), "Create",
			trackMetadata(owner, "Never attested", unattested))
		require.NoError(t, err)

		_, err = sendManageEntity(ctx, chainNode, thief, "Track", nextTrackID(), "Create",
			trackMetadata(thief, "Decoy of never attested", unattested))
		require.Error(t, err)
		require.Contains(t, err.Error(), "was not uploaded for user")
	})

	// Track updates are not projected at consensus (ownership on update is the
	// ETL's rule), so the two edit cases exercise the content-auth check alone.
	t.Run("OwnerEditResendingCidsIsAccepted", func(t *testing.T) {
		// The web client resends every cid field on a metadata edit, so an
		// edit is a fresh assertion of the same cids and must keep passing.
		_, err := sendManageEntity(ctx, chainNode, owner, "Track", trackID, "Update",
			trackMetadata(owner, "Anxiety Upgrade (edited)", cids))
		require.NoError(t, err)
	})

	t.Run("MetadataOnlyEditIsAccepted", func(t *testing.T) {
		_, err := sendManageEntity(ctx, chainNode, owner, "Track", trackID, "Update",
			trackMetadata(owner, "Anxiety Upgrade (no cids)", nil))
		require.NoError(t, err, "only cids present in the transaction are checked")
	})
}

// contentAuthActor is a user with its own wallet. Both users and tracks get
// fresh ids so the test does not collide with the other suites on the shared
// devnet, and the wallet is fresh so the auth projection's uniqueness rules
// accept the create.
type contentAuthActor struct {
	id      int64
	key     *ecdsa.PrivateKey
	address string
}

var contentAuthSeq atomic.Int64

func newContentAuthActor(t *testing.T) contentAuthActor {
	t.Helper()
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	return contentAuthActor{
		id:      nextEntityID(),
		key:     key,
		address: crypto.PubkeyToAddress(key.PublicKey).String(),
	}
}

// nextEntityID is unique across a devnet run: wall-clock seconds keep it
// distinct across runs, the sequence keeps it distinct within one.
func nextEntityID() int64 {
	return (time.Now().Unix()%1_000_000)*1000 + contentAuthSeq.Add(1)
}

// nextTrackID respects the live track id offset the auth projection enforces
// (auth_state.go authTrackIDOffset).
func nextTrackID() int64 {
	return 2_000_000 + nextEntityID()
}

func createUser(t *testing.T, ctx context.Context, node *sdk.OpenAudioSDK, actor contentAuthActor) {
	t.Helper()
	meta := map[string]any{
		"cid": "",
		"data": map[string]any{
			"handle": fmt.Sprintf("contentauth_%d", actor.id),
			"name":   fmt.Sprintf("Content Auth %d", actor.id),
		},
	}
	_, err := sendManageEntity(ctx, node, actor, "User", actor.id, "Create", mustJSON(meta))
	require.NoError(t, err, "user create for %d", actor.id)
}

// sendManageEntity signs an EIP-712 manage-entity transaction as the actor and
// submits it. SendTransaction validates at the mempool and waits for the
// commit, so a nil error means the transaction is in a block and its auth
// effects are visible to the next call.
func sendManageEntity(ctx context.Context, node *sdk.OpenAudioSDK, actor contentAuthActor, entityType string, entityID int64, action, metadata string) (*corev1.SendTransactionResponse, error) {
	me := &corev1.ManageEntityLegacy{
		UserId:     actor.id,
		EntityType: entityType,
		EntityId:   entityID,
		Action:     action,
		Metadata:   metadata,
		Nonce:      fmt.Sprintf("0x%064x", nextEntityID()),
		Signer:     actor.address,
	}
	// Dev signing domain; must match what the devnet verifies against.
	if err := server.SignManageEntity(&config.Config{
		AcdcEntityManagerAddress: config.DevAcdcAddress,
		AcdcChainID:              config.DevAcdcChainID,
	}, me, actor.key); err != nil {
		return nil, err
	}
	resp, err := node.Core.SendTransaction(ctx, connect.NewRequest(&corev1.SendTransactionRequest{
		Transaction: &corev1.SignedTransaction{
			RequestId:   uuid.NewString(),
			Transaction: &corev1.SignedTransaction_ManageEntity{ManageEntity: me},
		},
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// trackMetadata builds the {"cid":..., "data":{...}} envelope the ETL and the
// auth projection both unwrap. The projection pins owner_id to the writing
// user on create, as the ETL does. cids may be nil for a metadata-only edit.
func trackMetadata(owner contentAuthActor, title string, cids map[string]any) string {
	data := map[string]any{
		"owner_id": owner.id,
		"title":    title,
		"genre":    "Electronic",
	}
	for k, v := range cids {
		data[k] = v
	}
	return mustJSON(map[string]any{"cid": "", "data": data})
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// tusUploadAudio creates a tus upload on the node with the metadata the
// mediorum hook reads (upload_auth.go resolveUploadUserID) and streams the
// file. userID "" omits the key entirely, which is the unattributed case.
// Returns the upload id and the create response so a rejection's status can
// be checked.
func tusUploadAudio(ctx context.Context, host, file, userID string) (string, *http.Response, error) {
	base, err := url.Parse(strings.TrimRight(ensureHTTPS(host), "/") + "/files/")
	if err != nil {
		return "", nil, err
	}
	client := tusgo.NewClient(utils.NewTestHTTPClient(), base)
	client.Capabilities = &tusgo.ServerCapabilities{
		Extensions:       []string{"creation", "creation-with-upload", "termination"},
		ProtocolVersions: []string{"1.0.0"},
	}

	f, err := os.Open(file)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", nil, err
	}

	meta := map[string]string{
		"filename": path.Base(file),
		"template": "audio",
	}
	if userID != "" {
		meta["userId"] = userID
	}

	up := tusgo.Upload{}
	resp, err := client.CreateUpload(&up, info.Size(), false, meta)
	if err != nil {
		return "", resp, fmt.Errorf("tus create: %w", err)
	}
	if _, err := io.Copy(tusgo.NewUploadStream(client, &up), f); err != nil {
		return "", resp, fmt.Errorf("tus upload: %w", err)
	}
	loc, err := url.Parse(up.Location)
	if err != nil {
		return "", resp, err
	}
	id := path.Base(strings.TrimRight(loc.Path, "/"))
	if id == "" || id == "." || id == "/" {
		return "", resp, fmt.Errorf("no upload id in location %q", up.Location)
	}
	return id, resp, nil
}

func ensureHTTPS(host string) string {
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return host
	}
	return "https://" + host
}

// waitForUploadDone polls until the upload reads done. Mediorum flips the
// status only after the attestation transaction has committed, so done means
// the cids are claimable; an error status is a hard failure, not a retry.
func waitForUploadDone(t *testing.T, ctx context.Context, node *sdk.OpenAudioSDK, id string) *storagev1.Upload {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		resp, err := node.Storage.GetUpload(ctx, connect.NewRequest(&storagev1.GetUploadRequest{Id: id}))
		if err == nil && resp.Msg.Upload != nil {
			u := resp.Msg.Upload
			if u.Error != "" {
				t.Fatalf("upload %s failed: %s", id, u.Error)
			}
			if u.Status == "done" {
				return u
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("upload %s not done after 4 minutes", id)
	return nil
}
