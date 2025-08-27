package console

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"connectrpc.com/connect"
	ethv1 "github.com/AudiusProject/audiusd/pkg/api/eth/v1"
	"github.com/AudiusProject/audiusd/pkg/core/console/views/pages"
	"github.com/AudiusProject/audiusd/pkg/core/db"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
)

const (
	activeValidatorReportHistoryLength = 30
	validatorReportHistoryLength       = 5
	slaMeetsThreshold                  = 0.8
	slaMissThreshold                   = 0.4
)

func (cs *Console) uptimeFragment(c echo.Context) error {
	ctx := c.Request().Context()
	endpointURL := c.Param("endpoint")
	rollupBlockEnd := c.Param("rollup")
	if endpointURL == "" {
		endpointURL = cs.config.NodeEndpoint
	} else {
		endpointURL = fmt.Sprintf("https://%s", endpointURL)
	}

	endpoints, activeEndpoint, err := cs.generateEndpoints(ctx, endpointURL)
	if err != nil {
		cs.logger.Error("failed to generate endpoints: %v", err)
		return err
	}

	if err := cs.populateSlaReportsForEndpoints(ctx, endpoints, activeEndpoint, rollupBlockEnd); err != nil {
		cs.logger.Error("failed to populate SLA reports for endpoints: %v", err)
		return cs.views.RenderUptimeView(c, &pages.UptimePageView{
			ActiveEndpoint: activeEndpoint,
		})
	}

	// SLA rollup not found, abort early with empty data
	if activeEndpoint.ActiveReport == nil {
		return cs.views.RenderUptimeView(c, &pages.UptimePageView{
			ActiveEndpoint: activeEndpoint,
		})
	}

	avgBlockTimeMs, err := cs.getAverageBlockTimeForReport(ctx, activeEndpoint.ActiveReport)
	if err != nil {
		cs.logger.Error("Failed to calculate average block time", "error", err)
		return err
	}

	// Store endpoints as sorted slice
	// (adjust sorting method to fit display preference)
	sort.Slice(endpoints, func(i, j int) bool {
		// TODO incorporate exempt status
		if endpoints[i].ActiveReport.BlocksProposed != endpoints[j].ActiveReport.BlocksProposed {
			return endpoints[i].ActiveReport.BlocksProposed < endpoints[j].ActiveReport.BlocksProposed
		}
		return endpoints[i].Endpoint < endpoints[j].Endpoint
	})

	return cs.views.RenderUptimeView(c, &pages.UptimePageView{
		ActiveEndpoint: activeEndpoint,
		Endpoints:      endpoints,
		AvgBlockTimeMs: avgBlockTimeMs,
	})
}

func (cs *Console) getActiveSlaRollup(ctx context.Context, rollupBlockEndParam string) (db.SlaRollup, error) {
	var rollup db.SlaRollup
	var err error
	if rollupBlockEndParam == "" || rollupBlockEndParam == "latest" {
		rollup, err = cs.db.GetLatestSlaRollup(ctx)
	} else if i, err := strconv.Atoi(rollupBlockEndParam); err == nil {
		rollup, err = cs.db.GetSlaRollupWithBlockEnd(ctx, int64(i))
		if err != nil {
			return rollup, err
		}
	} else {
		err = fmt.Errorf("sla page called with invalid rollup block end %s", rollupBlockEndParam)
		return rollup, err
	}
	if err != nil {
		err = fmt.Errorf("failed to retrieve SlaRollup from db: %v", err)
		return rollup, err
	}

	return rollup, nil
}

func (cs *Console) getAverageBlockTimeForReport(ctx context.Context, report *pages.SlaReport) (int, error) {
	var avgBlockTimeMs = 0
	previousRollup, err := cs.db.GetPreviousSlaRollupFromId(ctx, report.SlaRollupId)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		err = fmt.Errorf("failure reading previous SlaRollup from db: %v", err)
		return avgBlockTimeMs, err
	} else if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	} else if err == nil && report.BlockEnd != 0 {
		totalBlocks := int(report.BlockEnd - report.BlockStart)
		avgBlockTimeMs = int(report.Time.UnixMilli()-previousRollup.Time.Time.UnixMilli()) / totalBlocks
	}
	return avgBlockTimeMs, err
}

