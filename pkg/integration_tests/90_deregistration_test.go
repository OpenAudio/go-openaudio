package integration_tests

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	ethv1 "github.com/OpenAudio/go-openaudio/pkg/api/eth/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/OpenAudio/go-openaudio/pkg/eth/contracts"
	"github.com/OpenAudio/go-openaudio/pkg/integration_tests/utils"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
)

const (
	contentThreeKey     = "1166189cdf129cdcb011f2ad0e5be24f967f7b7026d162d7c36073b12020b61c"
	contentThreeAddress = "0x1B569e8f1246907518Ff3386D523dcF373e769B6"
	contentThreeEp      = "https://node4.oap.devnet"
)

type CometRPCResponse struct {
	Result struct {
		ValidatorInfo struct {
			VotingPower int32 `json:"voting_power"`
		} `json:"validator_info"`
	} `json:"result"`
}

func TestDeregisterNode(t *testing.T) {
	ctx := context.Background()

	wsRpcUrl := config.DevEthRpc
	if strings.HasPrefix(wsRpcUrl, "https") {
		wsRpcUrl = "wss" + strings.TrimPrefix(wsRpcUrl, "https")
	} else if strings.HasPrefix(wsRpcUrl, "http:") {
		wsRpcUrl = "ws" + strings.TrimPrefix(wsRpcUrl, "http")
	}

	err := utils.WaitForDevnetHealthy(30 * time.Second)
	require.NoError(t, err)

	ethrpc, err := ethclient.Dial(wsRpcUrl)
	require.NoError(t, err, "eth client dial err")
	defer ethrpc.Close()

	// Init contracts
	c, err := contracts.NewAudiusContracts(ethrpc, config.DevRegistryAddress)
	require.NoError(t, err, "failed to initialize eth contracts")

	serviceProviderFactoryContract, err := c.GetServiceProviderFactoryContract()
	require.NoError(t, err, "failed to get service provider factory contract")

	chainID, err := ethrpc.ChainID(ctx)
	require.NoError(t, err, "failed to get chainID")

	ethKey, err := common.EthToEthKey(contentThreeKey)
	require.NoError(t, err, "failed to create ethereum key")

	opts, err := bind.NewKeyedTransactorWithChainID(ethKey, chainID)
	require.NoError(t, err, "failed to create keyed transactor")

	_, err = serviceProviderFactoryContract.Deregister(opts, contracts.ContentNode, contentThreeEp)
	require.NoError(t, err, "failed to deregister node4")

	time.Sleep(1 * time.Second)

	epResp, err := utils.ContentTwo.Eth.GetRegisteredEndpoints(ctx, connect.NewRequest(&ethv1.GetRegisteredEndpointsRequest{}))
	require.NoError(t, err, "failed to get registered endpoints from node3 eth service")
	require.Equal(t, 3, len(epResp.Msg.Endpoints), "unexpected number of endpoints returned by node3 eth service", epResp.Msg.Endpoints)

	for _, ep := range epResp.Msg.Endpoints {
		require.NotEqual(t, contentThreeEp, ep.Endpoint, "node4 should not be in returned endpoints")
	}

	timeout := time.After(30 * time.Second)
	for {
		select {
		case <-timeout:
			require.Fail(t, "timed out waiting for node4 comet rpc to deregister", err)
		default:
		}
		cometRpcResp, err := http.Get(contentThreeEp + "/core/crpc/status")
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		defer cometRpcResp.Body.Close()

		body, err := io.ReadAll(cometRpcResp.Body)
		require.NoError(t, err, "failed to read comet rpc response body")

		var r CometRPCResponse
		err = json.Unmarshal(body, &r)
		require.NoError(t, err, "failed to marshall comet rpc response body")
		if r.Result.ValidatorInfo.VotingPower != 0 {
			err = errors.New("Voting power is still non-zero")
			time.Sleep(2 * time.Second)
			continue
		}

		require.Equal(t, int32(0), r.Result.ValidatorInfo.VotingPower)

		break
	}
}
