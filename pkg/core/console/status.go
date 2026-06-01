package console

import (
	"context"
	"errors"
	"strings"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/jackc/pgx/v5"
)

func (cs *Console) applyChainHeightFallback(ctx context.Context, status *v1.GetStatusResponse) {
	if status == nil {
		return
	}
	if status.ChainInfo == nil {
		status.ChainInfo = &v1.GetStatusResponse_ChainInfo{
			ChainId: cs.config.GenesisFile.ChainID,
		}
	}
	if status.ChainInfo.CurrentHeight > 0 && status.ChainInfo.CurrentBlockHash != "" {
		return
	}

	latestBlock, err := cs.db.GetLatestBlock(ctx)
	if err != nil {
		return
	}

	if status.ChainInfo.CurrentHeight <= 0 {
		status.ChainInfo.CurrentHeight = latestBlock.Height
	}
	if status.ChainInfo.CurrentHeight == latestBlock.Height && status.ChainInfo.CurrentBlockHash == "" {
		status.ChainInfo.CurrentBlockHash = latestBlock.Hash
	}
}

func (cs *Console) latestKnownHeight(ctx context.Context, currentHeight int64) int64 {
	if currentHeight > 0 {
		return currentHeight
	}

	latestBlock, err := cs.db.GetLatestBlock(ctx)
	if err != nil {
		return currentHeight
	}
	return latestBlock.Height
}

func (cs *Console) currentNodeJailed(ctx context.Context) (bool, error) {
	if cometAddress := strings.TrimSpace(cs.config.ProposerAddress); cometAddress != "" {
		node, err := cs.db.GetRegisteredNodeByCometAddress(ctx, cometAddress)
		if err == nil {
			return node.Jailed, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	}

	if ethAddress := strings.TrimSpace(cs.config.WalletAddress); ethAddress != "" {
		node, err := cs.db.GetRegisteredNodeByEthAddress(ctx, ethAddress)
		if err == nil {
			return node.Jailed, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	}

	nodes, err := cs.db.GetAllRegisteredNodesIncludingJailed(ctx)
	if err != nil {
		return false, err
	}
	for _, node := range nodes {
		if strings.EqualFold(node.CometAddress, cs.config.ProposerAddress) ||
			strings.EqualFold(node.EthAddress, cs.config.WalletAddress) ||
			normalizeEndpoint(node.Endpoint) == normalizeEndpoint(cs.config.NodeEndpoint) {
			return node.Jailed, nil
		}
	}

	return false, nil
}
