package server

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "embed"
	_ "net/http/pprof"

	"github.com/AudiusProject/audiusd/pkg/common"
	coreServer "github.com/AudiusProject/audiusd/pkg/core/server"
	audiusHttputil "github.com/AudiusProject/audiusd/pkg/httputil"
	"github.com/AudiusProject/audiusd/pkg/lifecycle"
	"github.com/AudiusProject/audiusd/pkg/mediorum/cidutil"
	"github.com/AudiusProject/audiusd/pkg/mediorum/crudr"
	"github.com/AudiusProject/audiusd/pkg/mediorum/ethcontracts"
	"github.com/AudiusProject/audiusd/pkg/mediorum/persistence"
	"github.com/AudiusProject/audiusd/pkg/pos"
	"github.com/AudiusProject/audiusd/pkg/registrar"
	"github.com/AudiusProject/audiusd/pkg/version"
	"github.com/erni27/imcache"
	"github.com/imroc/req/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/oschwald/maxminddb-golang"
	"go.uber.org/zap"
	"gocloud.dev/blob"
	"golang.org/x/exp/slices"
	"golang.org/x/sync/errgroup"

	_ "gocloud.dev/blob/fileblob"
)

type MediorumConfig struct {
	Env                       string
	Self                      registrar.Peer
	Peers                     []registrar.Peer
	Signers                   []registrar.Peer
	ReplicationFactor         int
	Dir                       string `default:"/tmp/mediorum"`
	BlobStoreDSN              string `json:"-"`
	MoveFromBlobStoreDSN      string `json:"-"`
	PostgresDSN               string `json:"-"`
	PrivateKey                string `json:"-"`
	ListenPort                string
	TrustedNotifierID         int
	SPID                      int
	SPOwnerWallet             string
	GitSHA                    string
	AudiusDockerCompose       string
	AutoUpgradeEnabled        bool
	WalletIsRegistered        bool
	StoreAll                  bool
	VersionJson               version.VersionJson
	DiscoveryListensEndpoints []string
	LogLevel                  string

	ProgrammableDistributionEnabled bool

	// should have a basedir type of thing
	// by default will put db + blobs there

	privateKey *ecdsa.PrivateKey
}

type MediorumServer struct {
	lc               *lifecycle.Lifecycle
	echo             *echo.Echo
	bucket           *blob.Bucket
	logger           *zap.Logger
	crud             *crudr.Crudr
	pgPool           *pgxpool.Pool
	quit             chan error
	trustedNotifier  *ethcontracts.NotifierInfo
	reqClient        *req.Client
	rendezvousHasher *RendezvousHasher
	transcodeWork    chan *Upload
	g                registrar.PeerProvider

	// stats
	statsMutex       sync.RWMutex
	transcodeStats   *TranscodeStats
	mediorumPathUsed uint64
	mediorumPathSize uint64
	mediorumPathFree uint64

	databaseSize          uint64
	dbSizeErr             string
	lastSuccessfulRepair  RepairTracker
	lastSuccessfulCleanup RepairTracker

	uploadsCount    int64
	uploadsCountErr string

	isSeeding        bool
	isAudiusdManaged bool

	peerHealthsMutex      sync.RWMutex
	peerHealths           map[string]*PeerHealth
	unreachablePeers      []string
	redirectCache         *imcache.Cache[string, string]
	uploadOrigCidCache    *imcache.Cache[string, string]
	imageCache            *imcache.Cache[string, []byte]
	failsPeerReachability bool

	StartedAt time.Time
	Config    MediorumConfig

	crudSweepMutex sync.Mutex

	// handle communication between core and mediorum for Proof of Storage
	posChannel chan pos.PoSRequest

	core *coreServer.CoreService

	geoIPdb      *maxminddb.Reader
	geoIPdbReady chan struct{}

	playEventQueue *PlayEventQueue
}

type PeerHealth struct {
	Version        string               `json:"version"`
	LastReachable  time.Time            `json:"lastReachable"`
	LastHealthy    time.Time            `json:"lastHealthy"`
	ReachablePeers map[string]time.Time `json:"reachablePeers"`
}

var (
	apiBasePath = ""
)

const PercentSeededThreshold = 50

