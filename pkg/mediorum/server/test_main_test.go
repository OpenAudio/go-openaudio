package server

import (
	"context"
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
	"go.uber.org/zap"
)

var testNetwork []*MediorumServer

func setupTestNetwork(replicationFactor, serverCount int) []*MediorumServer {

	testBaseDir := "/tmp/mediorum_test"
	os.RemoveAll(testBaseDir)

	network := []registrar.Peer{}
	servers := []*MediorumServer{}
	provider := registrar.NewMultiDev()

	dbUrlTemplate := os.Getenv("dbUrlTemplate")
	if dbUrlTemplate == "" {
		dbUrlTemplate = "postgres://postgres:example@localhost:5454/m%d"
	}

	for i := 1; i <= serverCount; i++ {
		network = append(network, registrar.Peer{
			Host:   fmt.Sprintf("http://127.0.0.1:%d", 1980+i),
			Wallet: fmt.Sprintf("0xWallet%d", i), // todo keypair stuff
		})
	}

	z := zap.NewNop()
	lc := lifecycle.NewLifecycle(context.Background(), "mediorum test lifecycle", z)

	for idx, peer := range network {
		peer := peer
		config := MediorumConfig{
			Env:               "test",
			Self:              peer,
			Peers:             network,
			ReplicationFactor: replicationFactor,
			Dir:               fmt.Sprintf("%s/%s", testBaseDir, peer.Wallet),
			PostgresDSN:       fmt.Sprintf(dbUrlTemplate, idx+1),
			VersionJson: version.VersionJson{
				Version: "0.0.0",
				Service: "content-node",
			},
		}
		posChannel := make(chan pos.PoSRequest)
		server, err := New(lc, z, config, provider, posChannel, &coreServer.CoreService{})
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
