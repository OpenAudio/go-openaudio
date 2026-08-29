package common

import (
	"encoding/hex"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func TestKeccak256Concat(t *testing.T) {
	tests := []struct {
		name  string
		parts [][]byte
	}{
		{name: "empty"},
		{name: "single", parts: [][]byte{[]byte("abc")}},
		{name: "multipart", parts: [][]byte{[]byte("ab"), nil, []byte("c")}},
		{name: "binary", parts: [][]byte{{0, 1, 2}, {0xff, 0x80}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			joined := make([]byte, 0)
			for _, part := range tt.parts {
				joined = append(joined, part...)
			}
			want := crypto.Keccak256(joined)
			got := Keccak256Concat(tt.parts...)
			require.Equal(t, hex.EncodeToString(want), hex.EncodeToString(got[:]))
		})
	}

	empty := Keccak256Concat()
	require.Equal(t, "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470", hex.EncodeToString(empty[:]))
}
