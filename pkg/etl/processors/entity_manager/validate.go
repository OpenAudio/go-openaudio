package entity_manager

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/OpenAudio/go-openaudio/etl/db"
)

var handleRegexp = regexp.MustCompile(`^[a-z0-9_.]+$`)

// Reserved handles that cannot be used.
var reservedHandles = map[string]bool{
	"admin": true, "audius": true, "api": true, "app": true, "blog": true,
	"contact": true, "dashboard": true, "dev": true, "developer": true,
	"docs": true, "download": true, "explore": true, "faq": true,
	"feed": true, "help": true, "home": true, "jobs": true, "legal": true,
	"library": true, "login": true, "logout": true, "manage": true,
	"media": true, "music": true, "news": true, "notifications": true,
	"oauth": true, "playlist": true, "playlists": true, "premium": true,
	"press": true, "privacy": true, "profile": true, "register": true,
	"search": true, "settings": true, "signup": true, "status": true,
	"support": true, "terms": true, "track": true, "tracks": true,
	"trending": true, "upload": true, "user": true, "users": true,
	"verify": true,
}

// ValidateHandle checks handle format, length, and reserved words.
func ValidateHandle(handle string) error {
	if handle == "" {
		return NewValidationError("handle is missing")
	}
	handle = strings.ToLower(handle)
	if !handleRegexp.MatchString(handle) {
		return NewValidationError("handle %q contains illegal characters", handle)
	}
	if len(handle) > CharacterLimitHandle {
		return NewValidationError("handle %q exceeds %d character limit", handle, CharacterLimitHandle)
	}
	if reservedHandles[handle] {
		return NewValidationError("handle %q is reserved", handle)
	}
	return nil
}

// ValidateUserName checks name length.
func ValidateUserName(name string) error {
	if name == "" {
		return nil
	}
	if len(name) > CharacterLimitUserName {
		return NewValidationError("name exceeds %d character limit", CharacterLimitUserName)
	}
	return nil
}

// ValidateBio checks bio length.
func ValidateBio(bio string) error {
	if bio == "" {
		return nil
	}
	if len(bio) > CharacterLimitUserBio {
		return NewValidationError("bio exceeds %d character limit", CharacterLimitUserBio)
	}
	return nil
}

// ValidateDescription checks description length (used for tracks, playlists).
func ValidateDescription(desc string) error {
	if desc == "" {
		return nil
	}
	if len(desc) > CharacterLimitDescription {
		return NewValidationError("description exceeds %d character limit", CharacterLimitDescription)
	}
	return nil
}

// ValidateSigner checks that the signer matches the user's wallet or has a valid grant.
// For now this does a direct wallet comparison. Grant/DeveloperApp authorization
// will be added as those entity types are implemented.
func ValidateSigner(ctx context.Context, params *Params) error {
	wallet, err := getUserWallet(ctx, params.DBTX, params.UserID)
	if err != nil {
		return err
	}
	if wallet == "" {
		return NewValidationError("user %d does not exist", params.UserID)
	}
	if !strings.EqualFold(wallet, params.Signer) {
		return NewValidationError("signer %s does not match user %d wallet", params.Signer, params.UserID)
	}
	return nil
}

func getUserWallet(ctx context.Context, dbtx db.DBTX, userID int64) (string, error) {
	row := dbtx.QueryRow(ctx, "SELECT wallet FROM users WHERE user_id = $1 AND is_current = true LIMIT 1", userID)
	var wallet string
	if err := row.Scan(&wallet); err != nil {
		return "", nil
	}
	return wallet, nil
}

// ValidateGenre checks genre is in the allowlist.
func ValidateGenre(genre string) error {
	if genre == "" {
		return nil
	}
	if _, ok := GenreAllowlist[genre]; !ok {
		return NewValidationError("genre %q is not in the allow list", genre)
	}
	return nil
}

// ValidateAccessConditions checks gating field consistency, matching
// Only validates when gating fields are present in metadata.
func ValidateAccessConditions(p *Params) error {
	// Only validate if any gating field is present in metadata.
	_, hasSG := p.Metadata["is_stream_gated"]
	_, hasDG := p.Metadata["is_download_gated"]
	_, hasSC := p.Metadata["stream_conditions"]
	_, hasDC := p.Metadata["download_conditions"]
	if !hasSG && !hasDG && !hasSC && !hasDC {
		return nil
	}

	isStreamGated := p.MetadataBoolOr("is_stream_gated", false)
	isDownloadGated := p.MetadataBoolOr("is_download_gated", false)
	streamConditions, _ := p.MetadataJSON("stream_conditions")
	downloadConditions, _ := p.MetadataJSON("download_conditions")

	// Stem tracks cannot be gated.
	if stemOf, ok := p.MetadataJSON("stem_of"); ok && stemOf != nil {
		if isStreamGated || isDownloadGated {
			return NewValidationError("stem tracks cannot have stream or download gating")
		}
	}

	// Validate USDC purchase splits for both condition sets.
	if err := validateUSDCSplits(streamConditions); err != nil {
		return err
	}
	if err := validateUSDCSplits(downloadConditions); err != nil {
		return err
	}

	if isStreamGated {
		scMap, ok := streamConditions.(map[string]any)
		if !ok || len(scMap) == 0 {
			return NewValidationError("stream gated track must have stream_conditions")
		}
		if len(scMap) != 1 {
			return NewValidationError("stream_conditions must have exactly one condition type")
		}
		if !isDownloadGated {
			return NewValidationError("stream gated track must also be download gated")
		}
		// stream_conditions and download_conditions must be equal (marshaled comparison)
		if !jsonEqual(streamConditions, downloadConditions) {
			return NewValidationError("stream_conditions must match download_conditions for stream gated tracks")
		}
	} else if isDownloadGated {
		dcMap, ok := downloadConditions.(map[string]any)
		if !ok || len(dcMap) == 0 {
			return NewValidationError("download gated track must have download_conditions")
		}
		if len(dcMap) != 1 {
			return NewValidationError("download_conditions must have exactly one condition type")
		}
	}

	return nil
}

func validateUSDCSplits(conditions any) error {
	cMap, ok := conditions.(map[string]any)
	if !ok {
		return nil
	}
	usdc, ok := cMap["usdc_purchase"]
	if !ok {
		return nil
	}
	uMap, ok := usdc.(map[string]any)
	if !ok {
		return NewValidationError("usdc_purchase must be an object")
	}
	splits, ok := uMap["splits"]
	if !ok {
		return NewValidationError("usdc_purchase must contain splits")
	}
	switch s := splits.(type) {
	case []any:
		if len(s) == 0 {
			return NewValidationError("usdc_purchase splits cannot be empty")
		}
	case map[string]any:
		if len(s) == 0 {
			return NewValidationError("usdc_purchase splits cannot be empty")
		}
	default:
		return NewValidationError("usdc_purchase splits must be an array or object")
	}
	return nil
}

func jsonEqual(a, b any) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}
