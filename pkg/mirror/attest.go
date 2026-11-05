package mirror

import (
	"context"
	"crypto/ecdsa"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	coreServer "github.com/OpenAudio/go-openaudio/pkg/core/server"
	"google.golang.org/protobuf/proto"
)

// Attestation stands in for the storage node an old-chain upload never reached.
//
// The new chain only accepts a track naming audio that a registered node has
// attested was uploaded for that track's user. On the new chain a node's
// storage attests as each upload finishes; an upload made to the old chain
// after the genesis snapshot was attested by nobody. A track naming audio
// nobody holds a claim for gets the claim by naming it first, so those mostly
// pass. A track naming audio someone else already holds -- the same file
// uploaded again by another account -- is refused, although on the old chain it
// was accepted.
//
// For that refusal the mirror does what the storage node would have done:
// look up the upload the track names, and if that upload holds the same
// original file, attest its cids for the track's user, wait for the
// attestation to commit, and retry the track once. Anything short of that
// match still halts.
//
// The upload's user is an assertion made at upload time, exactly as it is
// for a storage node's own attestation, so this grants nothing a live upload
// to the new chain would not have.

// UploadRecord is the part of a storage node's upload row the mirror checks.
type UploadRecord struct {
	ID          string            `json:"id"`
	OrigFileCID string            `json:"orig_file_cid"`
	Results     map[string]string `json:"results"`
	UserID      sql.NullInt64     `json:"user_id"`
}

// UploadSource looks up an upload by id on the old chain's storage.
type UploadSource interface {
	Upload(ctx context.Context, id string) (*UploadRecord, error)
}

// errUploadNotFound is a definite answer that the upload does not exist, as
// opposed to a failed lookup that is worth retrying.
var errUploadNotFound = errors.New("upload not found")

// Attester signs content attestations with a registered node's key.
type Attester struct {
	key     *ecdsa.PrivateKey
	address string
	uploads UploadSource
}

func NewAttester(key *ecdsa.PrivateKey, uploads UploadSource) *Attester {
	return &Attester{key: key, address: common.PrivKeyToAddress(key), uploads: uploads}
}

// Address is the node address the attestations are signed as.
func (a *Attester) Address() string { return a.address }

// LoadAttestKey reads a hex private key from a file, tolerating a 0x prefix
// and surrounding whitespace.
func LoadAttestKey(path string) (*ecdsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return common.EthToEthKey(strings.TrimPrefix(strings.TrimSpace(string(b)), "0x"))
}

// isUnclaimedCidRefusal reports whether a refusal is content auth turning
// away a track's audio, the one refusal an attestation can answer.
func isUnclaimedCidRefusal(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "was not uploaded for user") || strings.Contains(msg, "is not attested to any uploader")
}

// errNotAttestable means the transaction's own upload does not vouch for what
// it names, so the refusal stands.
type errNotAttestable struct{ reason string }

func (e errNotAttestable) Error() string { return "not attestable: " + e.reason }

// trackAudioKeys are the track metadata fields content auth checks.
var trackAudioKeys = []string{"track_cid", "preview_cid", "orig_file_cid"}