func endpointIsExemptForSlaReport(endpoint *pages.Endpoint, reportTimestamp time.Time) bool {
	return !endpoint.IsEthRegistered || endpoint.RegisteredAt.After(reportTimestamp)
}

func (cs *Console) generateEndpoints(ctx context.Context, activeEndpointURL string) (endpoints []*pages.Endpoint, activeEndpoint *pages.Endpoint, err error) {
	// Fetch all endpoints registered on eth
	endpointsResp, err := cs.eth.GetRegisteredEndpoints(
		ctx,
		connect.NewRequest(&ethv1.GetRegisteredEndpointsRequest{}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("error requesting endpoints from eth service: %v", err)
	}

	// Fetch all cometBFT validators
	cometValidators, err := cs.db.GetAllRegisteredNodes(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, fmt.Errorf("error fetching cometValidators from db: %v", err)
	}
	cometValidatorMap := make(map[string]*db.CoreValidator, len(cometValidators))
	for _, cv := range cometValidators {
		cometValidatorMap[cv.Endpoint] = &cv
	}

	// Transform endpoints into pages.Endpoint objects
	endpoints = make([]*pages.Endpoint, len(endpointsResp.Msg.Endpoints))
	for i, ep := range endpointsResp.Msg.Endpoints {
		pep := &pages.Endpoint{
			Endpoint:        ep.Endpoint,
			EthAddress:      ep.DelegateWallet,
			Owner:           ep.Owner,
			RegisteredAt:    ep.RegisteredAt.AsTime(),
			IsEthRegistered: true,
		}
		if ep.Endpoint == activeEndpointURL {
			activeEndpoint = pep
		}
		endpoints[i] = pep

		if cv, ok := cometValidatorMap[pep.Endpoint]; ok {
			pep.CometAddress = cv.CometAddress
		} else {
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
				return nil, nil, err
			} else if err == nil {
				pep.CometAddress = history.CometAddress
			}
		}
	}

	// If this server is unregistered, create a dummy endpoint
	if activeEndpoint == nil {
		activeEndpoint = &pages.Endpoint{Endpoint: activeEndpointURL}
	}

	return endpoints, activeEndpoint, nil
}

