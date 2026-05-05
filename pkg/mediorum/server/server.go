package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
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

	"connectrpc.com/connect"
	ethv1 "github.com/OpenAudio/go-openaudio/pkg/api/eth/v1"
	ethv1connect "github.com/OpenAudio/go-openaudio/pkg/api/eth/v1/v1connect"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	coreServer "github.com/OpenAudio/go-openaudio/pkg/core/server"
	audiusHttputil "github.com/OpenAudio/go-openaudio/pkg/httputil"
	"github.com/OpenAudio/go-openaudio/pkg/lifecycle"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/crudr"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/ethcontracts"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/persistence"
	"github.com/OpenAudio/go-openaudio/pkg/pos"
	"github.com/OpenAudio/go-openaudio/pkg/registrar"
	"github.com/OpenAudio/go-openaudio/pkg/version"
	"github.com/erni27/imcache"
	"github.com/imroc/req/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/oschwald/maxminddb-golang"
	"go.uber.org/zap"
	"gocloud.dev/blob"

	_ "gocloud.dev/blob/fileblob"
)

// trackAccessInfo caches sound_recordings + management_keys lookup for cidstream auth
type trackAccessInfo struct {
	TrackID            string
	ManagementKeyCount int
	DurationSeconds    float64 // track duration in seconds (from FFProbe); 0 means unknown
}

type MediorumConfig struct {
	Env                       string
	Self                      registrar.Peer
	Peers                     []registrar.Peer
	Signers                   []registrar.Peer
	ReplicationFactor         int
	Dir                       string `default:"/tmp/mediorum"`
	BlobStoreDSN              string `json:"-"`
	ArchiveBlobStoreDSN       string `json:"-"`
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
	DeadHosts                 []string
	RepairEnabled              bool          `default:"true"`
	RepairInterval             time.Duration `default:"1h"`
	RepairQmCidsUseListIndex   bool

	ProgrammableDistributionEnabled bool
	BlobStorageStreaming             bool

	// should have a basedir type of thing
	// by default will put db + blobs there

	privateKey *ecdsa.PrivateKey
}