// attestationFor builds the signed attestation that lets tx's user name the
// audio tx names. It returns errNotAttestable when tx's upload does not
// vouch for that audio, and any other error when the lookup itself failed.
func (a *Attester) attestationFor(ctx context.Context, tx *corev1.SignedTransaction) (*corev1.SignedTransaction, error) {
	me := tx.GetManageEntity()
	if me == nil || me.GetEntityType() != "Track" {
		return nil, errNotAttestable{"not a track write"}
	}
	meta := parseMetadata(me.GetMetadata())
	uploadID := metaString(meta, "audio_upload_id")
	if uploadID == "" {
		return nil, errNotAttestable{"track names no audio_upload_id"}
	}
	orig := metaString(meta, "orig_file_cid")
	if orig == "" {
		return nil, errNotAttestable{"track names no orig_file_cid"}
	}

	up, err := a.uploads.Upload(ctx, uploadID)
	if errors.Is(err, errUploadNotFound) {
		return nil, errNotAttestable{fmt.Sprintf("upload %s does not exist", uploadID)}
	}
	if err != nil {
		return nil, fmt.Errorf("look up upload %s: %w", uploadID, err)
	}
	if up.OrigFileCID != orig {
		return nil, errNotAttestable{fmt.Sprintf("upload %s holds orig_file_cid %q, track names %q", uploadID, up.OrigFileCID, orig)}
	}
	// A storage node attests for the upload's user. Attesting for anyone else
	// would claim what that node never would.
	if up.UserID.Valid && up.UserID.Int64 != 0 && up.UserID.Int64 != me.GetUserId() {
		return nil, errNotAttestable{fmt.Sprintf("upload %s was made for user %d, track is by user %d", uploadID, up.UserID.Int64, me.GetUserId())}
	}

	// Attest only the cids this upload produced: the original and whatever
	// it was transcoded into, previews included.
	produced := map[string]bool{up.OrigFileCID: true}
	for _, c := range up.Results {
		produced[c] = true
	}
	var cids []string
	for _, key := range trackAudioKeys {
		if c := metaString(meta, key); c != "" && produced[c] && !slices.Contains(cids, c) {
			cids = append(cids, c)
		}
	}

	ca := &corev1.ContentAttestation{UserId: me.GetUserId(), Cids: cids, ValidatorAddress: a.address}
	payload, err := coreServer.ContentAttestationBytes(ca)
	if err != nil {
		return nil, fmt.Errorf("marshal attestation: %w", err)
	}
	// The signature is deterministic, so a rerun after a crash rebuilds the
	// same transaction and finds it already committed.
	if ca.ValidatorSignature, err = common.EthSign(a.key, payload); err != nil {
		return nil, fmt.Errorf("sign attestation: %w", err)
	}
	return &corev1.SignedTransaction{Transaction: &corev1.SignedTransaction_ContentAttestation{ContentAttestation: ca}}, nil
}

// parseMetadata unwraps a ManageEntity's {"cid": ..., "data": {...}}
// envelope the way core does when it checks content auth.
func parseMetadata(raw string) map[string]any {
	var meta map[string]any
	if json.Unmarshal([]byte(raw), &meta) != nil {
		return nil
	}
	if data, ok := meta["data"].(map[string]any); ok {
		return data
	}
	return meta
}

func metaString(meta map[string]any, key string) string {
	s, _ := meta[key].(string)
	return s
}

// HTTPUploads reads upload rows from a storage node's public GET /uploads/<id>.
type HTTPUploads struct {
	base   string
	client *http.Client
}

func NewHTTPUploads(nodeURL string, client *http.Client) *HTTPUploads {
	return &HTTPUploads{base: strings.TrimSuffix(nodeURL, "/"), client: client}
}

func (u *HTTPUploads) Upload(ctx context.Context, id string) (*UploadRecord, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.base+"/uploads/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	// Some nodes sit behind a CDN that turns away clients without one.
	req.Header.Set("User-Agent", "openaudio-mirror")
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, errUploadNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /uploads/%s: %s: %s", id, resp.Status, truncate(body))
	}
	var up UploadRecord
	if err := json.Unmarshal(body, &up); err != nil {
		return nil, fmt.Errorf("GET /uploads/%s: decode: %w", id, err)
	}
	if up.ID != id {
		return nil, fmt.Errorf("asked for upload %s, got %q", id, up.ID)
	}
	return &up, nil
}

// txHash is the hash the target commits tx under.
func txHash(tx *corev1.SignedTransaction) (string, error) {
	b, err := proto.Marshal(tx)
	if err != nil {
		return "", err
	}
	return common.ToTxHashFromBytes(b), nil
}
