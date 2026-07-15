package console

import (
	"context"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/core/console/views/pages"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

// getConsensusHealth reads the local CometBFT consensus + status and produces a
// sanitized ConsensusHealth summary. It never returns an error: if the RPC is
// unreachable it returns an "unknown" health so the banner/panel can fail closed.
func (cs *Console) getConsensusHealth(ctx context.Context) *pages.ConsensusHealth {
	state, err := cs.rpc.ConsensusState(ctx)
	if err != nil {
		cs.logger.Debug("consensus state unavailable", zap.Error(err))
		return pages.UnknownConsensusHealth()
	}

	var latestBlockTime time.Time
	catchingUp := false
	if status, err := cs.rpc.Status(ctx); err != nil {
		cs.logger.Debug("status unavailable for consensus health", zap.Error(err))
	} else {
		latestBlockTime = status.SyncInfo.LatestBlockTime
		catchingUp = status.SyncInfo.CatchingUp
	}

	health, err := pages.AnalyzeConsensusState(state.RoundState, latestBlockTime, catchingUp, time.Now(), pages.DefaultHaltThresholds())
	if err != nil {
		cs.logger.Debug("could not analyze consensus state", zap.Error(err))
		return pages.UnknownConsensusHealth()
	}
	return health
}

// navConsensusHalt renders the site-wide halt banner (empty unless halted).
func (cs *Console) navConsensusHalt(c echo.Context) error {
	health := cs.getConsensusHealth(c.Request().Context())
	return cs.views.RenderConsensusHaltBanner(c, health)
}

// overviewConsensusFragment renders the consensus health detail card.
func (cs *Console) overviewConsensusFragment(c echo.Context) error {
	health := cs.getConsensusHealth(c.Request().Context())
	return cs.views.RenderConsensusPanel(c, health)
}
