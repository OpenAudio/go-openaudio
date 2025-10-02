package common_test

import (
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/davecgh/go-spew/spew"
	"github.com/stretchr/testify/assert"
)

func TestObfuscate(t *testing.T) {
	obfuscated := common.Obfuscate("test")
	assert.NotEmpty(t, obfuscated)
	spew.Dump(obfuscated)
}

func TestDeobfuscate(t *testing.T) {
	obfuscated := common.Obfuscate("test")
	deobfuscated, err := common.Deobfuscate(obfuscated)
	assert.NoError(t, err)
	assert.Equal(t, "test", deobfuscated)
}
