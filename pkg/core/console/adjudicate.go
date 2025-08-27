package console

import (
	"errors"
	"time"

	"connectrpc.com/connect"
	ethv1 "github.com/AudiusProject/audiusd/pkg/api/eth/v1"
	"github.com/AudiusProject/audiusd/pkg/core/console/views/pages"
	"github.com/AudiusProject/audiusd/pkg/core/db"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
)

const minimumAudioStakePerEndpoint = 200000

func (cs *Console) adjudicateFragment(c echo.Context) error {
	ctx := c.Request().Context()

	// Get service provider information
	serviceProviderAddress := c.Param("sp")
	_, err := cs.eth.GetServiceProvider(
		ctx,
		connect.NewRequest(&ethv1.GetServiceProviderRequest{Address: serviceProviderAddress}),
	)
	if err != nil {
		cs.logger.Error("Falled to get service provider", "address", serviceProviderAddress, "error", err)
		return err
	}

	// Get service provider's endpoints
	endpointsResp, err := cs.eth.GetRegisteredEndpointsForServiceProvider(
		ctx,
		connect.NewRequest(&ethv1.GetRegisteredEndpointsForServiceProviderRequest{Owner: serviceProviderAddress}),
	)
	if err != nil {
		cs.logger.Error("Falled to get service provider endpoints", "address", serviceProviderAddress, "error", err)
		return err
	}
	endpoints := endpointsResp.Msg.Endpoints

	// configure start and end times
	utcStart := time.Now().Add(-30 * 24 * time.Hour).UTC()
	startTime := time.Date(utcStart.Year(), utcStart.Month(), utcStart.Day(), 0, 0, 0, 0, time.UTC)
	utcEnd := time.Now()
	endTime := time.Date(utcEnd.Year(), utcEnd.Month(), utcEnd.Day(), 0, 0, 0, 0, time.UTC)
	if c.QueryParam("start") != "" {
		if parsed, err := time.Parse("2006-01-02", c.QueryParam("start")); err != nil {
			cs.logger.Warn("failed to parse start time from query string", "error", err)
		} else {
			startTime = parsed
		}
	}
	if c.QueryParam("end") != "" {
		if parsed, err := time.Parse("2006-01-02", c.QueryParam("end")); err != nil {
			cs.logger.Warn("failed to parse end time from query string", "error", err)
		} else {
			endTime = parsed
		}
	}

	// Populate endpoints and their SLAs for the view model
	viewEndpoints := make([]*pages.Endpoint, len(endpoints))
	storageProofRollups := make(map[string]*pages.StorageProofRollup, len(endpoints))
	totalChallenges, failedChallenges := int64(0), int64(0)
	totalMetSlas, totalPartialSlas, totalDeadSlas, totalSlas := 0, 0, 0, 0
	for i, ep := range endpoints {
		// map the endpoint received from eth service into a a UI endpoint object
		viewEndpoints[i] = &pages.Endpoint{
			Endpoint:        ep.Endpoint,
			EthAddress:      ep.DelegateWallet,
			IsEthRegistered: true,
			RegisteredAt:    ep.RegisteredAt.AsTime(),
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			cs.logger.Error("Falled to get rollups in time range", "start time", startTime, "end time", endTime, "error", err)
			return err
		}

		// Get the comet address for each endpoint, if possible
		var cometAddress string
		validator, err := cs.db.GetNodeByEndpoint(ctx, ep.Endpoint)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			cs.logger.Error("Falled to get cometbft validator for endpoint", "endpoint", ep.Endpoint, "error", err)
			return err
		} else if errors.Is(err, pgx.ErrNoRows) {
			// Endpoint is registered on eth but not on comet.
			// Attempt to get comet address from validator history
			history, err := cs.db.GetValidatorHistoryForID(
				ctx,
				db.GetValidatorHistoryForIDParams{
					ep.Id,
					ep.ServiceType,
				},
			)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				cs.logger.Error("Falled to get validator history for endpoint", "endpoint", ep.Endpoint, "error", err)
				return err
			} else if err == nil {
				cometAddress = history.CometAddress
			}
		} else if err == nil {
			cometAddress = validator.CometAddress
		}
		viewEndpoints[i].CometAddress = cometAddress

		// Add individual sla reports to endpoint data
		slaRollups, err := cs.db.GetRollupReportsForNodeInTimeRange(
			ctx,
			db.GetRollupReportsForNodeInTimeRangeParams{
				Address: cometAddress,
				Time:    cs.db.ToPgxTimestamp(startTime),
				Time_2:  cs.db.ToPgxTimestamp(endTime),
			},
		)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			cs.logger.Error("Failed to get rollups from db for node", "address", cometAddress, "start time", startTime, "end time", endTime, "error", err)
			return err
		}
		viewSlaReports := make([]*pages.SlaReport, len(slaRollups))
		for j, rollup := range slaRollups {
			pageReport := &pages.SlaReport{
				SlaRollupId:    rollup.ID,
				TxHash:         rollup.TxHash,
				BlockStart:     rollup.BlockStart,
				BlockEnd:       rollup.BlockEnd,
				Time:           rollup.Time.Time,
				ValidatorCount: rollup.ValidatorCount,
				BlocksProposed: rollup.BlocksProposed.Int32,
			}
			setSlaReportStatus(pageReport, viewEndpoints[i])
			totalSlas += 1
			switch pageReport.Status {
			case pages.SlaMet:
				totalMetSlas += 1
			case pages.SlaPartial:
				totalPartialSlas += 1
			case pages.SlaDead:
				totalDeadSlas += 1
			}
			viewSlaReports[j] = pageReport
		}
		viewEndpoints[i].SlaReports = viewSlaReports

		// Add storage proof counts to endpoint data
		if viewEndpoints[i].CometAddress != "" && len(viewSlaReports) > 0 {
			cometAddress := viewEndpoints[i].CometAddress
			counts, err := cs.db.GetStorageProofRollupForNode(
				ctx,
				db.GetStorageProofRollupForNodeParams{
					Address:       cometAddress,
					BlockHeight:   viewSlaReports[0].BlockStart,
					BlockHeight_2: viewSlaReports[len(viewSlaReports)-1].BlockEnd,
				},
			)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				cs.logger.Error("Failed to get storage proofs for validator", "validator address", cometAddress, "error", err)
				return err
			} else if err == nil {
				storageProofRollups[cometAddress] = &pages.StorageProofRollup{
					ChallengesReceived: counts.TotalCount,
					ChallengesFailed:   counts.FailedCount,
				}
				totalChallenges += counts.TotalCount
				failedChallenges += counts.FailedCount
			}
		}
	}

	// Calculate slash recommendation based on SLA performance.
	// Explanation:
	//   200k AUDIO is the minimum stake per endpoint.
	//   We therefore recommend slashing 200k AUDIO for a full year of zero sla performance
	//   from a single endpoint.
	//   $AUDIO to slash = $200k * number of endpoints * (days in selected interval / 365) * (zeroed SLAs / total SLAs)
	periodDays := int64(endTime.Sub(startTime).Hours() / 24)
	slashRecommendation := int64(200000.0 * float64(len(endpoints)) * (float64(periodDays) / 365.0) * (float64(totalDeadSlas) / float64(totalSlas)))

	stakingResp, err := cs.eth.GetStakingMetadataForServiceProvider(
		ctx,
		connect.NewRequest(&ethv1.GetStakingMetadataForServiceProviderRequest{Address: serviceProviderAddress}),
	)
	if err != nil {
		cs.logger.Error("Falled to get service provider staking metadata", "address", serviceProviderAddress, "error", err)
		return err
	}

	view := &pages.AdjudicatePageView{
		ServiceProvider: &pages.ServiceProvider{
			Address:             serviceProviderAddress,
			Endpoints:           viewEndpoints,
			StorageProofRollups: storageProofRollups,
		},
		StartTime:           startTime,
		EndTime:             endTime,
		MetSlas:             totalMetSlas,
		PartialSlas:         totalPartialSlas,
		DeadSlas:            totalDeadSlas,
		TotalSlas:           totalSlas,
		TotalStaked:         stakingResp.Msg.TotalStaked,
		TotalChallenges:     totalChallenges,
		FailedChallenges:    failedChallenges,
		SlashRecommendation: slashRecommendation,
	}

	return cs.views.RenderAdjudicateView(c, view)
}
