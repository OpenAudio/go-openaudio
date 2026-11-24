package mirror

import (
	"context"
	"crypto/ecdsa"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

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
// A track written through the SDK or a developer app often names no
// audio_upload_id at all. For those the mirror looks for an upload made for
// the track's user in the hour before the track's block, holding the original
// and transcode the track names.
//
// Replacing a track's audio uploads a new file and updates the track to name
// it, but the update keeps the audio_upload_id of the track's first upload.
// When the named upload holds a different file, the mirror looks for the
// upload that does: one made later for the same user, holding the original the
// track names and the transcode it names.
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
	CreatedAt   time.Time         `json:"created_at"`
}

// UploadSource looks up uploads on the old chain's storage.
type UploadSource interface {
	Upload(ctx context.Context, id string) (*UploadRecord, error)
	// UploadsAfter lists uploads created after a time, oldest first, a page
	// at a time.
	UploadsAfter(ctx context.Context, after time.Time) ([]UploadRecord, error)
}

const (
	// maxUploadPages bounds a search of the upload list.
	maxUploadPages = 25
	// unnamedUploadWindow is how long before its block a track naming no
	// upload is searched for one.
	unnamedUploadWindow = time.Hour
	// uploadClockSlack allows for a storage node's clock running ahead of
	// the chain's.
	uploadClockSlack = time.Minute
)

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
// at is the time of tx's source block, zero if unknown.
func (a *Attester) attestationFor(ctx context.Context, tx *corev1.SignedTransaction, at time.Time) (*corev1.SignedTransaction, error) {
	me := tx.GetManageEntity()
	if me == nil || me.GetEntityType() != "Track" {
		return nil, errNotAttestable{"not a track write"}
	}
	meta := parseMetadata(me.GetMetadata())
	orig := metaString(meta, "orig_file_cid")
	if orig == "" {
		return nil, errNotAttestable{"track names no orig_file_cid"}
	}
	var until time.Time
	if !at.IsZero() {
		until = at.Add(uploadClockSlack)
	}

	uploadID := metaString(meta, "audio_upload_id")
	if uploadID == "" {
		if at.IsZero() {
			return nil, errNotAttestable{"track names no audio_upload_id"}
		}
		up, err := a.findUpload(ctx, me.GetUserId(), meta, at.Add(-unnamedUploadWindow), until)
		if err != nil {
			return nil, err
		}
		if up == nil {
			return nil, errNotAttestable{fmt.Sprintf("track names no audio_upload_id, and no upload for user %d in the hour before it holds its audio", me.GetUserId())}
		}
		return a.attest(me.GetUserId(), meta, up)
	}

	up, err := a.uploads.Upload(ctx, uploadID)
	if errors.Is(err, errUploadNotFound) {
		return nil, errNotAttestable{fmt.Sprintf("upload %s does not exist", uploadID)}
	}
	if err != nil {
		return nil, fmt.Errorf("look up upload %s: %w", uploadID, err)
	}
	if up.OrigFileCID != orig {
		replaced, err := a.findUpload(ctx, me.GetUserId(), meta, up.CreatedAt, until)
		if err != nil {
			return nil, err
		}
		if replaced == nil {
			return nil, errNotAttestable{fmt.Sprintf("upload %s holds orig_file_cid %q, track names %q, and no later upload for user %d holds it", uploadID, up.OrigFileCID, orig, me.GetUserId())}
		}
		up = replaced
	}
	return a.attest(me.GetUserId(), meta, up)
}

// attest signs an attestation of the cids up produced that the track names,
// for the track's user.
func (a *Attester) attest(user int64, meta map[string]any, up *UploadRecord) (*corev1.SignedTransaction, error) {
	// A storage node attests for the upload's user. Attesting for anyone else
	// would claim what that node never would.
	if up.UserID.Valid && up.UserID.Int64 != 0 && up.UserID.Int64 != user {
		return nil, errNotAttestable{fmt.Sprintf("upload %s was made for user %d, track is by user %d", up.ID, up.UserID.Int64, user)}
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

	ca := &corev1.ContentAttestation{UserId: user, Cids: cids, ValidatorAddress: a.address}
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

// findUpload finds an upload made after after, and before until unless it is
// zero, for user, holding the original file the track names and, if it names
// one, its transcode. It returns nil when there is none within maxUploadPages.
func (a *Attester) findUpload(ctx context.Context, user int64, meta map[string]any, after, until time.Time) (*UploadRecord, error) {
	orig, transcoded := metaString(meta, "orig_file_cid"), metaString(meta, "track_cid")
	for range maxUploadPages {
		page, err := a.uploads.UploadsAfter(ctx, after)
		if err != nil {
			return nil, fmt.Errorf("list uploads after %s: %w", after.Format(time.RFC3339Nano), err)
		}
		for i := range page {
			up := &page[i]
			if !until.IsZero() && up.CreatedAt.After(until) {
				return nil, nil
			}
			if up.UserID.Valid && up.UserID.Int64 == user && up.OrigFileCID == orig &&
				(transcoded == "" || slices.Contains(slices.Collect(maps.Values(up.Results)), transcoded)) {
				return up, nil
			}
		}
		if len(page) == 0 || !page[len(page)-1].CreatedAt.After(after) {
			return nil, nil
		}
		after = page[len(page)-1].CreatedAt
	}
	return nil, nil
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

func (u *HTTPUploads) UploadsAfter(ctx context.Context, after time.Time) ([]UploadRecord, error) {
	q := url.Values{"after": {after.UTC().Format(time.RFC3339Nano)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.base+"/uploads?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
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
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /uploads: %s: %s", resp.Status, truncate(body))
	}
	var ups []UploadRecord
	if err := json.Unmarshal(body, &ups); err != nil {
		return nil, fmt.Errorf("GET /uploads: decode: %w", err)
	}
	return ups, nil
}

// txHash is the hash the target commits tx under.
func txHash(tx *corev1.SignedTransaction) (string, error) {
	b, err := proto.Marshal(tx)
	if err != nil {
		return "", err
	}
	return common.ToTxHashFromBytes(b), nil
}
