package console

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/console/templates/pages"
	"github.com/AudiusProject/audiusd/pkg/etl"
	"github.com/AudiusProject/audiusd/pkg/etl/db"
	"github.com/AudiusProject/audiusd/pkg/sdk"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"
	"golang.org/x/sync/errgroup"

	"embed"
)

//go:embed assets/css
var cssFS embed.FS

//go:embed assets/images
var imagesFS embed.FS

//go:embed assets/js
var jsFS embed.FS

type Console struct {
	env                string
	e                  *echo.Echo
	etl                *etl.ETLService
	logger             *common.Logger
	trustedNode        *sdk.AudiusdSDK
	latestTrustedBlock atomic.Int64
	lastRefreshTime    atomic.Int64  // Unix timestamp of last refresh
	refreshInterval    time.Duration // How often to refresh
}

func NewConsole(etl *etl.ETLService, e *echo.Echo, env string) *Console {
	if e == nil {
		e = echo.New()
	}
	if env == "" {
		env = "prod"
	}

	trustedNodeURL := ""

	switch env {
	case "prod", "production", "mainnet":
		trustedNodeURL = "rpc.audius.engineering"
	case "staging", "stage", "testnet":
		trustedNodeURL = "rpc.staging.audius.engineering"
	case "dev":
		trustedNodeURL = "rpc.dev.audius.engineering"
	}

	return &Console{
		etl:             etl,
		e:               e,
		logger:          common.NewLogger(nil).Child("console"),
		env:             env,
		trustedNode:     sdk.NewAudiusdSDK(trustedNodeURL),
		refreshInterval: 10 * time.Second,
	}
}

func (con *Console) Initialize() {
	// start refresher
	go con.refreshTrustedBlock()

	e := con.e
	e.HideBanner = true

	// Add environment context middleware
	envMiddleware := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Add environment to the request context
			ctx := context.WithValue(c.Request().Context(), "env", con.env)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}

	// Add cache control middleware for static assets
	cacheControl := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			path := c.Request().URL.Path
			// Only apply caching to image files
			if strings.HasPrefix(path, "/assets/") && (strings.HasSuffix(path, ".svg") || strings.HasSuffix(path, ".png") || strings.HasSuffix(path, ".jpg") || strings.HasSuffix(path, ".jpeg") || strings.HasSuffix(path, ".gif")) {
				c.Response().Header().Set("Cache-Control", "public, max-age=604800") // Cache for 1 week
			}
			return next(c)
		}
	}

	cssHandler := echo.MustSubFS(cssFS, "assets/css")
	imagesHandler := echo.MustSubFS(imagesFS, "assets/images")
	jsHandler := echo.MustSubFS(jsFS, "assets/js")
	e.StaticFS("/assets/css", cssHandler)
	e.StaticFS("/assets/images", imagesHandler)
	e.StaticFS("/assets/js", jsHandler)

	// Apply middlewares
	e.Use(cacheControl)
	e.Use(envMiddleware)

	e.GET("/", con.Dashboard)
	e.GET("/hello", con.Hello)

	e.GET("/validators", con.Validators)
	e.GET("/validator/:address", con.Validator)
	e.GET("/validators/uptime", con.ValidatorsUptime)
	e.GET("/validators/uptime/:rollupid", con.ValidatorsUptimeByRollup)

	e.GET("/rollups", con.Rollups)

	e.GET("/blocks", con.Blocks)
	e.GET("/block/:height", con.Block)

	e.GET("/transactions", con.Transactions)
	e.GET("/transaction/:hash", con.Transaction)

	e.GET("/account/:address", con.Account)
	e.GET("/account/:address/transactions", con.stubRoute)
	e.GET("/account/:address/uploads", con.stubRoute)
	e.GET("/account/:address/releases", con.stubRoute)

	e.GET("/content", con.Content)
	e.GET("/content/:address", con.Content)

	e.GET("/release/:address", con.stubRoute)

	e.GET("/search", con.Search)

	// SSE endpoints
	e.GET("/sse/events", con.LiveEventsSSE)

	// HTMX Fragment routes
	e.GET("/fragments/stats-header", con.StatsHeaderFragment)
	e.GET("/fragments/tps", con.TPSFragment)
	e.GET("/fragments/total-transactions", con.TotalTransactionsFragment)
}

func (con *Console) Run() error {
	g, _ := errgroup.WithContext(context.Background())

	g.Go(func() error {
		info, err := con.trustedNode.Core.GetNodeInfo(context.Background(), &connect.Request[corev1.GetNodeInfoRequest]{})
		if err != nil {
			con.logger.Warn("Failed to initialize node info", "error", err)
			con.latestTrustedBlock.Store(0)
		} else {
			con.logger.Info("Initialized node info", "height", info.Msg.CurrentHeight)
			con.latestTrustedBlock.Store(info.Msg.CurrentHeight)
			con.lastRefreshTime.Store(time.Now().Unix())
		}

		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			info, err := con.trustedNode.Core.GetNodeInfo(context.Background(), &connect.Request[corev1.GetNodeInfoRequest]{})
			if err != nil {
				con.logger.Warn("Failed to get node info", "error", err)
				continue
			}
			con.latestTrustedBlock.Store(info.Msg.CurrentHeight)
			con.lastRefreshTime.Store(time.Now().Unix())
			con.logger.Info("Updated node info", "height", info.Msg.CurrentHeight)
		}
		return nil
	})

	g.Go(func() error {
		if err := con.etl.Run(); err != nil {
			return err
		}
		return nil
	})

	g.Go(func() error {
		if err := con.e.Start(":3000"); err != nil {
			return err
		}
		return nil
	})

	return g.Wait()
}

func (con *Console) Stop() {
	con.e.Shutdown(context.Background())
}

