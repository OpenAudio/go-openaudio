package server

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	coreServer "github.com/OpenAudio/go-openaudio/pkg/core/server"
	"github.com/OpenAudio/go-openaudio/pkg/lifecycle"
	"github.com/OpenAudio/go-openaudio/pkg/pos"
	"github.com/OpenAudio/go-openaudio/pkg/registrar"
	"github.com/OpenAudio/go-openaudio/pkg/version"
	"github.com/ethereum/go-ethereum/crypto"
	"go.uber.org/zap"
)

var testNetwork []*MediorumServer

// Generate deterministic test private keys
func generateTestPrivateKey(index int) *ecdsa.PrivateKey {
	// Use a deterministic seed based on index for reproducible test keys
	seed := fmt.Sprintf("test-private-key-%d-seed-for-mediorum-testing", index)
	hash := crypto.Keccak256Hash([]byte(seed))
	key, err := crypto.ToECDSA(hash.Bytes())
	if err != nil {
		panic(fmt.Sprintf("failed to generate test private key: %v", err))
	}
	return key
}

func setupTestNetwork(replicationFactor, serverCount int) []*MediorumServer {

	testBaseDir := "/tmp/mediorum_test"
	os.RemoveAll(testBaseDir)

	network := []registrar.Peer{}
	servers := []*MediorumServer{}

	dbUrlTemplate := os.Getenv("dbUrlTemplate")
	if dbUrlTemplate == "" {
		dbUrlTemplate = "postgres://postgres:example@localhost:5454/m%d"
	}

	for i := 1; i <= serverCount; i++ {
		privateKey := generateTestPrivateKey(i)
		wallet := crypto.PubkeyToAddress(privateKey.PublicKey).Hex()

		network = append(network, registrar.Peer{
			Host:   fmt.Sprintf("http://127.0.0.1:%d", 1980+i),
			Wallet: wallet,
		})
	}

	z, _ := zap.NewDevelopment()
	lc := lifecycle.NewLifecycle(context.Background(), "mediorum test lifecycle", z)

	for idx, peer := range network {
		peer := peer
		privateKey := generateTestPrivateKey(idx + 1)
		privateKeyHex := fmt.Sprintf("%x", crypto.FromECDSA(privateKey))

		// Create a logger with host field for this server
		serverLogger := z.With(zap.String("host", peer.Host))

		config := MediorumConfig{
			Env:               "test",
			Self:              peer,
			Peers:             network,
			ReplicationFactor: replicationFactor,
			Dir:               fmt.Sprintf("%s/%s", testBaseDir, peer.Wallet),
			PostgresDSN:       fmt.Sprintf(dbUrlTemplate, idx+1),
			PrivateKey:        privateKeyHex,
			VersionJson: version.VersionJson{
				Version: "0.0.0",
				Service: "content-node",
			},
		}
		posChannel := make(chan pos.PoSRequest)
		server, err := New(lc, serverLogger, config, posChannel, &coreServer.CoreService{}, nil)
		if err != nil {
			panic(err)
		}
		servers = append(servers, server)

		go func() {
			server.MustStart()
		}()
	}

	// give each server time to startup + health check
	time.Sleep(time.Second)
	log.Printf("started %d servers", serverCount)

	return servers

}

func TestMain(m *testing.M) {
	testNetwork = setupTestNetwork(5, 9)

	exitVal := m.Run()
	// todo: tear down testNetwork

	os.Exit(exitVal)
}