func New(lc *lifecycle.Lifecycle, logger *zap.Logger, config MediorumConfig, provider registrar.PeerProvider, posChannel chan pos.PoSRequest, core *coreServer.CoreService) (*MediorumServer, error) {
	if env := os.Getenv("MEDIORUM_ENV"); env != "" {
		config.Env = env
	}
	config.ProgrammableDistributionEnabled = common.IsProgrammableDistributionEnabled(config.Env)

	var isAudiusdManaged bool
	if audiusdGenerated := os.Getenv("AUDIUS_D_GENERATED"); audiusdGenerated != "" {
		isAudiusdManaged = true
	}

	if config.VersionJson == (version.VersionJson{}) {
		return nil, errors.New(".version.json is required to be bundled with the mediorum binary")
	}

	// validate host config
	if config.Self.Host == "" {
		return nil, errors.New("host is required")
	} else if hostUrl, err := url.Parse(config.Self.Host); err != nil {
		return nil, fmt.Errorf("invalid host: %v", err)
	} else if config.ListenPort == "" {
		config.ListenPort = hostUrl.Port()
	}

	if config.Dir == "" {
		config.Dir = "/tmp/mediorum"
	}

	if config.BlobStoreDSN == "" {
		config.BlobStoreDSN = "file://" + config.Dir + "/blobs?no_tmp_dir=true"
	}

	if pk, err := ethcontracts.ParsePrivateKeyHex(config.PrivateKey); err != nil {
		log.Println("invalid private key: ", err)
	} else {
		config.privateKey = pk
	}

	// check that we're registered...
	for _, peer := range config.Peers {
		if strings.EqualFold(config.Self.Wallet, peer.Wallet) && strings.EqualFold(config.Self.Host, peer.Host) {
			config.WalletIsRegistered = true
			break
		}
	}

	logger.Info("storage server starting")

	if config.discoveryListensEnabled() {
		logger.Info("discovery listens enabled")
	}

	// ensure dir
	if err := os.MkdirAll(config.Dir, os.ModePerm); err != nil {
		logger.Error("failed to create local persistent storage dir", zap.Error(err))
	}

	// bucket
	bucket, err := persistence.Open(config.BlobStoreDSN)
	if err != nil {
		logger.Error("failed to open persistent storage bucket", zap.Error(err))
		return nil, err
	}

	// bucket to move all files from
	if config.MoveFromBlobStoreDSN != "" {
		if config.MoveFromBlobStoreDSN == config.BlobStoreDSN {
			logger.Error("AUDIUS_STORAGE_DRIVER_URL_MOVE_FROM cannot be the same as AUDIUS_STORAGE_DRIVER_URL")
			return nil, err
		}
		bucketToMoveFrom, err := persistence.Open(config.MoveFromBlobStoreDSN)
		if err != nil {
			logger.Error("Failed to open bucket to move from. Ensure AUDIUS_STORAGE_DRIVER_URL and AUDIUS_STORAGE_DRIVER_URL_MOVE_FROM are set (the latter can be empty if not moving data)", zap.Error(err))
			return nil, err
		}

		logger.Info(fmt.Sprintf("Moving all files from %s to %s. This may take a few hours...", config.MoveFromBlobStoreDSN, config.BlobStoreDSN))
		err = persistence.MoveAllFiles(bucketToMoveFrom, bucket)
		if err != nil {
			logger.Error("Failed to move files. Ensure AUDIUS_STORAGE_DRIVER_URL and AUDIUS_STORAGE_DRIVER_URL_MOVE_FROM are set (the latter can be empty if not moving data)", zap.Error(err))
			return nil, err
		}

		logger.Info("Finished moving files between buckets. Please remove AUDIUS_STORAGE_DRIVER_URL_MOVE_FROM from your environment and restart the server.")
	}

	// db
	db := dbMustDial(config.PostgresDSN)
	if config.Env == "dev" {
		// air doesn't reset client connections so this explicitly sets the client encoding
		sqlDB, err := db.DB()
		if err == nil {
			_, err = sqlDB.Exec("SET client_encoding TO 'UTF8';")
			if err != nil {
				return nil, fmt.Errorf("Failed to set client encoding: %v", err)
			}
		}
	}

	// pg pool
	// config.PostgresDSN
	pgConfig, _ := pgxpool.ParseConfig(config.PostgresDSN)
	pgPool, err := pgxpool.NewWithConfig(context.Background(), pgConfig)
	if err != nil {
		logger.Error("dial postgres failed", zap.Error(err))
	}

	// lifecycle
	mediorumLifecycle := lifecycle.NewFromLifecycle(lc, "mediorum")

	// crud
	peerHosts := []string{}
	allHosts := []string{}
	for _, peer := range config.Peers {
		allHosts = append(allHosts, peer.Host)
		if peer.Host != config.Self.Host {
			peerHosts = append(peerHosts, peer.Host)
		}
	}

	crud := crudr.New(config.Self.Host, config.privateKey, peerHosts, db, mediorumLifecycle, logger)
	dbMigrate(crud, config.Self.Host)

	rendezvousHasher := NewRendezvousHasher(allHosts)

	// req.cool http client
	reqClient := req.C().
		SetUserAgent("mediorum " + config.Self.Host).
		SetTimeout(5 * time.Second)

	// Read trusted notifier endpoint from chain
	var trustedNotifier ethcontracts.NotifierInfo
	if config.TrustedNotifierID > 0 {
		trustedNotifier, err = ethcontracts.GetNotifierForID(strconv.Itoa(config.TrustedNotifierID), config.Self.Wallet)
		if err == nil {
			logger.Info("got trusted notifier from chain", zap.String("endpoint", trustedNotifier.Endpoint), zap.String("wallet", trustedNotifier.Wallet))
		} else {
			logger.Error("failed to get trusted notifier from chain, not polling delist statuses", zap.Error(err))
		}
	} else {
		logger.Warn("trusted notifier id not set, not polling delist statuses or serving /contact route")
	}

	// echoServer server
	echoServer := echo.New()
	echoServer.HideBanner = true
	echoServer.Debug = true

	echoServer.Use(middleware.Recover())
	echoServer.Use(middleware.Logger())
	echoServer.Use(middleware.CORS())
	echoServer.Use(timingMiddleware)

	ss := &MediorumServer{
		lc:               mediorumLifecycle,
		echo:             echoServer,
		bucket:           bucket,
		crud:             crud,
		pgPool:           pgPool,
		reqClient:        reqClient,
		logger:           logger,
		quit:             make(chan error, 1),
		g:                provider,
		trustedNotifier:  &trustedNotifier,
		isSeeding:        config.Env == "stage" || config.Env == "prod",
		isAudiusdManaged: isAudiusdManaged,
		rendezvousHasher: rendezvousHasher,
		transcodeWork:    make(chan *Upload),
		posChannel:       posChannel,

		peerHealths:        map[string]*PeerHealth{},
		redirectCache:      imcache.New(imcache.WithMaxEntriesLimitOption[string, string](50_000, imcache.EvictionPolicyLRU)),
		uploadOrigCidCache: imcache.New(imcache.WithMaxEntriesLimitOption[string, string](50_000, imcache.EvictionPolicyLRU)),
		imageCache:         imcache.New(imcache.WithMaxEntriesLimitOption[string, []byte](10_000, imcache.EvictionPolicyLRU)),

		StartedAt:    time.Now().UTC(),
		Config:       config,
		geoIPdbReady: make(chan struct{}),

		core: core,

		playEventQueue: NewPlayEventQueue(),
	}

	routes := echoServer.Group(apiBasePath)

	routes.GET("", func(c echo.Context) error {
		return c.Redirect(http.StatusFound, "/dashboard/#/nodes/content-node?endpoint="+config.Self.Host)
	})
	routes.GET("/", func(c echo.Context) error {
		return c.Redirect(http.StatusFound, "/dashboard/#/nodes/content-node?endpoint="+config.Self.Host)
	})

	// public: uploads
	routes.GET("/uploads", ss.serveUploadList)
	routes.GET("/uploads/:id", ss.serveUploadDetail, ss.requireHealthy)
	routes.POST("/uploads/:id", ss.updateUpload, ss.requireHealthy, ss.requireUserSignature)
	routes.POST("/uploads", ss.postUpload, ss.requireHealthy)
	// workaround because reverse proxy catches the browser's preflight OPTIONS request instead of letting our CORS middleware handle it
	routes.OPTIONS("/uploads", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	routes.POST("/generate_preview/:cid/:previewStartSeconds", ss.generatePreview, ss.requireHealthy)

	// legacy blob audio analysis
	routes.GET("/tracks/legacy/:cid/analysis", ss.serveLegacyBlobAnalysis, ss.requireHealthy)

	// serve blob (audio)
	routes.HEAD("/ipfs/:cid", ss.serveBlob, ss.requireHealthy, ss.ensureNotDelisted)
	routes.GET("/ipfs/:cid", ss.serveBlob, ss.requireHealthy, ss.ensureNotDelisted)
	routes.HEAD("/content/:cid", ss.serveBlob, ss.requireHealthy, ss.ensureNotDelisted)
	routes.GET("/content/:cid", ss.serveBlob, ss.requireHealthy, ss.ensureNotDelisted)
	routes.HEAD("/tracks/cidstream/:cid", ss.serveBlob, ss.requireHealthy, ss.ensureNotDelisted, ss.requireRegisteredSignature)
	routes.GET("/tracks/cidstream/:cid", ss.serveBlob, ss.requireHealthy, ss.ensureNotDelisted, ss.requireRegisteredSignature)
	routes.GET("/tracks/stream/:trackId", ss.serveTrack)

	// serve image
	routes.HEAD("/ipfs/:jobID/:variant", ss.serveImage, ss.requireHealthy)
	routes.GET("/ipfs/:jobID/:variant", ss.serveImage, ss.requireHealthy)
	routes.HEAD("/content/:jobID/:variant", ss.serveImage, ss.requireHealthy)
	routes.GET("/content/:jobID/:variant", ss.serveImage, ss.requireHealthy)

	routes.GET("/contact", ss.serveContact)
	routes.GET("/health_check", ss.serveHealthCheck)
	routes.HEAD("/health_check", ss.serveHealthCheck)
	routes.GET("/ip_check", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{
			"data": c.RealIP(), // client/requestor IP
		})
	})

	routes.GET("/delist_status/track/:trackCid", ss.serveTrackDelistStatus)
	routes.GET("/delist_status/user/:userId", ss.serveUserDelistStatus)
	routes.POST("/delist_status/insert", ss.serveInsertDelistStatus, ss.requireBodySignedByOwner)

	// -------------------
	// reverse proxy /d and /d_api to uptime container
	uptimeUrl, err := url.Parse("http://uptime:1996")
	if err != nil {
		return nil, fmt.Errorf("Invalid uptime URL: %v", err)
	}
	uptimeProxy := httputil.NewSingleHostReverseProxy(uptimeUrl)

	uptimeAPI := routes.Group("/d_api")
	// fixes what I think should be considered an echo bug: https://github.com/labstack/echo/issues/1419
	uptimeAPI.Use(ACAOHeaderOverwriteMiddleware)
	uptimeAPI.Any("/*", echo.WrapHandler(uptimeProxy))

	uptimeUI := routes.Group("/d")
	uptimeUI.Any("*", echo.WrapHandler(uptimeProxy))

	// -------------------
	// internal
	internalApi := routes.Group("/internal")

	// internal: crud
	internalApi.GET("/crud/sweep", ss.serveCrudSweep)
	internalApi.POST("/crud/push", ss.serveCrudPush, middleware.BasicAuth(ss.checkBasicAuth))

	internalApi.GET("/blobs/location/:cid", ss.serveBlobLocation, cidutil.UnescapeCidParam)
	internalApi.GET("/blobs/info/:cid", ss.serveBlobInfo, cidutil.UnescapeCidParam)

	// internal: blobs between peers
	internalApi.GET("/blobs/:cid", ss.serveInternalBlobGET, cidutil.UnescapeCidParam, middleware.BasicAuth(ss.checkBasicAuth))
	internalApi.POST("/blobs", ss.serveInternalBlobPOST, middleware.BasicAuth(ss.checkBasicAuth))
	internalApi.GET("/qm.csv", ss.serveInternalQmCsv)

	// WIP internal: metrics
	internalApi.GET("/metrics", ss.getMetrics)
	internalApi.GET("/metrics/blobs-served/:timeRange", ss.getBlobsServedMetrics)
	internalApi.GET("/logs/partition-ops", ss.getPartitionOpsLog)
	internalApi.GET("/logs/reaper", ss.getReaperLog)
	internalApi.GET("/logs/repair", ss.serveRepairLog)
	internalApi.GET("/logs/storageAndDb", ss.serveStorageAndDbLogs)
	internalApi.GET("/logs/pg-upgrade", ss.getPgUpgradeLog)

	// internal: testing
	internalApi.GET("/proxy_health_check", ss.proxyHealthCheck)

	go ss.loadGeoIPDatabase()

	return ss, nil

}