// getTransactionsWithBlockHeights is a helper method to get transactions with their block heights
func (con *Console) getTransactionsWithBlockHeights(ctx context.Context, limit, offset int32) ([]*db.EtlTransaction, map[string]int64, error) {
	// Use GetTransactionsByPage for proper offset-based pagination
	transactions, err := con.etl.GetDB().GetTransactionsByPage(ctx, db.GetTransactionsByPageParams{
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return nil, nil, err
	}

	// Convert to pointers and create block heights map
	txPointers := make([]*db.EtlTransaction, len(transactions))
	blockHeights := make(map[string]int64)
	for i := range transactions {
		txPointers[i] = &transactions[i]
		blockHeights[transactions[i].TxHash] = transactions[i].BlockHeight
	}

	return txPointers, blockHeights, nil
}

func (con *Console) Hello(c echo.Context) error {
	param := "sup"
	if name := c.QueryParam("name"); name != "" {
		param = name
	}
	p := pages.Hello(param)

	// Use context with environment
	ctx := c.Request().Context()
	return p.Render(ctx, c.Response().Writer)
}

func (con *Console) Dashboard(c echo.Context) error {
	ctx := c.Request().Context()

	// Get dashboard transaction stats from materialized view
	txStats, err := con.etl.GetDB().GetDashboardTransactionStats(ctx)
	if err != nil {
		con.logger.Warn("Failed to get dashboard transaction stats", "error", err)
		// Use fallback empty stats
		txStats = db.MvDashboardTransactionStat{}
	}

	// Get transaction type breakdown from materialized view
	txTypes, err2 := con.etl.GetDB().GetDashboardTransactionTypes(ctx)
	if err2 != nil {
		con.logger.Warn("Failed to get dashboard transaction types", "error", err2)
		txTypes = []db.MvDashboardTransactionType{}
	}

	// Get latest indexed block
	latestBlockHeight, err := con.etl.GetDB().GetLatestIndexedBlock(ctx)
	if err != nil {
		con.logger.Warn("Failed to get latest block height", "error", err)
		latestBlockHeight = 0
	}

	// Get latest trusted block height from the trusted node
	trustedBlockHeight := int64(con.latestTrustedBlock.Load())

	// Calculate sync status - consider synced if within 60 blocks of the head
	const syncThreshold = 60
	var isSyncing bool
	var blockDelta int64
	syncProgressPercentage := float64(100) // Default to fully synced

	con.logger.Info("Latest block height", "height", latestBlockHeight)
	con.logger.Info("Trusted block height", "height", trustedBlockHeight)

	if trustedBlockHeight >= 0 && latestBlockHeight >= 0 {

		blockDelta = trustedBlockHeight - latestBlockHeight
		isSyncing = blockDelta > syncThreshold

		con.logger.Info("Block delta", "delta", blockDelta)
		con.logger.Info("Is syncing", "isSyncing", isSyncing)

		if isSyncing && trustedBlockHeight > 0 {
			syncProgressPercentage = (float64(latestBlockHeight) / float64(trustedBlockHeight)) * 100
			con.logger.Info("Sync progress percentage", "percentage", syncProgressPercentage)
			// Ensure percentage doesn't exceed 100
			if syncProgressPercentage > 100 {
				syncProgressPercentage = 100
			}
		} else {
			syncProgressPercentage = 100 // Fully synced
		}
	} else {
		con.logger.Info("No trusted block info, assuming synced")
		// If we don't have trusted block info, assume synced
		isSyncing = false
		blockDelta = 0
		syncProgressPercentage = 100
	}

	// Get latest SLA rollup for BPS/TPS data
	var bps, tps float64 = 0, 0
	var avgBlockTime float32 = 0
	latestSlaRollup, err := con.etl.GetDB().GetLatestSlaRollup(ctx)
	if err != nil {
		con.logger.Debug("Failed to get latest SLA rollup", "error", err)
		// Fall back to default values
		bps = 0.5
		tps = 0.1
		avgBlockTime = 2.0
	} else {
		bps = latestSlaRollup.Bps
		tps = latestSlaRollup.Tps
		// Calculate average block time from BPS (if BPS > 0)
		if bps > 0 {
			avgBlockTime = float32(1.0 / bps)
		} else {
			avgBlockTime = 2.0 // Default 2 seconds
		}
	}

	// Get some recent transactions for the dashboard
	transactions, blockHeights, err := con.getTransactionsWithBlockHeights(ctx, 10, 0)
	if err != nil {
		con.logger.Warn("Failed to get transactions", "error", err)
		transactions = []*db.EtlTransaction{}
		blockHeights = make(map[string]int64)
	}

	blocks, err := con.etl.GetDB().GetBlocksByPage(ctx, db.GetBlocksByPageParams{
		Limit:  10,
		Offset: 0,
	})
	if err != nil {
		con.logger.Warn("Failed to get blocks", "error", err)
		return c.String(http.StatusInternalServerError, "Failed to get blocks")
	}

	blockPointers := make([]*db.EtlBlock, len(blocks))
	for i := range blocks {
		blockPointers[i] = &blocks[i]
	}

	// Get active validator count
	validatorCount, err := con.etl.GetDB().GetActiveValidatorCount(ctx)
	if err != nil {
		con.logger.Warn("Failed to get validator count", "error", err)
		validatorCount = 0
	}

	// Build stats using materialized view data
	stats := &pages.DashboardStats{
		CurrentBlockHeight:           latestBlockHeight,
		ChainID:                      con.etl.ChainID,
		BPS:                          bps,
		TPS:                          tps,
		TotalTransactions:            txStats.TotalTransactions,
		ValidatorCount:               validatorCount,
		LatestBlock:                  nil, // TODO: Implement
		RecentProposers:              nil, // TODO: Implement
		IsSyncing:                    isSyncing,
		LatestIndexedHeight:          latestBlockHeight,
		LatestChainHeight:            trustedBlockHeight,
		BlockDelta:                   blockDelta,
		TotalTransactions24h:         txStats.Transactions24h,
		TotalTransactionsPrevious24h: txStats.TransactionsPrevious24h,
		TotalTransactions7d:          txStats.Transactions7d,
		TotalTransactions30d:         txStats.Transactions30d,
		AvgBlockTime:                 avgBlockTime,
	}

	// Convert materialized view transaction types to template format
	maxTypes := 5 // only show up to 5 transaction types
	if len(txTypes) < maxTypes {
		maxTypes = len(txTypes)
	}
	transactionBreakdown := make([]*pages.TransactionTypeBreakdown, maxTypes)
	colors := []string{"bg-blue-500", "bg-green-500", "bg-purple-500", "bg-yellow-500", "bg-red-500", "bg-indigo-500", "bg-pink-500"}
	for i := 0; i < maxTypes; i++ {
		txType := txTypes[i]
		color := colors[i%len(colors)]
		transactionBreakdown[i] = &pages.TransactionTypeBreakdown{
			Type:  txType.TxType,
			Count: txType.TransactionCount,
			Color: color,
		}
	}

	// Get SLA performance data for the chart (most recent 50 rollups)
	slaRollupsData, err := con.etl.GetDB().GetSlaRollupsWithPagination(ctx, db.GetSlaRollupsWithPaginationParams{
		Limit:  50,
		Offset: 0,
	})
	if err != nil {
		con.logger.Warn("Failed to get SLA rollups for performance chart", "error", err)
		slaRollupsData = []db.EtlSlaRollup{}
	}

	con.logger.Info("SLA rollups data retrieved", "count", len(slaRollupsData))

	// Build SLA performance data points for chart - Initialize as empty slice, not nil
	slaPerformanceData := make([]*pages.SLAPerformanceDataPoint, 0)

	// Build chart data if we have any rollups
	if len(slaRollupsData) > 0 {
		con.logger.Info("Building SLA performance chart data", "rollupCount", len(slaRollupsData))

		// Extract rollup IDs for healthy validators query
		rollupIDs := make([]int32, len(slaRollupsData))
		for i, rollup := range slaRollupsData {
			rollupIDs[i] = rollup.ID
		}

		// Get healthy validator counts for these rollups
		healthyValidatorData, err := con.etl.GetDB().GetHealthyValidatorCountsForRollups(ctx, rollupIDs)
		if err != nil {
			con.logger.Warn("Failed to get healthy validator counts", "error", err)
			healthyValidatorData = []db.GetHealthyValidatorCountsForRollupsRow{}
		}

		// Build a map for quick lookup of healthy validator counts
		healthyValidatorsMap := make(map[int32]int32)
		for _, hvData := range healthyValidatorData {
			if healthyCount, ok := hvData.HealthyValidators.(int64); ok {
				healthyValidatorsMap[hvData.RollupID] = int32(healthyCount)
			} else {
				healthyValidatorsMap[hvData.RollupID] = 0
			}
		}

		// Filter out invalid rollups and build valid data points
		validDataPoints := make([]*pages.SLAPerformanceDataPoint, 0, len(slaRollupsData))
		for i, rollup := range slaRollupsData {
			// Log the first rollup to see what data we're getting
			if i == 0 {
				con.logger.Info("First rollup data sample",
					"id", rollup.ID,
					"blockHeight", rollup.BlockHeight,
					"validatorCount", rollup.ValidatorCount,
					"bps", rollup.Bps,
					"tps", rollup.Tps,
					"createdAtValid", rollup.CreatedAt.Valid,
					"blockStart", rollup.BlockStart,
					"blockEnd", rollup.BlockEnd)
			}

			// Comprehensive validation of rollup data
			if rollup.ID <= 0 {
				con.logger.Debug("Skipping rollup with invalid ID", "index", i, "rollupId", rollup.ID)
				continue
			}

			if !rollup.CreatedAt.Valid {
				con.logger.Debug("Skipping rollup with invalid timestamp", "rollupId", rollup.ID)
				continue
			}

			if rollup.BlockHeight <= 0 {
				con.logger.Debug("Skipping rollup with invalid block height", "rollupId", rollup.ID, "blockHeight", rollup.BlockHeight)
				continue
			}

			if rollup.Bps < 0 || rollup.Tps < 0 {
				con.logger.Debug("Skipping rollup with invalid performance data", "rollupId", rollup.ID, "bps", rollup.Bps, "tps", rollup.Tps)
				continue
			}

			if rollup.BlockStart < 0 || rollup.BlockEnd <= 0 || rollup.BlockStart > rollup.BlockEnd {
				con.logger.Debug("Skipping rollup with invalid block range", "rollupId", rollup.ID, "start", rollup.BlockStart, "end", rollup.BlockEnd)
				continue
			}

			// Use the validator count from the rollup data itself
			validatorCount := rollup.ValidatorCount
			if validatorCount <= 0 {
				con.logger.Debug("Invalid validator count in rollup, using fallback", "rollupId", rollup.ID, "count", validatorCount)
				validatorCount = 1 // Minimum of 1 validator
			}

			// Get healthy validators count for this rollup
			healthyValidators := int32(0)
			if healthyCount, exists := healthyValidatorsMap[rollup.ID]; exists {
				healthyValidators = healthyCount
			}

			// Create a fully validated data point
			dataPoint := &pages.SLAPerformanceDataPoint{
				RollupID:          rollup.ID,
				BlockHeight:       rollup.BlockHeight,
				Timestamp:         rollup.CreatedAt.Time.Format(time.RFC3339),
				ValidatorCount:    validatorCount,
				HealthyValidators: healthyValidators,
				BPS:               rollup.Bps,
				TPS:               rollup.Tps,
				BlockStart:        rollup.BlockStart,
				BlockEnd:          rollup.BlockEnd,
			}

			// Extra safety check - ensure we're not adding nil
			if dataPoint != nil {
				validDataPoints = append(validDataPoints, dataPoint)
			}
		}

		// Use the data if we have any valid points after filtering
		if len(validDataPoints) > 0 {
			slaPerformanceData = validDataPoints
			con.logger.Info("Successfully built SLA performance data", "validPoints", len(validDataPoints))
		} else {
			con.logger.Warn("No valid rollup data points after filtering", "valid", len(validDataPoints), "total", len(slaRollupsData))
			// Keep the empty slice - don't set to nil
		}
	} else {
		con.logger.Info("No rollups available for chart", "rollupCount", len(slaRollupsData))
	}

	// Final debug of what we're passing to template
	con.logger.Info("Final SLA performance data for template", "dataPoints", len(slaPerformanceData))

	// Convert rollups to pointers for template
	recentSLARollups := make([]*db.EtlSlaRollup, len(slaRollupsData))
	for i := range slaRollupsData {
		recentSLARollups[i] = &slaRollupsData[i]
	}

	props := pages.DashboardProps{
		Stats:                  stats,
		TransactionBreakdown:   transactionBreakdown,
		RecentBlocks:           blockPointers,
		RecentTransactions:     transactions,
		RecentSLARollups:       recentSLARollups,
		SLAPerformanceData:     slaPerformanceData,
		BlockHeights:           blockHeights,
		SyncProgressPercentage: syncProgressPercentage,
	}

	p := pages.Dashboard(props)

	// Use context with environment
	return p.Render(ctx, c.Response().Writer)
}

func (con *Console) Validators(c echo.Context) error {
	// Parse query parameters
	pageParam := c.QueryParam("page")
	countParam := c.QueryParam("count")
	queryType := c.QueryParam("type") // "active", "registrations", "deregistrations"
	endpointFilter := c.QueryParam("endpoint_filter")

	page := int32(1) // default to page 1
	if pageParam != "" {
		if parsedPage, err := strconv.ParseInt(pageParam, 10, 32); err == nil && parsedPage > 0 {
			page = int32(parsedPage)
		}
	}

	count := int32(50) // default to 50 per page
	if countParam != "" {
		if parsedCount, err := strconv.ParseInt(countParam, 10, 32); err == nil && parsedCount > 0 && parsedCount <= 200 {
			count = int32(parsedCount)
		}
	}

	// Default to active validators
	if queryType == "" {
		queryType = "active"
	}

	// Calculate offset from page number
	offset := (page - 1) * count

	var validators []*db.EtlValidator
	validatorUptimeMap := make(map[string][]*db.EtlSlaNodeReport)

	ctx := c.Request().Context()

	switch queryType {
	case "active":
		// Get active validators
		validatorsData, err := con.etl.GetDB().GetActiveValidators(ctx, db.GetActiveValidatorsParams{
			Limit:  count,
			Offset: offset,
		})
		if err != nil {
			con.logger.Warn("Failed to get active validators", "error", err)
			validatorsData = []db.EtlValidator{}
		}

		// Convert to pointers and apply endpoint filter
		for i := range validatorsData {
			if endpointFilter == "" || strings.Contains(strings.ToLower(validatorsData[i].Endpoint), strings.ToLower(endpointFilter)) {
				validators = append(validators, &validatorsData[i])

				// Get uptime data for each validator
				reports, err := con.etl.GetDB().GetSlaNodeReportsByAddress(ctx, db.GetSlaNodeReportsByAddressParams{
					Lower: validatorsData[i].CometAddress,
					Limit: 5, // Get last 5 SLA reports
				})
				if err != nil {
					con.logger.Warn("Failed to get SLA reports", "address", validatorsData[i].CometAddress, "error", err)
				} else {
					reportPointers := make([]*db.EtlSlaNodeReport, len(reports))
					for j := range reports {
						reportPointers[j] = &reports[j]
					}
					validatorUptimeMap[validatorsData[i].CometAddress] = reportPointers
				}
			}
		}

	case "registrations":
		// Get validator registrations - this will need a different approach since it's a different table
		regsData, err := con.etl.GetDB().GetValidatorRegistrations(ctx, db.GetValidatorRegistrationsParams{
			Limit:  count,
			Offset: offset,
		})
		if err != nil {
			con.logger.Warn("Failed to get validator registrations", "error", err)
			regsData = []db.GetValidatorRegistrationsRow{}
		}

		// Convert registrations to validator format for template
		for i := range regsData {
			validator := &db.EtlValidator{
				ID:           regsData[i].ID,
				Address:      regsData[i].Address,
				Endpoint:     regsData[i].Endpoint, // Already a string
				CometAddress: regsData[i].CometAddress,
				NodeType:     regsData[i].NodeType,    // Already a string
				Spid:         regsData[i].Spid,        // Already a string
				VotingPower:  regsData[i].VotingPower, // Already int64
				Status:       "registered",
				RegisteredAt: regsData[i].BlockHeight,
				CreatedAt:    pgtype.Timestamp{Time: time.Now(), Valid: true}, // Manual timestamp
			}
			if endpointFilter == "" || strings.Contains(strings.ToLower(validator.Endpoint), strings.ToLower(endpointFilter)) {
				validators = append(validators, validator)
			}
		}

	case "deregistrations":
		// Get validator deregistrations
		deregsData, err := con.etl.GetDB().GetValidatorDeregistrations(ctx, db.GetValidatorDeregistrationsParams{
			Limit:  count,
			Offset: offset,
		})
		if err != nil {
			con.logger.Warn("Failed to get validator deregistrations", "error", err)
			deregsData = []db.GetValidatorDeregistrationsRow{}
		}

		// Convert deregistrations to validator format for template
		for i := range deregsData {
			endpoint := ""
			if deregsData[i].Endpoint.Valid {
				endpoint = deregsData[i].Endpoint.String
			}
			nodeType := ""
			if deregsData[i].NodeType.Valid {
				nodeType = deregsData[i].NodeType.String
			}
			spid := ""
			if deregsData[i].Spid.Valid {
				spid = deregsData[i].Spid.String
			}
			votingPower := int64(0)
			if deregsData[i].VotingPower.Valid {
				votingPower = deregsData[i].VotingPower.Int64
			}

			validator := &db.EtlValidator{
				ID:           deregsData[i].ID,
				Address:      "",
				Endpoint:     endpoint,
				CometAddress: deregsData[i].CometAddress,
				NodeType:     nodeType,
				Spid:         spid,
				VotingPower:  votingPower,
				Status:       "deregistered",
				RegisteredAt: deregsData[i].BlockHeight,
				CreatedAt:    pgtype.Timestamp{Time: time.Now(), Valid: true}, // placeholder
			}
			if endpointFilter == "" || strings.Contains(strings.ToLower(validator.Endpoint), strings.ToLower(endpointFilter)) {
				validators = append(validators, validator)
			}
		}
	}

	// Calculate pagination state
	hasNext := len(validators) == int(count) // Simple check - if we got the full limit, there might be more
	hasPrev := page > 1

	props := pages.ValidatorsProps{
		Validators:         validators,
		ValidatorUptimeMap: validatorUptimeMap,
		CurrentPage:        page,
		HasNext:            hasNext,
		HasPrev:            hasPrev,
		PageSize:           count,
		QueryType:          queryType,
		EndpointFilter:     endpointFilter,
	}

	p := pages.Validators(props)
	return p.Render(ctx, c.Response().Writer)
}

func (con *Console) Validator(c echo.Context) error {
	address := c.Param("address")
	if address == "" {
		return c.String(http.StatusBadRequest, "Validator address required")
	}

	ctx := c.Request().Context()

	// Get validator by address
	validator, err := con.etl.GetDB().GetValidatorByAddress(ctx, address)
	if err != nil {
		return c.String(http.StatusNotFound, fmt.Sprintf("Validator not found: %s", address))
	}

	// Get SLA rollup reports for this validator
	reports, err := con.etl.GetDB().GetSlaNodeReportsByAddress(ctx, db.GetSlaNodeReportsByAddressParams{
		Lower: validator.CometAddress,
		Limit: 10, // Get last 10 reports
	})
	if err != nil {
		con.logger.Warn("Failed to get SLA reports for validator", "address", address, "error", err)
		reports = []db.EtlSlaNodeReport{}
	}

	// Convert reports to pointers
	rollups := make([]*db.EtlSlaNodeReport, len(reports))
	for i := range reports {
		rollups[i] = &reports[i]
	}

	// TODO: Get validator events from registration/deregistration tables
	// For now, create empty events slice
	events := []*pages.ValidatorEvent{}

	props := pages.ValidatorProps{
		Validator: &validator,
		Events:    events,
		Rollups:   rollups,
	}

	p := pages.Validator(props)
	return p.Render(ctx, c.Response().Writer)
}

func (con *Console) ValidatorsUptime(c echo.Context) error {
	// Parse query parameters for pagination
	pageParam := c.QueryParam("page")
	countParam := c.QueryParam("count")

	page := int32(1) // default to page 1
	if pageParam != "" {
		if parsedPage, err := strconv.ParseInt(pageParam, 10, 32); err == nil && parsedPage > 0 {
			page = int32(parsedPage)
		}
	}

	count := int32(20) // default to 20 per page for rollups
	if countParam != "" {
		if parsedCount, err := strconv.ParseInt(countParam, 10, 32); err == nil && parsedCount > 0 && parsedCount <= 100 {
			count = int32(parsedCount)
		}
	}

	// Calculate offset from page number
	offset := (page - 1) * count

	ctx := c.Request().Context()

	// Get paginated SLA rollups
	rollupsData, err := con.etl.GetDB().GetSlaRollupsWithPagination(ctx, db.GetSlaRollupsWithPaginationParams{
		Limit:  count,
		Offset: offset,
	})
	if err != nil {
		con.logger.Warn("Failed to get SLA rollups", "error", err)
		rollupsData = []db.EtlSlaRollup{}
	}

	// Convert to pointers
	rollups := make([]*db.EtlSlaRollup, len(rollupsData))
	for i := range rollupsData {
		rollups[i] = &rollupsData[i]
	}

	// Calculate pagination state
	hasNext := len(rollupsData) == int(count)
	hasPrev := page > 1

	// TODO: Get actual total count from database
	totalCount := int64(len(rollupsData)) // Placeholder

	props := pages.RollupsProps{
		Rollups:          rollups,
		RollupValidators: []*db.EtlSlaNodeReport{}, // Not needed for rollups list view
		CurrentPage:      page,
		HasNext:          hasNext,
		HasPrev:          hasPrev,
		PageSize:         count,
		TotalCount:       totalCount,
	}

	p := pages.Rollups(props)
	return p.Render(ctx, c.Response().Writer)
}

func (con *Console) ValidatorsUptimeByRollup(c echo.Context) error {
	rollupIDParam := c.Param("rollupid")
	if rollupIDParam == "" {
		return c.String(http.StatusBadRequest, "Rollup ID required")
	}

	rollupID, err := strconv.ParseInt(rollupIDParam, 10, 32)
	if err != nil {
		return c.String(http.StatusBadRequest, "Invalid rollup ID")
	}

	ctx := c.Request().Context()

	// First, get the actual SLA rollup data to get tx_hash, created_at, block quota, etc.
	rollupInfo, err := con.etl.GetDB().GetSlaRollupById(ctx, int32(rollupID))
	if err != nil {
		con.logger.Warn("Failed to get SLA rollup by ID", "rollupID", rollupID, "error", err)
		return c.String(http.StatusNotFound, fmt.Sprintf("SLA rollup not found: %d", rollupID))
	}

	// Get validators for this specific SLA rollup
	validatorsData, err := con.etl.GetDB().GetValidatorsForSlaRollup(ctx, int32(rollupID))
	if err != nil {
		con.logger.Warn("Failed to get validators for SLA rollup", "rollupID", rollupID, "error", err)
		validatorsData = []db.GetValidatorsForSlaRollupRow{}
	}

	// Calculate challenge statistics dynamically for this rollup's block range
	// This ensures we get the current accurate data instead of potentially stale pre-calculated values
	challengeStats, err := con.etl.GetDB().GetChallengeStatisticsForBlockRange(ctx, db.GetChallengeStatisticsForBlockRangeParams{
		Height:   rollupInfo.BlockStart,
		Height_2: rollupInfo.BlockEnd,
	})
	if err != nil {
		con.logger.Warn("Failed to get challenge statistics", "rollupID", rollupID, "error", err)
		challengeStats = []db.GetChallengeStatisticsForBlockRangeRow{}
	}

	// Create a map for quick lookup of challenge statistics by address
	challengeStatsMap := make(map[string]db.GetChallengeStatisticsForBlockRangeRow)
	for _, stat := range challengeStats {
		challengeStatsMap[stat.Address] = stat
	}

	// Build validator uptime info for each validator
	validators := make([]*pages.ValidatorUptimeInfo, 0, len(validatorsData))
	for i := range validatorsData {
		validator := &db.EtlValidator{
			ID:           validatorsData[i].ID,
			Address:      validatorsData[i].Address,
			Endpoint:     validatorsData[i].Endpoint,
			CometAddress: validatorsData[i].CometAddress,
			NodeType:     validatorsData[i].NodeType,
			Spid:         validatorsData[i].Spid,
			VotingPower:  validatorsData[i].VotingPower,
			Status:       validatorsData[i].Status,
			RegisteredAt: validatorsData[i].RegisteredAt,
			CreatedAt:    validatorsData[i].CreatedAt,
			UpdatedAt:    validatorsData[i].UpdatedAt,
		}

		// Create a full SLA report for this rollup with all the required fields
		var reportPointers []*db.EtlSlaNodeReport
		slaReport := &db.EtlSlaNodeReport{
			SlaRollupID:        int32(rollupID),
			Address:            validatorsData[i].CometAddress,
			NumBlocksProposed:  0, // Default to 0
			ChallengesReceived: 0, // Default to 0
			ChallengesFailed:   0, // Default to 0
			TxHash:             rollupInfo.TxHash,
			CreatedAt:          rollupInfo.CreatedAt,
			BlockHeight:        rollupInfo.BlockHeight,
		}

		// Override with actual data if validator has report data (for blocks proposed)
		if validatorsData[i].NumBlocksProposed.Valid {
			slaReport.NumBlocksProposed = validatorsData[i].NumBlocksProposed.Int32
		}

		// Use dynamically calculated challenge statistics instead of potentially stale pre-calculated values
		if stat, exists := challengeStatsMap[validatorsData[i].CometAddress]; exists {
			slaReport.ChallengesReceived = int32(stat.ChallengesReceived)
			slaReport.ChallengesFailed = int32(stat.ChallengesFailed)
		}

		reportPointers = []*db.EtlSlaNodeReport{slaReport}

		validators = append(validators, &pages.ValidatorUptimeInfo{
			Validator:     validator,
			RecentRollups: reportPointers,
		})
	}

	props := pages.ValidatorsUptimeByRollupProps{
		Validators: validators,
		RollupID:   int32(rollupID),
		RollupData: &rollupInfo,
	}

	p := pages.ValidatorsUptimeByRollup(props)
	return p.Render(ctx, c.Response().Writer)
}

func (con *Console) Rollups(c echo.Context) error {
	// Parse query parameters for pagination
	pageParam := c.QueryParam("page")
	countParam := c.QueryParam("count")

	page := int32(1) // default to page 1
	if pageParam != "" {
		if parsedPage, err := strconv.ParseInt(pageParam, 10, 32); err == nil && parsedPage > 0 {
			page = int32(parsedPage)
		}
	}

	count := int32(20) // default to 20 per page
	if countParam != "" {
		if parsedCount, err := strconv.ParseInt(countParam, 10, 32); err == nil && parsedCount > 0 && parsedCount <= 100 {
			count = int32(parsedCount)
		}
	}

	// Calculate offset from page number
	offset := (page - 1) * count

	ctx := c.Request().Context()

	// Get paginated SLA rollups
	rollupsData, err := con.etl.GetDB().GetSlaRollupsWithPagination(ctx, db.GetSlaRollupsWithPaginationParams{
		Limit:  count,
		Offset: offset,
	})
	if err != nil {
		con.logger.Warn("Failed to get SLA rollups", "error", err)
		rollupsData = []db.EtlSlaRollup{}
	}

	// Convert to pointers
	rollups := make([]*db.EtlSlaRollup, len(rollupsData))
	for i := range rollupsData {
		rollups[i] = &rollupsData[i]
	}

	// Calculate pagination state
	hasNext := len(rollupsData) == int(count)
	hasPrev := page > 1

	// TODO: Get actual total count from database
	totalCount := int64(len(rollupsData)) // Placeholder

	props := pages.RollupsProps{
		Rollups:          rollups,
		RollupValidators: []*db.EtlSlaNodeReport{}, // Not needed for rollups list view
		CurrentPage:      page,
		HasNext:          hasNext,
		HasPrev:          hasPrev,
		PageSize:         count,
		TotalCount:       totalCount,
	}

	p := pages.Rollups(props)
	return p.Render(ctx, c.Response().Writer)
}

func (con *Console) Blocks(c echo.Context) error {
	// Parse query parameters
	pageParam := c.QueryParam("page")
	countParam := c.QueryParam("count")

	page := int32(1) // default to page 1
	if pageParam != "" {
		if parsedPage, err := strconv.ParseInt(pageParam, 10, 32); err == nil && parsedPage > 0 {
			page = int32(parsedPage)
		}
	}

	count := int32(50) // default to 50 per page
	if countParam != "" {
		if parsedCount, err := strconv.ParseInt(countParam, 10, 32); err == nil && parsedCount > 0 && parsedCount <= 200 {
			count = int32(parsedCount)
		}
	}

	// Calculate offset from page number
	offset := (page - 1) * count

	// Get blocks from database
	blocksData, err := con.etl.GetDB().GetBlocksByPage(c.Request().Context(), db.GetBlocksByPageParams{
		Limit:  count,
		Offset: offset,
	})
	if err != nil {
		con.logger.Warn("Failed to get blocks", "error", err)
		blocksData = []db.EtlBlock{}
	}

	// Convert to pointers
	blocks := make([]*db.EtlBlock, len(blocksData))
	blockTransactions := make([]int32, len(blocksData))
	for i := range blocksData {
		blocks[i] = &blocksData[i]
		// Get transaction count for each block
		txCount, err := con.etl.GetDB().GetBlockTransactionCount(c.Request().Context(), blocksData[i].BlockHeight)
		if err != nil {
			con.logger.Warn("Failed to get transaction count for block", "height", blocksData[i].BlockHeight, "error", err)
			txCount = 0
		}
		blockTransactions[i] = int32(txCount)
	}

	// Calculate pagination state
	hasNext := len(blocks) == int(count) // Simple check - if we got the full limit, there might be more
	hasPrev := page > 1

	props := pages.BlocksProps{
		Blocks:            blocks,
		BlockTransactions: blockTransactions,
		CurrentPage:       page,
		HasNext:           hasNext,
		HasPrev:           hasPrev,
		PageSize:          count,
	}

	p := pages.Blocks(props)
	ctx := c.Request().Context()
	return p.Render(ctx, c.Response().Writer)
}

func (con *Console) Transactions(c echo.Context) error {
	// Parse query parameters
	pageParam := c.QueryParam("page")
	countParam := c.QueryParam("count")

	page := int32(1) // default to page 1
	if pageParam != "" {
		if parsedPage, err := strconv.ParseInt(pageParam, 10, 32); err == nil && parsedPage > 0 {
			page = int32(parsedPage)
		}
	}

	count := int32(50) // default to 50 per page
	if countParam != "" {
		if parsedCount, err := strconv.ParseInt(countParam, 10, 32); err == nil && parsedCount > 0 && parsedCount <= 200 {
			count = int32(parsedCount)
		}
	}

	// Calculate offset from page number
	offset := (page - 1) * count

	transactions, blockHeights, err := con.getTransactionsWithBlockHeights(c.Request().Context(), count, offset)
	if err != nil {
		return c.String(http.StatusInternalServerError, "Failed to get transactions")
	}

	// Calculate pagination state
	hasNext := len(transactions) == int(count) // Simple check - if we got the full limit, there might be more
	hasPrev := page > 1

	props := pages.TransactionsProps{
		Transactions: transactions,
		BlockHeights: blockHeights,
		CurrentPage:  page,
		HasNext:      hasNext,
		HasPrev:      hasPrev,
		PageSize:     count,
	}

	p := pages.Transactions(props)
	ctx := c.Request().Context()
	return p.Render(ctx, c.Response().Writer)
}

func (con *Console) Content(c echo.Context) error {
	p := pages.Content()
	ctx := c.Request().Context()
	return p.Render(ctx, c.Response().Writer)
}

func (con *Console) Block(c echo.Context) error {
	height, err := strconv.ParseInt(c.Param("height"), 10, 64)
	if err != nil {
		return c.String(http.StatusBadRequest, "Invalid block height")
	}

	ctx := c.Request().Context()

	// Get block by height
	block, err := con.etl.GetDB().GetBlockByHeight(ctx, height)
	if err != nil {
		return c.String(http.StatusNotFound, fmt.Sprintf("Block not found at height %d", height))
	}

	// Get transactions for this block
	// First get all transactions and filter by block height
	// This is not the most efficient but will work for now - TODO: add GetTransactionsByBlockHeight query
	transactionsData, err := con.etl.GetDB().GetTransactionsByPage(ctx, db.GetTransactionsByPageParams{
		Limit:  1000, // Get a large number to ensure we get all for this block
		Offset: 0,
	})
	if err != nil {
		con.logger.Warn("Failed to get transactions", "error", err)
		transactionsData = []db.EtlTransaction{}
	}

	// Filter transactions for this specific block height
	var blockTransactions []*db.EtlTransaction
	for i := range transactionsData {
		if transactionsData[i].BlockHeight == height {
			blockTransactions = append(blockTransactions, &transactionsData[i])
		}
	}

	// Create block props
	props := pages.BlockProps{
		Block:        &block,
		Transactions: blockTransactions,
	}

	p := pages.Block(props)
	return p.Render(ctx, c.Response().Writer)
}

func (con *Console) Transaction(c echo.Context) error {
	txHash := c.Param("hash")
	if txHash == "" {
		return c.String(http.StatusBadRequest, "Transaction hash required")
	}

	ctx := c.Request().Context()

	// Get transaction by hash
	transaction, err := con.etl.GetDB().GetTransactionByHash(ctx, txHash)
	if err != nil {
		return c.String(http.StatusNotFound, fmt.Sprintf("Transaction not found: %s", txHash))
	}

	// Get block info for this transaction
	block, err := con.etl.GetDB().GetBlockByHeight(ctx, transaction.BlockHeight)
	if err != nil {
		con.logger.Warn("Failed to get block for transaction", "blockHeight", transaction.BlockHeight, "error", err)
		return c.String(http.StatusNotFound, fmt.Sprintf("Block not found at height %d", transaction.BlockHeight))
	}

	// Fetch transaction content based on type
	var content interface{}
	switch transaction.TxType {
	case "play":
		plays, err := con.etl.GetDB().GetPlaysByTxHash(ctx, txHash)
		if err != nil {
			con.logger.Warn("Failed to get plays for transaction", "txHash", txHash, "error", err)
		} else if len(plays) > 0 {
			// Convert to pointers for template
			playPointers := make([]*db.EtlPlay, len(plays))
			for i := range plays {
				playPointers[i] = &plays[i]
			}
			content = playPointers
		}

	case "manage_entity":
		entity, err := con.etl.GetDB().GetManageEntityByTxHash(ctx, txHash)
		if err != nil {
			con.logger.Warn("Failed to get manage entity for transaction", "txHash", txHash, "error", err)
		} else {
			content = &entity
		}

	case "validator_registration":
		registration, err := con.etl.GetDB().GetValidatorRegistrationByTxHash(ctx, txHash)
		if err != nil {
			con.logger.Warn("Failed to get validator registration for transaction", "txHash", txHash, "error", err)
		} else {
			content = &registration
		}

	case "validator_deregistration":
		deregistration, err := con.etl.GetDB().GetValidatorDeregistrationByTxHash(ctx, txHash)
		if err != nil {
			con.logger.Warn("Failed to get validator deregistration for transaction", "txHash", txHash, "error", err)
		} else {
			content = &deregistration
		}

	case "sla_rollup":
		slaRollup, err := con.etl.GetDB().GetSlaRollupByTxHash(ctx, txHash)
		if err != nil {
			con.logger.Warn("Failed to get SLA rollup for transaction", "txHash", txHash, "error", err)
		} else {
			content = &slaRollup
		}

	case "storage_proof":
		storageProof, err := con.etl.GetDB().GetStorageProofByTxHash(ctx, txHash)
		if err != nil {
			con.logger.Warn("Failed to get storage proof for transaction", "txHash", txHash, "error", err)
		} else {
			content = &storageProof
		}

	case "storage_proof_verification":
		storageProofVerification, err := con.etl.GetDB().GetStorageProofVerificationByTxHash(ctx, txHash)
		if err != nil {
			con.logger.Warn("Failed to get storage proof verification for transaction", "txHash", txHash, "error", err)
		} else {
			content = &storageProofVerification
		}
	}

	// Create transaction props
	props := pages.TransactionProps{
		Transaction: &transaction,
		Proposer:    block.ProposerAddress,
		Content:     content,
	}

	p := pages.Transaction(props)
	return p.Render(ctx, c.Response().Writer)
}

func (con *Console) Account(c echo.Context) error {
	address := c.Param("address")
	if address == "" {
		return c.String(http.StatusBadRequest, "Address parameter is required")
	}

	isEthAddress := ethcommon.IsHexAddress(address)
	if !isEthAddress {
		// assume handle and query audius api
		res, err := http.Get(fmt.Sprintf("https://api.audius.co/v1/users/handle/%s", address))
		if err != nil {
			return c.String(http.StatusBadRequest, "Invalid address")
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			return c.String(http.StatusBadRequest, "Invalid address")
		}

		type audiusUser struct {
			Wallet string `json:"wallet"`
		}

		type audiusResponse struct {
			Data audiusUser `json:"data"`
		}

		var response audiusResponse
		err = json.Unmarshal(body, &response)
		if err != nil {
			return c.String(http.StatusBadRequest, "Invalid address")
		}
		address = response.Data.Wallet

		return c.Redirect(http.StatusTemporaryRedirect, fmt.Sprintf("/account/%s", address))
	}

	// Parse query parameters
	pageParam := c.QueryParam("page")
	countParam := c.QueryParam("count")
	relationFilter := c.QueryParam("relation")
	startDate := c.QueryParam("start_date")
	endDate := c.QueryParam("end_date")

	page := int32(1) // default to page 1
	if pageParam != "" {
		if parsedPage, err := strconv.ParseInt(pageParam, 10, 32); err == nil && parsedPage > 0 {
			page = int32(parsedPage)
		}
	}

	count := int32(50) // default to 50 per page
	if countParam != "" {
		if parsedCount, err := strconv.ParseInt(countParam, 10, 32); err == nil && parsedCount > 0 && parsedCount <= 200 {
			count = int32(parsedCount)
		}
	}

	// Calculate offset from page number
	offset := (page - 1) * count

	ctx := c.Request().Context()
	etlDB := con.etl.GetDB()

	// Parse date filters
	var startTimestamp, endTimestamp pgtype.Timestamp
	if startDate != "" {
		if t, err := time.Parse("2006-01-02", startDate); err == nil {
			startTimestamp = pgtype.Timestamp{Time: t, Valid: true}
		}
	}
	if endDate != "" {
		if t, err := time.Parse("2006-01-02", endDate); err == nil {
			// Add 24 hours to include the entire end date
			endTimestamp = pgtype.Timestamp{Time: t.Add(24 * time.Hour), Valid: true}
		}
	}

	// Get transactions for this address
	transactionRows, err := etlDB.GetTransactionsByAddress(ctx, db.GetTransactionsByAddressParams{
		Lower:   address,
		Column2: relationFilter, // empty string means all relations
		Column3: startTimestamp,
		Column4: endTimestamp,
		Limit:   count,
		Offset:  offset,
	})
	if err != nil {
		con.logger.Error("Failed to get transactions for address", "address", address, "error", err)
		return c.String(http.StatusInternalServerError, "Failed to get transactions")
	}

	// Get total count for pagination
	totalCount, err := etlDB.GetTransactionCountByAddress(ctx, db.GetTransactionCountByAddressParams{
		Lower:   address,
		Column2: relationFilter,
		Column3: startTimestamp,
		Column4: endTimestamp,
	})
	if err != nil {
		con.logger.Error("Failed to get transaction count for address", "address", address, "error", err)
		return c.String(http.StatusInternalServerError, "Failed to get transaction count")
	}

	// Get available relation types for filter dropdown
	relationTypesRaw, err := etlDB.GetRelationTypesByAddress(ctx, address)
	if err != nil {
		con.logger.Error("Failed to get relation types for address", "address", address, "error", err)
		// Don't fail the request, just log the error
		relationTypesRaw = []interface{}{}
	}

	// Convert interface{} slice to string slice
	relationTypes := make([]string, len(relationTypesRaw))
	for i, rt := range relationTypesRaw {
		if str, ok := rt.(string); ok {
			relationTypes[i] = str
		} else {
			relationTypes[i] = fmt.Sprintf("%v", rt)
		}
	}

	// Convert transaction rows to transactions and extract relations
	transactions := make([]*db.EtlTransaction, len(transactionRows))
	txRelations := make([]string, len(transactionRows))
	for i, row := range transactionRows {
		transactions[i] = &db.EtlTransaction{
			ID:          row.ID,
			TxHash:      row.TxHash,
			BlockHeight: row.BlockHeight,
			TxIndex:     row.TxIndex,
			TxType:      row.TxType,
			CreatedAt:   row.CreatedAt,
		}
		// Handle relation type assertion
		if str, ok := row.Relation.(string); ok {
			txRelations[i] = str
		} else {
			txRelations[i] = fmt.Sprintf("%v", row.Relation)
		}
	}

	// Calculate pagination state
	hasNext := int64(offset+count) < totalCount
	hasPrev := page > 1

	props := pages.AccountProps{
		Address:       address,
		Transactions:  transactions,
		TxRelations:   txRelations,
		CurrentPage:   page,
		HasNext:       hasNext,
		HasPrev:       hasPrev,
		PageSize:      count,
		RelationTypes: relationTypes,
		CurrentFilter: relationFilter,
		StartDate:     startDate,
		EndDate:       endDate,
	}

	p := pages.Account(props)
	ctx = c.Request().Context()
	return p.Render(ctx, c.Response().Writer)
}

func (con *Console) stubRoute(c echo.Context) error {
	return c.String(http.StatusOK, "Hello, World!")
}

// HTMX Fragment Handlers
func (con *Console) StatsHeaderFragment(c echo.Context) error {
	ctx := c.Request().Context()

	// Get latest indexed block
	latestBlockHeight, err := con.etl.GetDB().GetLatestIndexedBlock(ctx)
	if err != nil {
		con.logger.Warn("Failed to get latest block height", "error", err)
		latestBlockHeight = 0
	}

	// Get latest trusted block height from the trusted node
	trustedBlockHeight := int64(con.latestTrustedBlock.Load())

	// Calculate sync status - consider synced if within 60 blocks of the head
	const syncThreshold = 60
	var isSyncing bool
	var blockDelta int64
	syncProgressPercentage := float64(100) // Default to fully synced

	if trustedBlockHeight > 0 && latestBlockHeight > 0 {
		blockDelta = trustedBlockHeight - latestBlockHeight
		isSyncing = blockDelta > syncThreshold

		if isSyncing && trustedBlockHeight > 0 {
			syncProgressPercentage = (float64(latestBlockHeight) / float64(trustedBlockHeight)) * 100
			// Ensure percentage doesn't exceed 100
			if syncProgressPercentage > 100 {
				syncProgressPercentage = 100
			}
		} else {
			syncProgressPercentage = 100 // Fully synced
		}
	} else {
		// If we don't have trusted block info, assume synced
		isSyncing = false
		blockDelta = 0
		syncProgressPercentage = 100
	}

	// Get latest SLA rollup for BPS/TPS data
	var bps float64 = 0
	var avgBlockTime float32 = 0
	latestSlaRollup, err := con.etl.GetDB().GetLatestSlaRollup(ctx)
	if err != nil {
		con.logger.Debug("Failed to get latest SLA rollup", "error", err)
		// Fall back to default values
		bps = 0.5
		avgBlockTime = 2.0
	} else {
		bps = latestSlaRollup.Bps
		// Calculate average block time from BPS (if BPS > 0)
		if bps > 0 {
			avgBlockTime = float32(1.0 / bps)
		} else {
			avgBlockTime = 2.0 // Default 2 seconds
		}
	}

	// Get active validator count
	validatorCount, err := con.etl.GetDB().GetActiveValidatorCount(ctx)
	if err != nil {
		con.logger.Warn("Failed to get validator count", "error", err)
		validatorCount = 0
	}

	stats := &pages.DashboardStats{
		CurrentBlockHeight:  latestBlockHeight,
		ChainID:             con.etl.ChainID,
		BPS:                 bps,
		ValidatorCount:      validatorCount,
		AvgBlockTime:        avgBlockTime,
		IsSyncing:           isSyncing,
		LatestIndexedHeight: latestBlockHeight,
		LatestChainHeight:   trustedBlockHeight,
		BlockDelta:          blockDelta,
	}

	// Render the stats header fragment template
	fragment := pages.StatsHeaderFragment(stats, syncProgressPercentage)
	return fragment.Render(ctx, c.Response().Writer)
}

func (con *Console) NetworkSidebarFragment(c echo.Context) error {
	// TODO: Implement network sidebar fragment using database queries
	return c.String(http.StatusNotImplemented, "TODO: Implement network sidebar fragment")
}

func (con *Console) TPSFragment(c echo.Context) error {
	ctx := c.Request().Context()

	// Get latest SLA rollup for TPS data
	var tps float64 = 0
	latestSlaRollup, err := con.etl.GetDB().GetLatestSlaRollup(ctx)
	if err != nil {
		con.logger.Debug("Failed to get latest SLA rollup", "error", err)
		// Fall back to default value
		tps = 0.1
	} else {
		tps = latestSlaRollup.Tps
	}

	// Get dashboard transaction stats from materialized view
	txStats, err := con.etl.GetDB().GetDashboardTransactionStats(ctx)
	if err != nil {
		con.logger.Warn("Failed to get dashboard transaction stats", "error", err)
		txStats = db.MvDashboardTransactionStat{}
	}

	stats := &pages.DashboardStats{
		TPS:                  tps,
		TotalTransactions30d: txStats.Transactions30d,
	}

	// Render the TPS fragment template
	fragment := pages.TPSFragment(stats)
	return fragment.Render(ctx, c.Response().Writer)
}

func (con *Console) TotalTransactionsFragment(c echo.Context) error {
	ctx := c.Request().Context()

	// Get dashboard transaction stats from materialized view
	txStats, err := con.etl.GetDB().GetDashboardTransactionStats(ctx)
	if err != nil {
		con.logger.Warn("Failed to get dashboard transaction stats", "error", err)
		txStats = db.MvDashboardTransactionStat{}
	}

	stats := &pages.DashboardStats{
		TotalTransactions:            txStats.TotalTransactions,
		TotalTransactions24h:         txStats.Transactions24h,
		TotalTransactionsPrevious24h: txStats.TransactionsPrevious24h,
	}

	// Render the total transactions fragment template
	fragment := pages.TotalTransactionsFragment(stats)
	return fragment.Render(ctx, c.Response().Writer)
}

type SSEEvent struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

const sseConnectionTTL = 1 * time.Minute

func (con *Console) LiveEventsSSE(c echo.Context) error {
	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")
	c.Response().WriteHeader(http.StatusOK)

	flusher, ok := c.Response().Writer.(http.Flusher)
	if !ok {
		return nil
	}

	flusher.Flush()

	// Subscribe to both block and play events from ETL pubsub
	blockCh := con.etl.GetBlockPubsub().Subscribe(etl.BlockTopic, 10)
	playCh := con.etl.GetPlayPubsub().Subscribe(etl.PlayTopic, 10)

	// Ensure cleanup on connection close
	defer func() {
		con.etl.GetBlockPubsub().Unsubscribe(etl.BlockTopic, blockCh)
		con.etl.GetPlayPubsub().Unsubscribe(etl.PlayTopic, playCh)
	}()

	// Throttle state for block events
	var (
		latestBlock    *db.EtlBlock
		lastSentHeight int64
		blockTicker    = time.NewTicker(1 * time.Second)
	)
	defer blockTicker.Stop()

	flusher.Flush()

	timeout := time.After(sseConnectionTTL)

	for {
		select {
		case <-c.Request().Context().Done():
			return nil

		case <-timeout:
			return nil

		case blockEvent := <-blockCh:
			if blockEvent != nil {
				latestBlock = blockEvent
			}

		case <-blockTicker.C:
			if latestBlock != nil && latestBlock.BlockHeight > lastSentHeight {
				// Send block event
				blockEvent := SSEEvent{
					Event: "block",
					Data: map[string]interface{}{
						"height":   latestBlock.BlockHeight,
						"proposer": latestBlock.ProposerAddress,
						"time":     latestBlock.BlockTime.Time.Format(time.RFC3339),
					},
				}
				eventData, _ := json.Marshal(blockEvent)
				fmt.Fprintf(c.Response(), "data: %s\n\n", string(eventData))
				lastSentHeight = latestBlock.BlockHeight
				flusher.Flush()
			}

		case play := <-playCh:
			if play != nil {
				// Get coordinates for the play location
				if play.City != "" && play.Region != "" && play.Country != "" {
					if latLong, err := con.etl.GetLocationDB().GetLatLong(c.Request().Context(), play.City, play.Region, play.Country); err == nil {
						lat := latLong.Latitude
						lng := latLong.Longitude
						// Send play event with coordinates
						playEvent := SSEEvent{
							Event: "play",
							Data: map[string]interface{}{
								"lat":       lat,
								"lng":       lng,
								"timestamp": time.Now().Format(time.RFC3339),
								"duration":  5, // Default 5 seconds for animation
							},
						}
						eventData, _ := json.Marshal(playEvent)
						fmt.Fprintf(c.Response(), "data: %s\n\n", string(eventData))
						flusher.Flush()
					}
				}
			}

		}
	}
}

func (con *Console) Search(c echo.Context) error {
	query := c.QueryParam("q")
	if query == "" {
		return c.JSON(http.StatusOK, map[string]interface{}{
			"results": []interface{}{},
		})
	}

	// TODO: Implement search using database queries
	return c.JSON(http.StatusOK, map[string]interface{}{
		"results": []interface{}{},
	})
}

// refreshTrustedBlock refreshes the trusted block height every 10 seconds
func (con *Console) refreshTrustedBlock() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		info, err := con.trustedNode.Core.GetNodeInfo(context.Background(), &connect.Request[corev1.GetNodeInfoRequest]{})
		if err != nil {
			con.logger.Warn("Failed to refresh node info", "error", err)
			// Use the cached value if refresh fails
		} else {
			con.latestTrustedBlock.Store(info.Msg.CurrentHeight)
			con.lastRefreshTime.Store(time.Now().Unix())
		}
	}
}
