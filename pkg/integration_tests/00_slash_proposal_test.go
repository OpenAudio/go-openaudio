package integration_tests

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	ethv1 "github.com/AudiusProject/audiusd/pkg/api/eth/v1"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/core/config"
	"github.com/AudiusProject/audiusd/pkg/eth"
	"github.com/AudiusProject/audiusd/pkg/eth/contracts"
	"github.com/AudiusProject/audiusd/pkg/eth/contracts/gen"
	"github.com/AudiusProject/audiusd/pkg/integration_tests/utils"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
)

const (
	contentTwoKey = "1aa14c63d481dcc1185a654eb52c9c0749d07ac8f30ef17d45c3c391d9bf68eb"
)

func TestCanRetrieveSlashProposal(t *testing.T) {
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

	governanceContract, err := c.GetGovernanceContract()
	require.NoError(t, err, "failed to get staking contract")

	chainID, err := ethrpc.ChainID(ctx)
	require.NoError(t, err, "failed to get chainID")

	ethKey, err := common.EthToEthKey(contentTwoKey)
	require.NoError(t, err, "failed to create ethereum key")

	opts, err := bind.NewKeyedTransactorWithChainID(ethKey, chainID)
	require.NoError(t, err, "failed to create keyed transactor")

	delegateManagerABI, err := gen.DelegateManagerMetaData.GetAbi()
	require.NoError(t, err, "failed to get governance abi")

	slashMethod, ok := delegateManagerABI.Methods["slash"]
	require.True(t, ok, "could not retrieve slash method from DelegateManager abi")

	contentThreeAddr := ethcommon.HexToAddress(contentThreeAddress)

	callData1, err := slashMethod.Inputs.Pack(contracts.AudioToWei(big.NewInt(10)), contentThreeAddr)
	require.NoError(t, err, "failed to pack callData1")
	callData2, err := slashMethod.Inputs.Pack(contracts.AudioToWei(big.NewInt(100000)), contentThreeAddr)
	require.NoError(t, err, "failed to pack callData2")
	callData3, err := slashMethod.Inputs.Pack(contracts.AudioToWei(big.NewInt(5)), contentThreeAddr)
	require.NoError(t, err, "failed to pack callData3")

	_, err = governanceContract.SubmitProposal(
		opts,
		contracts.DelegateManagerKey,
		big.NewInt(0),
		"slash(uint256,address)",
		callData1,
		"Test Slash Proposal 1",
		"Integration test for slash proposal 1",
	)
	require.NoError(t, err, "failed to create slash proposal 1")

	_, err = governanceContract.SubmitProposal(
		opts,
		contracts.DelegateManagerKey,
		big.NewInt(0),
		"slash(uint256,address)",
		callData2,
		"Test Slash Proposal 2",
		"Integration test for slash proposal 2",
	)
	require.NoError(t, err, "failed to create slash proposal 2")

	_, err = governanceContract.SubmitProposal(
		opts,
		contracts.DelegateManagerKey,
		big.NewInt(0),
		"slash(uint256,address)",
		callData3,
		"Test Slash Proposal 3",
		"Integration test for slash proposal 3",
	)
	require.NoError(t, err, "failed to create slash proposal 3")

	time.Sleep(1 * time.Second)

	contentOne := utils.ContentOne
	resp, err := contentOne.Eth.GetActiveSlashProposalForAddress(
		ctx,
		connect.NewRequest(&ethv1.GetActiveSlashProposalForAddressRequest{
			Address: contentThreeAddress,
		}),
	)
	require.NoError(t, err, "failed to get active slash proposal for content node three")

	slashAddr, slashAmount, err := eth.DecodeSlashProposalArguments(resp.Msg.CallData)
	require.NoError(t, err, "failed to decode slash proposal arguments")
	require.Equal(t, slashAddr.Cmp(contentThreeAddr), 0)
	require.Equal(t, contracts.WeiToAudio(slashAmount).Int64(), int64(100000))
}
