// loadgen fires a batch of entity-manager transactions at a local devnet node
// to exercise the indexer (users, tracks, follows, subscribes, reposts, saves,
// comments, user updates). Each user is minted with its own ECDSA key so the
// "wallet already in use" / signer-authorization checks pass.
//
//	make up                       # start the devnet
//	go run ./examples/loadgen --rpc https://node1.oap.devnet --insecure --users 5 --social 200
//
// Then watch your ETL index the resulting blocks.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/OpenAudio/go-openaudio/pkg/core/server"
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
)

const (
	userIDOffset  = 3_000_000
	trackIDOffset = 2_000_000
)

type user struct {
	id  int64
	key *ecdsa.PrivateKey
}

func main() {
	rpcURL := flag.String("rpc", "https://node1.oap.devnet", "Core RPC endpoint")
	insecure := flag.Bool("insecure", true, "Skip TLS verification (self-signed devnet cert)")
	numUsers := flag.Int("users", 5, "How many users to create")
	numTracks := flag.Int("tracks", 3, "How many tracks to create (spread across users)")
	numSocial := flag.Int("social", 100, "How many random social actions to fire")
	pause := flag.Duration("pause", 1500*time.Millisecond, "Pause after each dependency stage to let blocks commit")
	flag.Parse()

	ctx := context.Background()

	httpClient := http.DefaultClient
	if *insecure {
		httpClient = &http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
			Timeout:   30 * time.Second,
		}
	}
	auds := sdk.NewOpenAudioSDKWithClient(*rpcURL, httpClient)
	if err := auds.Init(ctx); err != nil {
		log.Fatalf("init SDK: %v", err)
	}
	cfg := &config.Config{
		AcdcEntityManagerAddress: config.DevAcdcAddress,
		AcdcChainID:              config.DevAcdcChainID,
	}

	// Unique per-run base so reruns don't collide on user/track ids or handles.
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	base := int64(rng.Intn(900_000)) * 100

	send := newSender(ctx, auds, cfg)

	// 1. Users — each with its own wallet.
	users := make([]user, 0, *numUsers)
	for i := 0; i < *numUsers; i++ {
		key, _ := crypto.GenerateKey()
		id := userIDOffset + base + int64(i)
		meta := fmt.Sprintf(`{"handle":"lg_%d_%d","name":"Loadgen User %d"}`, base, i, i)
		if send("User", "Create", id, id, meta, key) {
			users = append(users, user{id: id, key: key})
		}
	}
	if len(users) == 0 {
		log.Fatal("no users created; aborting")
	}
	log.Printf("created %d users; waiting for commit", len(users))
	time.Sleep(*pause)

	// 2. Tracks — owned by random users.
	tracks := make([]int64, 0, *numTracks)
	for j := 0; j < *numTracks; j++ {
		owner := users[rng.Intn(len(users))]
		id := trackIDOffset + base + int64(j)
		meta := fmt.Sprintf(`{"title":"Loadgen Track %d","genre":"Electronic","cid":"Qm-loadgen-%d-%d"}`, j, base, j)
		if send("Track", "Create", owner.id, id, meta, owner.key) {
			tracks = append(tracks, id)
		}
	}
	log.Printf("created %d tracks; waiting for commit", len(tracks))
	time.Sleep(*pause)

	// 3. Random social actions among the created users/tracks.
	sent, ok := 0, 0
	for i := 0; i < *numSocial; i++ {
		actor := users[rng.Intn(len(users))]
		var entityType, action string
		var entityID int64
		switch rng.Intn(6) {
		case 0, 1: // follow / subscribe another user
			target := users[rng.Intn(len(users))]
			if target.id == actor.id {
				continue
			}
			entityType, entityID = "User", target.id
			action = []string{"Follow", "Subscribe"}[rng.Intn(2)]
		case 2: // repost a track
			if len(tracks) == 0 {
				continue
			}
			entityType, entityID, action = "Track", tracks[rng.Intn(len(tracks))], "Repost"
		case 3: // save a track
			if len(tracks) == 0 {
				continue
			}
			entityType, entityID, action = "Track", tracks[rng.Intn(len(tracks))], "Save"
		case 4: // comment on a track
			if len(tracks) == 0 {
				continue
			}
			cid := int64(4_000_000) + base + int64(i)
			meta := fmt.Sprintf(`{"body":"loadgen comment %d","entity_id":%d,"entity_type":"Track"}`, i, tracks[rng.Intn(len(tracks))])
			sent++
			if send("Comment", "Create", actor.id, cid, meta, actor.key) {
				ok++
			}
			continue
		default: // update profile
			entityType, entityID, action = "User", actor.id, "Update"
			meta := fmt.Sprintf(`{"name":"Loadgen User %d v%d"}`, actor.id, i)
			sent++
			if send(entityType, action, actor.id, entityID, meta, actor.key) {
				ok++
			}
			continue
		}
		sent++
		if send(entityType, action, actor.id, entityID, "{}", actor.key) {
			ok++
		}
	}
	log.Printf("done: %d/%d social actions accepted (rejections are expected for dup follows/saves)", ok, sent)
}

// newSender returns a closure that signs and submits one ManageEntity tx,
// logging the outcome and returning whether it was accepted.
func newSender(ctx context.Context, auds *sdk.OpenAudioSDK, cfg *config.Config) func(entityType, action string, userID, entityID int64, metadata string, key *ecdsa.PrivateKey) bool {
	var nonce int64
	return func(entityType, action string, userID, entityID int64, metadata string, key *ecdsa.PrivateKey) bool {
		nonce++
		em := &corev1.ManageEntityLegacy{
			UserId:     userID,
			EntityType: entityType,
			EntityId:   entityID,
			Action:     action,
			Metadata:   metadata,
			Nonce:      fmt.Sprintf("0x%064x", time.Now().UnixNano()+nonce),
		}
		if err := server.SignManageEntity(cfg, em, key); err != nil {
			log.Printf("  sign %s/%s e%d: %v", entityType, action, entityID, err)
			return false
		}
		_, err := auds.Core.SendTransaction(ctx, connect.NewRequest(&corev1.SendTransactionRequest{
			Transaction: &corev1.SignedTransaction{
				RequestId:   uuid.NewString(),
				Transaction: &corev1.SignedTransaction_ManageEntity{ManageEntity: em},
			},
		}))
		if err != nil {
			log.Printf("  send %s/%s e%d: %v", entityType, action, entityID, err)
			return false
		}
		log.Printf("  %s/%s user=%d entity=%d", entityType, action, userID, entityID)
		return true
	}
}
