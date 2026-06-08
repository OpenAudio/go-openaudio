package entity_manager

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Regression: name/bio/description/handle limits are CHARACTER limits, not byte
// limits. A value with multi-byte runes (e.g. emoji) that is within the rune
// limit must pass even when its UTF-8 byte length exceeds the limit. Previously
// these checks used len(), which counts bytes, so a valid profile was silently
// rejected -- and because setting an artist pick re-submits the whole user
// profile (including the unchanged name), that rejection blocked every user
// update, not just the name change.
func TestValidateUserName_CountsRunesNotBytes(t *testing.T) {
	// Real-world name that triggered the bug: 31 runes, 34 bytes (🖤 is 4 bytes),
	// within the 32-character limit but over it when counted as bytes.
	name := "g1ld3dd (artist/producer) PHL 🖤"
	if rc, bc := utf8.RuneCountInString(name), len(name); rc > CharacterLimitUserName || bc <= CharacterLimitUserName {
		t.Fatalf("fixture invalid: runes=%d bytes=%d limit=%d (want runes<=limit<bytes)", rc, bc, CharacterLimitUserName)
	}
	if err := ValidateUserName(name); err != nil {
		t.Errorf("ValidateUserName(%q) = %v, want nil (31 runes within %d-char limit)", name, err, CharacterLimitUserName)
	}

	// Exactly at the rune limit passes; one rune over fails.
	if err := ValidateUserName(strings.Repeat("é", CharacterLimitUserName)); err != nil {
		t.Errorf("name of %d multi-byte runes should pass: %v", CharacterLimitUserName, err)
	}
	if err := ValidateUserName(strings.Repeat("é", CharacterLimitUserName+1)); err == nil {
		t.Errorf("name of %d runes should exceed %d-char limit", CharacterLimitUserName+1, CharacterLimitUserName)
	}
}

func TestValidateBio_CountsRunesNotBytes(t *testing.T) {
	if err := ValidateBio(strings.Repeat("🎵", CharacterLimitUserBio)); err != nil {
		t.Errorf("bio of %d runes should pass: %v", CharacterLimitUserBio, err)
	}
	if err := ValidateBio(strings.Repeat("🎵", CharacterLimitUserBio+1)); err == nil {
		t.Errorf("bio of %d runes should exceed %d-char limit", CharacterLimitUserBio+1, CharacterLimitUserBio)
	}
}
