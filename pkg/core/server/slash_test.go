package server

import (
	"context"
	"encoding/hex"
	"math"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// testChainID is the chain every fixture in this package signs under.
const testChainID = "test"

// The exact dual-parse body from the local-devnet report. Fields 6-9 carry
// the Comet key, voting power and deadline through SlashRecommendation as
// unknown fields; fields 2-3 parse as both timestamps and control characters.
const slashRegistrationReproHex = "0a2a307831633866374631393846306442303835654633654533373534433846376432363538303432334439120208011a0208023228373144353536304642303741343034314636334638433641453637334344464637374231423835343a20aa892a1e8a15bb8679b6e9fa5e63e449b314e52771038697c1bd79f80875d690401948a506"

func validSlashRecommendation() *v1.SlashRecommendation {
	return &v1.SlashRecommendation{
		Address:    "0x1c8f7F198F0dB085eF3eE3754C8F7d26580423D9",
		Start:      timestamppb.New(time.Unix(0, 0)),
		End:        timestamppb.New(time.Unix(365*24*60*60, 0)),
		MissedSLAs: 5,
		Amount:     100000,
	}
}

func TestSlashRegistrationReproBytes(t *testing.T) {
	body, err := hex.DecodeString(slashRegistrationReproHex)
	require.NoError(t, err)
	var reg v1.ValidatorRegistration
	var slash v1.SlashRecommendation
	require.NoError(t, proto.Unmarshal(body, &reg))
	require.NoError(t, proto.Unmarshal(body, &slash))
	roundTrip, err := proto.Marshal(&slash)
	require.NoError(t, err)
	require.Equal(t, body, roundTrip, "the reported byte collision must still be reproduced")
	require.Equal(t, "\x08\x01", reg.Endpoint)
	require.NotEmpty(t, slash.ProtoReflect().GetUnknown())

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	legacySig, err := common.EthSign(key, roundTrip)
	require.NoError(t, err)
	_, signer, err := common.EthRecover(legacySig, body)
	require.NoError(t, err)
	require.Equal(t, crypto.PubkeyToAddress(key.PublicKey).Hex(), signer)

	sig, err := SignSlashRecommendation(key, testChainID, &slash)
	require.ErrorContains(t, err, "unknown fields")
	require.Empty(t, sig)
	// Rejection must happen before touching Ethereum or the database.
	sig, err = (&Server{}).getSlashAttestation(context.Background(), &slash)
	require.ErrorContains(t, err, "unknown fields")
	require.Empty(t, sig)
}

func TestSlashRecommendationValidation(t *testing.T) {
	unknown := []byte{0x30, 0x01}
	tests := map[string]func(*v1.SlashRecommendation){
		"unknown body":      func(s *v1.SlashRecommendation) { s.ProtoReflect().SetUnknown(unknown) },
		"unknown start":     func(s *v1.SlashRecommendation) { s.Start.ProtoReflect().SetUnknown(unknown) },
		"unknown end":       func(s *v1.SlashRecommendation) { s.End.ProtoReflect().SetUnknown(unknown) },
		"missing start":     func(s *v1.SlashRecommendation) { s.Start = nil },
		"missing end":       func(s *v1.SlashRecommendation) { s.End = nil },
		"bad seconds":       func(s *v1.SlashRecommendation) { s.End.Seconds = math.MaxInt64 },
		"bad nanos":         func(s *v1.SlashRecommendation) { s.Start.Nanos = -1 },
		"equal interval":    func(s *v1.SlashRecommendation) { s.End = s.Start },
		"reversed interval": func(s *v1.SlashRecommendation) { s.Start, s.End = s.End, s.Start },
		"bad address":       func(s *v1.SlashRecommendation) { s.Address = "not-an-address" },
		"negative amount":   func(s *v1.SlashRecommendation) { s.Amount = -1 },
		"negative missed":   func(s *v1.SlashRecommendation) { s.MissedSLAs = -1 },
	}
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			slash := validSlashRecommendation()
			mutate(slash)
			sig, err := SignSlashRecommendation(key, testChainID, slash)
			require.Error(t, err)
			require.Empty(t, sig)
			_, err = (&Server{}).getSlashAttestation(context.Background(), slash)
			require.Error(t, err)
			_, err = (&Server{}).gatherSlashAttestations(context.Background(), slash)
			require.Error(t, err)
		})
	}
	_, err = SignSlashRecommendation(key, testChainID, nil)
	require.Error(t, err)
	_, err = SignSlashRecommendation(nil, testChainID, validSlashRecommendation())
	require.Error(t, err)
	core := &CoreService{core: &Server{}}
	_, err = core.GetSlashAttestations(context.Background(), connect.NewRequest(&v1.GetSlashAttestationsRequest{}))
	require.Error(t, err)
}

