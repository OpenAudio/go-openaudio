package server

import (
	"context"
	"crypto/ecdsa"
	"os"
	"testing"
	"time"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/server/signature"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type playTestFixture struct {
	s    *Server
	node *ecdsa.PrivateKey
}

func newPlayTestFixture(t *testing.T) playTestFixture {
	t.Helper()
	pool := setupValidatorTestDB(t)
	truncateValidators(t, pool)

	q := db.New(pool)
	key, err := gethcrypto.GenerateKey()
	require.NoError(t, err)
	require.NoError(t, q.InsertRegisteredNode(context.Background(), db.InsertRegisteredNodeParams{
		PubKey:       "plays-pubkey",
		Endpoint:     "https://node.example.com",
		EthAddress:   gethcrypto.PubkeyToAddress(key.PublicKey).Hex(),
		CometAddress: "PLAYSCOMETADDRESS",
		CometPubKey:  "plays-comet-pubkey",
		EthBlock:     "100",
		NodeType:     "validator",
		SpID:         "1",
	}))

	return playTestFixture{
		s:    &Server{db: q, logger: zap.NewNop(), config: &config.Config{}},
		node: key,
	}
}

func testPlays(n int) *v1.TrackPlays {
	plays := make([]*v1.TrackPlay, n)
	for i := range plays {
		plays[i] = &v1.TrackPlay{
			UserId:    "1",
			TrackId:   "2",
			Timestamp: timestamppb.New(time.Unix(1_700_000_000, 0)),
			Signature: "0xlisten",
			City:      "Brooklyn",
			Region:    "NY",
			Country:   "US",
		}
	}
	return &v1.TrackPlays{Plays: plays}
}

func signPlaysForTest(t *testing.T, plays *v1.TrackPlays, key *ecdsa.PrivateKey) *v1.SignedTransaction {
	t.Helper()
	sig, err := signature.SignCoreBytes(plays, key)
	require.NoError(t, err)
	return &v1.SignedTransaction{
		Signature:   sig,
		Transaction: &v1.SignedTransaction_Plays{Plays: plays},
	}
}

func playBlockValid(t *testing.T, s *Server, rules config.Rules, stx *v1.SignedTransaction) (bool, error) {
	t.Helper()
	txBytes, err := proto.Marshal(stx)
	require.NoError(t, err)
	return s.validateBlockTx(context.Background(), rules, newOverlayAuthStore(newMemAuthStore()), time.Time{}, 100, nil, txBytes)
}

func TestIsValidPlayTransaction(t *testing.T) {
	f := newPlayTestFixture(t)
	ctx := context.Background()

	require.NoError(t, f.s.isValidPlayTransaction(ctx, signPlaysForTest(t, testPlays(1), f.node)))
	require.NoError(t, f.s.isValidPlayTransaction(ctx, signPlaysForTest(t, testPlays(MaxPlaysPerTransaction), f.node)))

	stranger, err := gethcrypto.GenerateKey()
	require.NoError(t, err)

	unsignedPlay := testPlays(3)
	unsignedPlay.Plays[1].Signature = ""

	tampered := signPlaysForTest(t, testPlays(1), f.node)
	tampered.GetPlays().Plays[0].TrackId = "3"

	cases := map[string]*v1.SignedTransaction{
		"empty envelope signature":   {Transaction: &v1.SignedTransaction_Plays{Plays: testPlays(1)}},
		"non-hex signature":          {Signature: "zz", Transaction: &v1.SignedTransaction_Plays{Plays: testPlays(1)}},
		"short signature":            {Signature: "abcd", Transaction: &v1.SignedTransaction_Plays{Plays: testPlays(1)}},
		"unregistered signer":        signPlaysForTest(t, testPlays(1), stranger),
		"body altered after signing": tampered,
		"too many plays":             signPlaysForTest(t, testPlays(MaxPlaysPerTransaction+1), f.node),
		"no plays":                   signPlaysForTest(t, testPlays(0), f.node),
		"play without signature":     signPlaysForTest(t, unsignedPlay, f.node),
	}
	for name, stx := range cases {
		t.Run(name, func(t *testing.T) {
			err := f.s.isValidPlayTransaction(ctx, stx)
			require.Error(t, err)
			require.True(t, authRejected(err), "want a deterministic rejection, got %v", err)

			require.Error(t, f.s.validateV1Transaction(ctx, 100, stx), "mempool must refuse regardless of activation height")

			valid, err := playBlockValid(t, f.s, config.Rules{PlayAuthEnforced: true}, stx)
			require.NoError(t, err)
			require.False(t, valid, "proposal must be rejected once play auth is enforced")

			valid, err = playBlockValid(t, f.s, config.Rules{}, stx)
			require.NoError(t, err)
			require.True(t, valid, "proposal must stay valid before the activation height")
		})
	}
}

func TestPlayBlockValidityAcceptsSignedPlays(t *testing.T) {
	f := newPlayTestFixture(t)
	valid, err := playBlockValid(t, f.s, config.Rules{PlayAuthEnforced: true}, signPlaysForTest(t, testPlays(3), f.node))
	require.NoError(t, err)
	require.True(t, valid)
}

func TestPlayBlockValiditySurfacesStoreErrors(t *testing.T) {
	f := newPlayTestFixture(t)
	closed, err := pgxpool.New(context.Background(), os.Getenv("TEST_DB_URL"))
	require.NoError(t, err)
	closed.Close()
	f.s.db = db.New(closed)

	stx := signPlaysForTest(t, testPlays(1), f.node)
	valid, err := playBlockValid(t, f.s, config.Rules{PlayAuthEnforced: true}, stx)
	require.Error(t, err, "a store failure must report unknown, not vote the proposal invalid")
	require.False(t, valid)
}

func TestPlayCheckTxShape(t *testing.T) {
	key, err := gethcrypto.GenerateKey()
	require.NoError(t, err)

	require.NoError(t, validateSignedTransactionForCheckTx(signPlaysForTest(t, testPlays(1), key)))
	require.Error(t, validateSignedTransactionForCheckTx(&v1.SignedTransaction{
		Transaction: &v1.SignedTransaction_Plays{Plays: testPlays(1)},
	}))
	require.Error(t, validateSignedTransactionForCheckTx(signPlaysForTest(t, testPlays(MaxPlaysPerTransaction+1), key)))
}

func TestFinalizePlayTransactionAcceptsUnsignedPlays(t *testing.T) {
	s := &Server{logger: zap.NewNop()}
	plays := testPlays(20)
	for _, p := range plays.Plays {
		p.Signature = ""
	}
	got, err := s.finalizePlayTransaction(context.Background(), &v1.SignedTransaction{
		Transaction: &v1.SignedTransaction_Plays{Plays: plays},
	})
	require.NoError(t, err, "genesis-writer replay plays are unsigned and must still finalize")
	require.Len(t, got.Plays, 20)
}
