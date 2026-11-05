package processors

import (
	"errors"
	"strings"
	"testing"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/server/signature"
	"github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func testPlays() *corev1.TrackPlays {
	return &corev1.TrackPlays{Plays: []*corev1.TrackPlay{
		{UserId: "1", TrackId: "2", Timestamp: timestamppb.Now(), City: "Austin"},
		{UserId: "3", TrackId: "4", Timestamp: timestamppb.Now()},
	}}
}

func TestRecoverPlaysSigner(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	plays := testPlays()
	sig, err := signature.SignCoreBytes(plays, key)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("mediorum signature", func(t *testing.T) {
		got, err := RecoverPlaysSigner(sig, plays)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("signer = %s, want %s", got, want)
		}
	})

	t.Run("0x prefix", func(t *testing.T) {
		got, err := RecoverPlaysSigner("0x"+sig, plays)
		if err != nil || got != want {
			t.Errorf("signer = %s, %v; want %s", got, err, want)
		}
	})

	t.Run("empty", func(t *testing.T) {
		if _, err := RecoverPlaysSigner("", plays); !errors.Is(err, ErrPlaysUnsigned) {
			t.Errorf("err = %v, want ErrPlaysUnsigned", err)
		}
	})

	t.Run("not hex", func(t *testing.T) {
		if _, err := RecoverPlaysSigner("zz", plays); err == nil {
			t.Error("expected error")
		}
	})

	t.Run("wrong length", func(t *testing.T) {
		if _, err := RecoverPlaysSigner(sig[:20], plays); err == nil {
			t.Error("expected error")
		}
	})

	t.Run("signature over other plays", func(t *testing.T) {
		other := testPlays()
		other.Plays[0].TrackId = "999"
		got, err := RecoverPlaysSigner(sig, other)
		if err == nil && got == want {
			t.Error("signature recovered to the signer for different plays")
		}
	})
}
