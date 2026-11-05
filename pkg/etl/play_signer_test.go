package etl

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/server/signature"
	"github.com/ethereum/go-ethereum/crypto"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func signedPlaysTx(t *testing.T, sign bool) (*corev1.SignedTransaction, string) {
	t.Helper()
	plays := &corev1.TrackPlays{Plays: []*corev1.TrackPlay{
		{UserId: "1", TrackId: "2", Timestamp: timestamppb.Now()},
	}}
	stx := &corev1.SignedTransaction{Transaction: &corev1.SignedTransaction_Plays{Plays: plays}}
	if !sign {
		return stx, ""
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := signature.SignCoreBytes(plays, key)
	if err != nil {
		t.Fatal(err)
	}
	stx.Signature = sig
	return stx, strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
}

func knownSet(addrs ...string) knownNodeLookup {
	return func(_ context.Context, address string) (bool, error) {
		for _, a := range addrs {
			if a == address {
				return true, nil
			}
		}
		return false, nil
	}
}

func TestPlaysRejection(t *testing.T) {
	ctx := context.Background()
	e := New(nil, zap.NewNop())

	signed, signer := signedPlaysTx(t, true)
	unsigned, _ := signedPlaysTx(t, false)
	garbage, _ := signedPlaysTx(t, false)
	garbage.Signature = strings.Repeat("ab", 65)

	cases := []struct {
		name   string
		stx    *corev1.SignedTransaction
		known  knownNodeLookup
		reject bool
	}{
		{"registered signer", signed, knownSet(signer), false},
		{"unregistered signer", signed, knownSet("0x0000000000000000000000000000000000000001"), true},
		{"empty signature", unsigned, knownSet(signer), true},
		{"garbage signature", garbage, knownSet(signer), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, err := e.playsRejection(ctx, tc.stx, 100, tc.known)
			if err != nil {
				t.Fatal(err)
			}
			if (reason != "") != tc.reject {
				t.Errorf("reason = %q, want reject=%v", reason, tc.reject)
			}
		})
	}
}

func TestPlaysRejection_GenesisMigrationRange(t *testing.T) {
	ctx := context.Background()
	e := New(nil, zap.NewNop())
	e.config.GenesisMigrationEndHeight = 50
	unsigned, _ := signedPlaysTx(t, false)
	none := knownSet()

	for _, h := range []int64{1, 50} {
		if reason, err := e.playsRejection(ctx, unsigned, h, none); err != nil || reason != "" {
			t.Errorf("height %d: reason = %q, err = %v; want accepted", h, reason, err)
		}
	}
	if reason, _ := e.playsRejection(ctx, unsigned, 51, none); reason == "" {
		t.Error("height 51: unsigned plays accepted past the migration range")
	}
}

func TestPlaysRejection_Disabled(t *testing.T) {
	e := New(nil, zap.NewNop())
	e.config.VerifyPlaySigners = false
	unsigned, _ := signedPlaysTx(t, false)
	if reason, err := e.playsRejection(context.Background(), unsigned, 100, knownSet()); err != nil || reason != "" {
		t.Errorf("reason = %q, err = %v; want accepted", reason, err)
	}
}

func TestPlaysRejection_LookupError(t *testing.T) {
	e := New(nil, zap.NewNop())
	signed, _ := signedPlaysTx(t, true)
	failing := func(context.Context, string) (bool, error) { return false, errors.New("db down") }
	if _, err := e.playsRejection(context.Background(), signed, 100, failing); err == nil {
		t.Error("expected lookup error to propagate")
	}
}

func TestReadPlaySignerEnv(t *testing.T) {
	t.Setenv("OPENAUDIO_ETL_VERIFY_PLAY_SIGNERS", "false")
	t.Setenv("OPENAUDIO_ETL_GENESIS_MIGRATION_END_HEIGHT", "1234")
	c := DefaultConfig()
	if !c.VerifyPlaySigners {
		t.Fatal("VerifyPlaySigners should default to true")
	}
	if err := c.ReadPlaySignerEnv(); err != nil {
		t.Fatal(err)
	}
	if c.VerifyPlaySigners || c.GenesisMigrationEndHeight != 1234 {
		t.Errorf("config = %+v", c)
	}

	t.Setenv("OPENAUDIO_ETL_GENESIS_MIGRATION_END_HEIGHT", "nope")
	if err := c.ReadPlaySignerEnv(); err == nil {
		t.Error("expected error for invalid height")
	}
}
