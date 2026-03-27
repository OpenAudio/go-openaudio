package entity_manager

import (
	"context"
	"regexp"
	"strings"

	"github.com/OpenAudio/go-openaudio/etl/db"
)

var handleRegexp = regexp.MustCompile(`^[a-z0-9_.]+$`)

// Reserved handles that cannot be used (subset matching discovery-provider).
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