func TestSlashSignatureDomain(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	slash := validSlashRecommendation()
	sig, err := SignSlashRecommendation(key, testChainID, slash)
	require.NoError(t, err)
	payload, err := SlashRecommendationSignBytes(testChainID, slash)
	require.NoError(t, err)
	_, recovered, err := common.EthRecover(sig, payload)
	require.NoError(t, err)
	address := crypto.PubkeyToAddress(key.PublicKey).Hex()
	require.Equal(t, address, recovered)
	raw, err := proto.Marshal(slash)
	require.NoError(t, err)
	_, recovered, err = common.EthRecover(sig, raw)
	require.NoError(t, err)
	require.NotEqual(t, address, recovered)
	require.Error(t, proto.Unmarshal(payload, &v1.ValidatorRegistration{}))

	// A proof gathered on one network must not be evidence on another, and an
	// unknown chain must refuse to sign rather than fall back to no domain.
	other, err := SlashRecommendationSignBytes("other-chain", slash)
	require.NoError(t, err)
	require.NotEqual(t, payload, other)
	_, recovered, err = common.EthRecover(sig, other)
	require.NoError(t, err)
	require.NotEqual(t, address, recovered)
	_, err = SlashRecommendationSignBytes("", slash)
	require.ErrorContains(t, err, "chain ID")
	_, err = SignSlashRecommendation(key, "", slash)
	require.ErrorContains(t, err, "chain ID")
	// Rejection must happen before the fan-out to peers.
	_, err = (&Server{}).gatherSlashAttestations(context.Background(), slash)
	require.ErrorContains(t, err, "chain ID")

	slash.Amount++
	changed, err := SlashRecommendationSignBytes(testChainID, slash)
	require.NoError(t, err)
	_, recovered, err = common.EthRecover(sig, changed)
	require.NoError(t, err)
	require.NotEqual(t, address, recovered)
}

func TestSlashEvidence(t *testing.T) {
	slash := validSlashRecommendation()
	require.NoError(t, validateSlashEvidence(slash, 1, 10, 5))
	require.Error(t, validateSlashEvidence(slash, 0, 10, 5))
	require.Error(t, validateSlashEvidence(slash, 1, 0, 0))
	// Both round to zero, so comparing just Amount would miss this lie.
	slash.Amount = 0
	slash.End = timestamppb.New(slash.Start.AsTime().Add(time.Second))
	require.ErrorContains(t, validateSlashEvidence(slash, 1, 10, 4), "counts")
	require.NoError(t, validateSlashEvidence(slash, 1, 10, 5))
	slash.Amount = 1
	require.ErrorContains(t, validateSlashEvidence(slash, 1, 10, 5), "amounts")
}

func TestCalculateSlashRecommendation(t *testing.T) {
	start := time.Unix(0, 0)
	year := start.Add(365 * 24 * time.Hour)
	tests := []struct {
		name                     string
		end                      time.Time
		endpoints, total, missed int
		want                     int64
	}{
		{"full year", year, 1, 10, 10, 200000},
		{"half missed two endpoints", year, 2, 10, 5, 200000},
		{"none missed", year, 1, 10, 0, 0},
		{"no endpoints", year, 0, 10, 5, 0},
		{"no evidence", year, 1, 0, 0, 0},
		{"negative total", year, 1, -1, 0, 0},
		{"negative missed", year, 1, 10, -1, 0},
		{"excess missed", year, 1, 10, 11, 0},
		{"reversed", start.Add(-time.Hour), 1, 10, 5, 0},
		{"empty interval", start, 1, 10, 5, 0},
		{"partial day", start.Add(time.Hour), 1, 10, 5, 0},
		{"saturates overflow", year, math.MaxInt, 10, 10, math.MaxInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, CalculateSlashRecommendation(start, tt.end, tt.endpoints, tt.total, tt.missed))
		})
	}
}

func FuzzSlashRecommendationSignBytes(f *testing.F) {
	seed, _ := proto.Marshal(validSlashRecommendation())
	f.Add(seed)
	repro, _ := hex.DecodeString(slashRegistrationReproHex)
	f.Add(repro)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		var slash v1.SlashRecommendation
		if proto.Unmarshal(raw, &slash) != nil {
			return
		}
		payload, err := SlashRecommendationSignBytes(testChainID, &slash)
		if err != nil {
			return
		}
		require.Empty(t, slash.ProtoReflect().GetUnknown())
		require.Empty(t, slash.Start.ProtoReflect().GetUnknown())
		require.Empty(t, slash.End.ProtoReflect().GetUnknown())
		require.Error(t, proto.Unmarshal(payload, &v1.ValidatorRegistration{}))
		again, err := SlashRecommendationSignBytes(testChainID, &slash)
		require.NoError(t, err)
		require.Equal(t, payload, again)
	})
}
