package server

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// The stateless user-metadata rules the indexer applies to live user creates
// and updates. Core keeps its own copy: a create the indexer rejects must not
// land in core_auth_users either, or the leftover row holds its wallet and
// handle against a later create the indexer accepts.
//
// These rules are not height-gated. A rejection in FinalizeBlock is a logged
// skip that leaves the tx result unchanged, and the app hash covers only the
// block's transactions, so a node replaying old blocks under these rules
// commits the same headers as one that applied them later. Its core_auth_*
// state differs only by rows the indexer never had.
const (
	authHandleMaxRunes = 30
	authNameMaxRunes   = 32
	authBioMaxRunes    = 256
)

var authHandleRegexp = regexp.MustCompile(`^[a-z0-9_.]+$`)

var authReservedHandles = map[string]bool{
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

// validateAuthHandle reports why a handle would be refused, given its
// lowercased form. An empty handle is the caller's concern: creates may omit
// it and updates only check a handle they change.
func validateAuthHandle(handleLC string) error {
	if !authHandleRegexp.MatchString(handleLC) {
		return authValidationErrorf("handle %q contains illegal characters", handleLC)
	}
	if utf8.RuneCountInString(handleLC) > authHandleMaxRunes {
		return authValidationErrorf("handle %q exceeds %d character limit", handleLC, authHandleMaxRunes)
	}
	if authReservedHandles[handleLC] {
		return authValidationErrorf("handle %q is reserved", handleLC)
	}
	return nil
}

// validateLiveUserMetadata applies the stateless rules to a live user create
// or update. Migration rows replay legacy state verbatim and are not checked.
func validateLiveUserMetadata(tx authTx) error {
	if bio := tx.metaString("bio"); bio != "" && utf8.RuneCountInString(bio) > authBioMaxRunes {
		return authValidationErrorf("bio exceeds %d character limit", authBioMaxRunes)
	}
	if name := tx.metaString("name"); name != "" && utf8.RuneCountInString(name) > authNameMaxRunes {
		return authValidationErrorf("name exceeds %d character limit", authNameMaxRunes)
	}
	if handle := tx.metaString("handle"); handle != "" {
		if err := validateAuthHandle(strings.ToLower(handle)); err != nil {
			return err
		}
	}
	return nil
}