func setResponseACAOHeaderFromRequest(req http.Request, resp echo.Response) {
	resp.Header().Set(
		echo.HeaderAccessControlAllowOrigin,
		req.Header.Get(echo.HeaderOrigin),
	)
}

func ACAOHeaderOverwriteMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		ctx.Response().Before(func() {
			setResponseACAOHeaderFromRequest(*ctx.Request(), *ctx.Response())
		})
		return next(ctx)
	}
}

func timingMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		startTime := time.Now()
		c.Set("startTime", startTime)
		c.Response().Before(func() {
			c.Response().Header().Set("x-took", time.Since(startTime).String())
		})
		return next(c)
	}
}

// Calling echo response functions (c.JSON or c.String)
// will automatically set timing header in timingMiddleware.
// But for places where we do http.ServeContent
// we have to manually call setTimingHeader right before writing response.
func setTimingHeader(c echo.Context) {
	if startTime, ok := c.Get("startTime").(time.Time); ok {
		c.Response().Header().Set("x-took", time.Since(startTime).String())
	}
}

func (ss *MediorumServer) MustStart() error {
	ss.lc.AddManagedRoutine("pprof server", ss.startPprofServer)
	ss.lc.AddManagedRoutine("echo server", ss.startEchoServer)
	ss.lc.AddManagedRoutine("transcoder", ss.startTranscoder)
	ss.lc.AddManagedRoutine("audio analyzer", ss.startAudioAnalyzer)

	if ss.Config.StoreAll {
		ss.lc.AddManagedRoutine("fix truncated qm worker", ss.startFixTruncatedQmWorker)
	}

	zeroTime := time.Time{}
	var lastSuccessfulRepair RepairTracker
	err := ss.crud.DB.
		Where("finished_at is not null and finished_at != ? and aborted_reason = ?", zeroTime, "").
		Order("started_at desc").
		First(&lastSuccessfulRepair).Error
	if err != nil {
		lastSuccessfulRepair = RepairTracker{Counters: map[string]int{}}
	}
	ss.lastSuccessfulRepair = lastSuccessfulRepair

	var lastSuccessfulCleanup RepairTracker
	err = ss.crud.DB.
		Where("finished_at is not null and finished_at != ? and aborted_reason = ? and cleanup_mode = true", zeroTime, "").
		Order("started_at desc").
		First(&lastSuccessfulCleanup).Error
	if err != nil {
		lastSuccessfulCleanup = RepairTracker{Counters: map[string]int{}}
	}
	ss.lastSuccessfulCleanup = lastSuccessfulCleanup

	// for any background task that make authenticated peer requests
	// only start if we have a valid registered wallet
	if ss.Config.WalletIsRegistered {
		ss.crud.StartClients()

		ss.lc.AddManagedRoutine("health poller", ss.startHealthPoller)
		ss.lc.AddManagedRoutine("repairer", ss.startRepairer)
		ss.lc.AddManagedRoutine("qm syncer", ss.startQmSyncer)
		ss.lc.AddManagedRoutine("delist status poller", ss.startPollingDelistStatuses)
		ss.lc.AddManagedRoutine("seeding completion poller", ss.pollForSeedingCompletion)
		ss.lc.AddManagedRoutine("upload scroller", ss.startUploadScroller)
		ss.lc.AddManagedRoutine("play event queue", ss.startPlayEventQueue)
		ss.lc.AddManagedRoutine("zap syncer", func(ctx context.Context) error {
			ticker := time.NewTicker(10 * time.Second)
			for {
				select {
				case <-ticker.C:
					ss.logger.Sync()
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		})

	} else {
		ss.lc.AddManagedRoutine("registration warner", func(ctx context.Context) error {
			ticker := time.NewTicker(10 * time.Second)
			for {
				select {
				case <-ticker.C:
					ss.logger.Warn("node not fully running yet - please register at https://dashboard.audius.org and restart the server")
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		})
	}

	ss.lc.AddManagedRoutine("metrics monitor", ss.monitorMetrics)
	ss.lc.AddManagedRoutine("peer reachability monitor", ss.monitorPeerReachability)
	ss.lc.AddManagedRoutine("proof of storage handler", ss.startPoSHandler)
	ss.lc.AddManagedRoutine("peer refresher", ss.refreshPeersAndSigners)

	return <-ss.quit
}

func (ss *MediorumServer) Stop() {
	ss.logger.Info("stopping")
	if err := ss.lc.ShutdownWithTimeout(time.Minute); err != nil {
		panic("could not shutdown gracefully, timed out")
	}

	if db, err := ss.crud.DB.DB(); err == nil {
		if err := db.Close(); err != nil {
			ss.logger.Error("db shutdown", zap.Error(err))
		}
	}
	ss.logger.Info("bye")
	ss.quit <- errors.New("mediorum stopped")
}

func (ss *MediorumServer) pollForSeedingCompletion(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Second)
	for {
		select {
		case <-ticker.C:
			if ss.crud.GetPercentNodesSeeded() > PercentSeededThreshold {
				ss.isSeeding = false
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// discovery listens are enabled if endpoints are provided
func (mc *MediorumConfig) discoveryListensEnabled() bool {
	return len(mc.DiscoveryListensEndpoints) > 0
}

func (ss *MediorumServer) startEchoServer(ctx context.Context) error {
	done := make(chan error, 1)
	go func() {
		err := ss.echo.Start(":" + ss.Config.ListenPort)
		if err != nil && err != http.ErrServerClosed {
			ss.logger.Error("echo server error", zap.Error(err))
			done <- err
		} else {
			done <- nil
		}
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err := ss.echo.Shutdown(shutdownCtx)
		if err != nil {
			ss.logger.Error("failed to shutdown echo server", zap.Error(err))
			return err
		}
		return ctx.Err()
	}
}

func (ss *MediorumServer) startPprofServer(ctx context.Context) error {
	done := make(chan error, 1)
	srv := &http.Server{Addr: ":6060", Handler: nil}
	go func() {
		err := srv.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			ss.logger.Error("pprof server error", zap.Error(err))
			done <- err
		} else {
			done <- nil
		}
	}()
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			if err := srv.Shutdown(shutdownCtx); err != nil {
				ss.logger.Error("failed to shutdown pprof server", zap.Error(err))
				return err
			}
			return ctx.Err()
		}
	}
}

func (ss *MediorumServer) refreshPeersAndSigners(ctx context.Context) error {
	interval := 30 * time.Minute
	if os.Getenv("MEDIORUM_ENV") == "dev" {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	for {
		select {
		case <-ticker.C:
			var peers, signers []registrar.Peer
			var err error

			eg := new(errgroup.Group)
			eg.Go(func() error {
				peers, err = ss.g.Peers()
				return err
			})
			eg.Go(func() error {
				signers, err = ss.g.Signers()
				return err
			})
			if err := eg.Wait(); err != nil {
				ss.logger.Error("failed to fetch registered nodes", zap.Error(err))
				continue
			}

			var combined, configCombined []string

			for _, peer := range append(peers, signers...) {
				combined = append(combined, fmt.Sprintf("%s,%s", audiusHttputil.RemoveTrailingSlash(strings.ToLower(peer.Host)), strings.ToLower(peer.Wallet)))
			}

			for _, configPeer := range append(ss.Config.Peers, ss.Config.Signers...) {
				configCombined = append(configCombined, fmt.Sprintf("%s,%s", audiusHttputil.RemoveTrailingSlash(strings.ToLower(configPeer.Host)), strings.ToLower(configPeer.Wallet)))
			}

			slices.Sort(combined)
			slices.Sort(configCombined)
			if !slices.Equal(combined, configCombined) {
				ss.logger.Info("peers or signers changed on chain. restarting...", zap.Int("peers", len(peers)), zap.Int("signers", len(signers)), zap.Strings("combined", combined), zap.Strings("configCombined", configCombined))
				go ss.Stop()
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