func (cs *Console) populateSlaReportsForEndpoints(ctx context.Context, endpoints []*pages.Endpoint, activeEndpoint *pages.Endpoint, rollupBlockEnd string) error {
	activeRollup, err := cs.getActiveSlaRollup(ctx, rollupBlockEnd)
	if err != nil {
		cs.logger.Error("failed to fetch active sla rollup", "error", err)
		return err
	}

	// Get SLA reports for active endpoint
	dbReports, err := cs.db.GetRollupReportsForNodeInTimeRange(
		ctx,
		db.GetRollupReportsForNodeInTimeRangeParams{
			Address: activeEndpoint.CometAddress, // Does not matter if unset
			Time:    cs.db.ToPgxTimestamp(activeRollup.Time.Time.Add(-24 * time.Hour)),
			Time_2:  cs.db.ToPgxTimestamp(activeRollup.Time.Time.Add(24 * time.Hour)),
		},
	)
	if err != nil {
		cs.logger.Error("failed to get rollup reports for active endpoint", "error", err)
		return err
	}

	// Attach each report to the active endpoint's history
	activeEndpoint.SlaReports = make([]*pages.SlaReport, 0, len(dbReports))
	for _, dbrep := range dbReports {
		pagerep := &pages.SlaReport{
			SlaRollupId:    dbrep.ID,
			TxHash:         dbrep.TxHash,
			BlockStart:     dbrep.BlockStart,
			BlockEnd:       dbrep.BlockEnd,
			BlocksProposed: dbrep.BlocksProposed.Int32,
			Time:           dbrep.Time.Time,
			ValidatorCount: dbrep.ValidatorCount,
		}
		if pagerep.SlaRollupId == activeRollup.ID {
			activeEndpoint.ActiveReport = pagerep
		}
		setSlaReportStatus(pagerep, activeEndpoint)
		activeEndpoint.SlaReports = append(activeEndpoint.SlaReports, pagerep)
	}

	// Now fetch the SLA reports for all endpoints, along with extra reports
	// to show a quick overview of each endpoint's SLA history

	// Organize endpoints in to map before populating with SlaRollups
	cometAddressToEndpointMap := make(map[string]*pages.Endpoint, len(endpoints))
	allCometAddresses := make([]string, len(endpoints))
	for i, ep := range endpoints {
		var key string
		if ep.CometAddress != "" {
			key = ep.CometAddress
		} else {
			// dummy address allows us to assign empty SlaReport from db later
			key = fmt.Sprintf("dummy_address_%d", i)
		}
		cometAddressToEndpointMap[key] = ep
		allCometAddresses[i] = key
	}

	// Get SLA reports for all endpoints from db in a six hour time window
	allDbReports, err := cs.db.GetRollupReportsForNodesInTimeRange(
		ctx,
		db.GetRollupReportsForNodesInTimeRangeParams{
			Column1: allCometAddresses,
			Time:    cs.db.ToPgxTimestamp(activeRollup.Time.Time.Add(-6 * time.Hour)),
			Time_2:  cs.db.ToPgxTimestamp(activeRollup.Time.Time),
		},
	)
	if err != nil {
		cs.logger.Error("failed to get rollup reports for all endpoints", "error", err)
		return err
	}

	// Attach each report to the appropriate endpoint's history
	for _, dbrep := range allDbReports {
		if ep, ok := cometAddressToEndpointMap[dbrep.Address]; ok {
			if ep.Endpoint == activeEndpoint.Endpoint {
				// we already filled out the history of the active endpoint, skip
				continue
			}
			if ep.SlaReports == nil {
				// initialize SlaReports slice with some extra headroom
				ep.SlaReports = make([]*pages.SlaReport, 0, len(allDbReports)/len(allCometAddresses)+3)
			}
			pagerep := &pages.SlaReport{
				SlaRollupId:    dbrep.ID,
				TxHash:         dbrep.TxHash,
				BlockStart:     dbrep.BlockStart,
				BlockEnd:       dbrep.BlockEnd,
				BlocksProposed: dbrep.BlocksProposed.Int32,
				Time:           dbrep.Time.Time,
				ValidatorCount: dbrep.ValidatorCount,
			}
			if pagerep.SlaRollupId == activeRollup.ID {
				ep.ActiveReport = pagerep
			}
			setSlaReportStatus(pagerep, ep)
			ep.SlaReports = append(ep.SlaReports, pagerep)
		}
	}

	// Get proof of storage history
	posRollups, err := cs.db.GetStorageProofRollups(
		ctx,
		db.GetStorageProofRollupsParams{
			BlockHeight:   activeRollup.BlockStart,
			BlockHeight_2: activeRollup.BlockEnd,
		},
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		cs.logger.Error("Failure getting proof of storage rollups", "error", err)
		return err
	}
	for _, posr := range posRollups {
		if ep, ok := cometAddressToEndpointMap[posr.Address]; ok {
			ep.ActiveReport.PoSChallengesFailed = int32(posr.FailedCount)
			ep.ActiveReport.PoSChallengesTotal = int32(posr.TotalCount)
		}
	}

	return nil
}

func setSlaReportStatus(slaReport *pages.SlaReport, endpoint *pages.Endpoint) {
	if endpointIsExemptForSlaReport(endpoint, slaReport.Time) {
		slaReport.Status = pages.SlaExempt
		slaReport.Quota = 0
	} else {
		if slaReport.ValidatorCount == 0 {
			// unexpected state, but protect against divide-by-zero panic anyway
			slaReport.ValidatorCount += 1
		}
		// +1 because range is inclusive
		quota := int32(slaReport.BlockEnd-slaReport.BlockStart+1) / int32(slaReport.ValidatorCount)
		if quota == 0 {
			// protect against divide-by-zero panic again
			quota += 1
		}
		slaReport.Quota = quota
		faultRatio := float64(slaReport.BlocksProposed) / float64(quota)
		if faultRatio < slaMeetsThreshold && faultRatio > 0 {
			slaReport.Status = pages.SlaPartial
		} else if faultRatio == 0 {
			slaReport.Status = pages.SlaDead
		} else {
			slaReport.Status = pages.SlaMet
		}
	}
}