type MediorumServer struct {
	lc               *lifecycle.Lifecycle
	echo             *echo.Echo
	bucket           *blob.Bucket
	archiveBucket    *blob.Bucket
	logger           *zap.Logger
	crud             *crudr.Crudr
	pgPool           *pgxpool.Pool
	quit             chan error
	trustedNotifier  *ethcontracts.NotifierInfo
	reqClient        *req.Client
	peerHTTPClient   *http.Client // for outbound peer requests (replication, blob pull); uses InsecureSkipVerify in dev+self-signed
	rendezvousHasher *common.RendezvousHasher
	transcodeWork    chan *Upload
	replicationWork  chan *Upload
	ethService       ethv1connect.EthServiceHandler

	// stats
	statsMutex         sync.RWMutex
	transcodeStats     *TranscodeStats
	mediorumPathUsed   uint64
	mediorumPathSize   uint64
	mediorumPathFree   uint64
	storageExpectation uint64

	// archive bucket stats (only populated when ArchiveBlobStoreDSN is set)
	archivePathUsed uint64
	archivePathSize uint64
	archivePathFree uint64

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
	trackAccessInfoCache  *imcache.Cache[string, trackAccessInfo]
	attrCache             *imcache.Cache[string, *blob.Attributes]
	knownPresent          *imcache.Cache[string, int64]
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

func New(lc *lifecycle.Lifecycle, logger *zap.Logger, config MediorumConfig, posChannel chan pos.PoSRequest, core *coreServer.CoreService, ethService ethv1connect.EthServiceHandler) (*MediorumServer, error) {
	if env := os.Getenv("OPENAUDIO_ENV"); env != "" {
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
	} else {
		config.BlobStoreDSN = ensureNoTmpDir(config.BlobStoreDSN)
	}
	if config.ArchiveBlobStoreDSN != "" {
		config.ArchiveBlobStoreDSN = ensureNoTmpDir(config.ArchiveBlobStoreDSN)
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

	// archive bucket: only opened if configured. Routes CIDs that this node
	// only stores due to StoreAll (rendezvous rank >= ReplicationFactor).
	var archiveBucket *blob.Bucket
	if config.ArchiveBlobStoreDSN != "" {
		if config.ArchiveBlobStoreDSN == config.BlobStoreDSN {
			return nil, errors.New("OPENAUDIO_ARCHIVE_STORAGE_DRIVER_URL must differ from OPENAUDIO_STORAGE_DRIVER_URL")
		}
		if !config.StoreAll {
			logger.Warn("OPENAUDIO_ARCHIVE_STORAGE_DRIVER_URL is set but STORE_ALL is false; archive bucket will be unused")
		}
		archiveBucket, err = persistence.Open(config.ArchiveBlobStoreDSN)
		if err != nil {
			logger.Error("failed to open archive storage bucket", zap.Error(err))
			return nil, err
		}
	}

	// bucket to move all files from
	if config.MoveFromBlobStoreDSN != "" {
		if config.MoveFromBlobStoreDSN == config.BlobStoreDSN {
			logger.Error("OPENAUDIO_STORAGE_DRIVER_URL_MOVE_FROM (or AUDIUS_STORAGE_DRIVER_URL_MOVE_FROM) cannot be the same as OPENAUDIO_STORAGE_DRIVER_URL (or AUDIUS_STORAGE_DRIVER_URL)")
			return nil, err
		}
		bucketToMoveFrom, err := persistence.Open(config.MoveFromBlobStoreDSN)
		if err != nil {
			logger.Error("Failed to open bucket to move from. Ensure OPENAUDIO_STORAGE_DRIVER_URL (or AUDIUS_STORAGE_DRIVER_URL) and OPENAUDIO_STORAGE_DRIVER_URL_MOVE_FROM (or AUDIUS_STORAGE_DRIVER_URL_MOVE_FROM) are set (the latter can be empty if not moving data)", zap.Error(err))
			return nil, err
		}

		logger.Info(fmt.Sprintf("Moving all files from %s to %s. This may take a few hours...", config.MoveFromBlobStoreDSN, config.BlobStoreDSN))
		err = persistence.MoveAllFiles(bucketToMoveFrom, bucket)
		if err != nil {
			logger.Error("Failed to move files. Ensure OPENAUDIO_STORAGE_DRIVER_URL (or AUDIUS_STORAGE_DRIVER_URL) and OPENAUDIO_STORAGE_DRIVER_URL_MOVE_FROM (or AUDIUS_STORAGE_DRIVER_URL_MOVE_FROM) are set (the latter can be empty if not moving data)", zap.Error(err))
			return nil, err
		}

		logger.Info("Finished moving files between buckets. Please remove OPENAUDIO_STORAGE_DRIVER_URL_MOVE_FROM (or AUDIUS_STORAGE_DRIVER_URL_MOVE_FROM) from your environment and restart the server.")
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

	// HTTP transport for peer requests - skip TLS verify in dev with self-signed certs for replication/upload-scroll to work
	var peerTransport http.RoundTripper = http.DefaultTransport
	if config.Env == "dev" && os.Getenv("OPENAUDIO_TLS_SELF_SIGNED") == "true" {
		peerTransport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
		logger.Info("peer HTTP client using InsecureSkipVerify for dev with self-signed TLS")
	}

	peerHTTPClient := &http.Client{
		Transport: peerTransport,
		Timeout:   3 * time.Minute, // covers blob replication and pull
	}

	// crud
	peerHosts := []string{}
	allHosts := []string{}
	for _, peer := range config.Peers {
		allHosts = append(allHosts, peer.Host)
		if peer.Host != config.Self.Host {
			peerHosts = append(peerHosts, peer.Host)
		}
	}

	crud := crudr.New(config.Self.Host, config.privateKey, peerHosts, db, mediorumLifecycle, logger, peerHTTPClient)
	dbMigrate(crud, config.Self.Host)

	deadHosts := config.DeadHosts
	if deadHosts == nil {
		deadHosts = []string{}
	}
	rendezvousHasher := common.NewRendezvousHasher(allHosts, deadHosts)

	// req.cool http client for upload scroll, hostGetBlobInfo, etc.
	reqClient := req.C().
		SetUserAgent("mediorum " + config.Self.Host).
		SetTimeout(5 * time.Second)
	if config.Env == "dev" && os.Getenv("OPENAUDIO_TLS_SELF_SIGNED") == "true" {
		reqClient = reqClient.EnableInsecureSkipVerify()
	}

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
	echoServer.Use(common.CORS())
	echoServer.Use(timingMiddleware)

	ss := &MediorumServer{
		lc:               mediorumLifecycle,
		echo:             echoServer,
		bucket:           bucket,
		archiveBucket:    archiveBucket,
		crud:             crud,
		pgPool:           pgPool,
		reqClient:        reqClient,
		peerHTTPClient:   peerHTTPClient,
		logger:           logger,
		quit:             make(chan error, 1),
		ethService:       ethService,
		trustedNotifier:  &trustedNotifier,
		isSeeding:        config.Env == "stage" || config.Env == "prod",
		isAudiusdManaged: isAudiusdManaged,
		rendezvousHasher: rendezvousHasher,
		transcodeWork:    make(chan *Upload, 100),
		replicationWork:  make(chan *Upload, 100),
		posChannel:       posChannel,

		peerHealths:          map[string]*PeerHealth{},
		redirectCache:        imcache.New(imcache.WithMaxEntriesLimitOption[string, string](50_000, imcache.EvictionPolicyLRU)),
		uploadOrigCidCache:   imcache.New(imcache.WithMaxEntriesLimitOption[string, string](50_000, imcache.EvictionPolicyLRU)),
		imageCache:           imcache.New(imcache.WithMaxEntriesLimitOption[string, []byte](10_000, imcache.EvictionPolicyLRU)),
		trackAccessInfoCache: imcache.New(imcache.WithMaxEntriesLimitOption[string, trackAccessInfo](50_000, imcache.EvictionPolicyLRU), imcache.WithDefaultExpirationOption[string, trackAccessInfo](5*time.Minute)),
		attrCache:            imcache.New(imcache.WithMaxEntriesLimitOption[string, *blob.Attributes](10_000, imcache.EvictionPolicyLRU)),
		knownPresent:         imcache.New(imcache.WithMaxEntriesLimitOption[string, int64](500_000, imcache.EvictionPolicyLRU)),

		StartedAt: time.Now().UTC(),
		Config:    config,
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

	// Setup embedded tusd handler
	tusdHandler, err := ss.setupTusdHandler()
	if err != nil {
		logger.Error("failed to setup tusd handler", zap.Error(err))
		return nil, err
	}

	// Mount tusd routes
	routes.Any("/files/*", echo.WrapHandler(http.StripPrefix("/files", tusdHandler)))

	// public: uploads
	routes.GET("/uploads", ss.serveUploadList)
	routes.GET("/uploads/:id", ss.serveUploadDetail, ss.requireHealthy)
	routes.POST("/uploads/:id", ss.updateUpload, ss.requireHealthy, ss.requireUserSignature)
	routes.POST("/uploads", ss.postUpload, ss.requireHealthy)

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

	// Legacy endpoint for backward compatibility with old servers
	routes.GET("/health_check", ss.serveHealthCheckLegacy)
	routes.HEAD("/health_check", ss.serveHealthCheckLegacy)
	// New endpoint with consolidated health data
	routes.GET("/health-check", ss.serveMediorumHealthCheck)

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

	go ss.loadGeoIPDatabase()

	return ss, nil

}

func setResponseACAOHeaderFromRequest(req http.Request, resp echo.Response) {
	resp.Header().Set(
		echo.HeaderAccessControlAllowOrigin,
		req.Header.Get(echo.HeaderOrigin),
	)
}

// ensureNoTmpDir ensures file:// DSNs carry no_tmp_dir=true to avoid cross-device
// link errors when /tmp and the blob storage path are on different mount points.
// Non-file DSNs are returned unchanged.
func ensureNoTmpDir(dsn string) string {
	if !strings.HasPrefix(dsn, "file://") {
		return dsn
	}
	if strings.Contains(dsn, "no_tmp_dir") {
		return dsn
	}
	if strings.Contains(dsn, "?") {
		return dsn + "&no_tmp_dir=true"
	}
	return dsn + "?no_tmp_dir=true"
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
	ss.lc.AddManagedRoutine("replication workers", ss.startReplicationWorkers)

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
	if err := ss.lc.ShutdownWithTimeout(2 * time.Minute); err != nil {
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
	interval := 10 * time.Minute
	if os.Getenv("OPENAUDIO_ENV") == "dev" {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	for {
		select {
		case <-ticker.C:
			if ss.ethService == nil {
				ss.logger.Error("eth service not available for peer refresh")
				continue
			}

			// Fetch registered endpoints from eth service
			refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			endpointsResp, err := ss.ethService.GetRegisteredEndpoints(refreshCtx, connect.NewRequest(&ethv1.GetRegisteredEndpointsRequest{}))
			cancel()
			if err != nil {
				ss.logger.Error("failed to fetch registered nodes from eth service", zap.Error(err))
				continue
			}

			// Filter endpoints by service type and convert to registrar.Peer format
			var peers, signers []registrar.Peer
			var allHosts []string

			for _, ep := range endpointsResp.Msg.Endpoints {
				peer := registrar.Peer{
					Host:   audiusHttputil.RemoveTrailingSlash(strings.ToLower(ep.Endpoint)),
					Wallet: strings.ToLower(ep.DelegateWallet),
				}

				// Content nodes and validators are peers
				if strings.EqualFold(ep.ServiceType, "content-node") || strings.EqualFold(ep.ServiceType, "validator") {
					peers = append(peers, peer)
					allHosts = append(allHosts, peer.Host)
				}
			}

			// Update rendezvous hasher with new hosts
			deadHosts := ss.Config.DeadHosts
			if deadHosts == nil {
				deadHosts = []string{}
			}
			ss.rendezvousHasher.UpdateHosts(allHosts, deadHosts)

			// Update config dynamically (protected by existing config access patterns)
			ss.Config.Peers = peers
			ss.Config.Signers = signers

			// Update WalletIsRegistered based on updated peers list
			ss.Config.WalletIsRegistered = false
			for _, peer := range peers {
				if strings.EqualFold(ss.Config.Self.Wallet, peer.Wallet) && strings.EqualFold(ss.Config.Self.Host, peer.Host) {
					ss.Config.WalletIsRegistered = true
					break
				}
			}

			// Log detailed info if not registered to help diagnose issues
			if !ss.Config.WalletIsRegistered {
				peerHosts := make([]string, len(peers))
				for i, p := range peers {
					peerHosts[i] = p.Host
				}
				ss.logger.Warn("node not found in registered peers list",
					zap.String("self_host", ss.Config.Self.Host),
					zap.String("self_wallet", ss.Config.Self.Wallet),
					zap.Int("total_peers", len(peers)),
					zap.Strings("peer_hosts", peerHosts))
			}

			ss.crud.UpdatePeers(allHosts)

			ss.logger.Info("updated peers and signers dynamically", zap.Int("peers", len(peers)), zap.Int("signers", len(signers)), zap.Bool("wallet_is_registered", ss.Config.WalletIsRegistered))
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
