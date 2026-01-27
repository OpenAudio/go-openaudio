package uptime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"connectrpc.com/connect"
	ethv1 "github.com/OpenAudio/go-openaudio/pkg/api/eth/v1"
	ethv1connect "github.com/OpenAudio/go-openaudio/pkg/api/eth/v1/v1connect"
	"github.com/OpenAudio/go-openaudio/pkg/httputil"
	"github.com/OpenAudio/go-openaudio/pkg/registrar"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"go.etcd.io/bbolt"
)

var (
	UptimeBucket = []byte("UptimeRecords")
)

type Config struct {
	Self       registrar.Peer
	Peers      []registrar.Peer
	ListenPort string
	Dir        string

	Env      string
	NodeType string
}

type Uptime struct {
	quit       chan os.Signal
	logger     *slog.Logger
	Config     Config
	DB         *bbolt.DB
	ethService ethv1connect.EthServiceHandler
	ctx        context.Context
	peersMu    sync.RWMutex
}

func Run(ctx context.Context, ethService ethv1connect.EthServiceHandler) error {
	if ethService == nil {
		return fmt.Errorf("ethService is required")
	}
	env := os.Getenv("OPENAUDIO_ENV")
	nodeType := "validator"
	slog.Info("starting", "env", env, "nodeType", nodeType)

	switch env {
	case "prod":
		err := start(ctx, true, nodeType, env, ethService)
		if err != nil {
			return err
		}
	case "stage":
		err := start(ctx, false, nodeType, env, ethService)
		if err != nil {
			return err
		}
	case "single":
		slog.Info("no need to monitor peers when running a single node. sleeping forever...")
		// block forever so container doesn't restart constantly
		c := make(chan struct{})
		<-c
	default:
		// TODO
		// startDevCluster()
		c := make(chan struct{})
		<-c
	}

	return nil
}

func New(config Config) (*Uptime, error) {
	// validate host config
	if config.Self.Host == "" {
		log.Fatal("host is required")
	} else if hostUrl, err := url.Parse(config.Self.Host); err != nil {
		log.Fatal("invalid host: ", err, "host", hostUrl)
	}

	logger := slog.With("self", config.Self.Host)

	// ensure dir
	if err := os.MkdirAll(config.Dir, os.ModePerm); err != nil {
		logger.Error("failed to create BoltDB dir", "err", err)
	}

	// initialize BoltDB
	db, err := bbolt.Open(config.Dir+"/uptime.db", 0666, nil)
	if err != nil {
		log.Fatal(err)
	}
	err = db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(UptimeBucket)
		return err
	})
	if err != nil {
		log.Fatal(err)
	}

	u := &Uptime{
		quit:   make(chan os.Signal, 1),
		logger: logger,
		Config: config,
		DB:     db,
	}

	return u, nil
}

func (u *Uptime) Start() {
	go u.startHealthPoller()
	go u.startPeerRefresher()

	e := echo.New()
	e.HideBanner = true
	e.Debug = true

	e.Use(middleware.Recover())
	e.Use(middleware.Logger())
	e.Use(middleware.CORS())

	e.GET("/d_api/uptime", u.handleUptime)
	e.GET("/health_check", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]interface{}{
			"healthy": "true",
		})
	})

	e.Logger.Fatal(e.Start(":" + u.Config.ListenPort))

	signal.Notify(u.quit, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	<-u.quit
	close(u.quit)

	u.Stop()
}

func (u *Uptime) Stop() {
	u.logger.Info("stopping")
	if u.DB != nil {
		err := u.DB.Close()
		if err != nil {
			u.logger.Error("error closing db", "err", err)
		}
	}
	u.logger.Info("bye")
}

func (u *Uptime) startHealthPoller() {
	time.Sleep(time.Second)

	u.logger.Info("starting health poller")

	u.pollHealth()
	ticker := time.NewTicker(time.Hour)
	for range ticker.C {
		u.pollHealth()
	}
}

func toPeers(endpoints []*ethv1.ServiceEndpoint) []registrar.Peer {
	var peers []registrar.Peer
	for _, ep := range endpoints {
		if strings.EqualFold(ep.ServiceType, "content-node") || strings.EqualFold(ep.ServiceType, "validator") {
			peers = append(peers, registrar.Peer{
				Host:   httputil.RemoveTrailingSlash(strings.ToLower(ep.Endpoint)),
				Wallet: strings.ToLower(ep.DelegateWallet),
			})
		}
	}
	return peers
}

func (u *Uptime) startPeerRefresher() {
	interval := 30 * time.Minute
	if os.Getenv("OPENAUDIO_ENV") == "dev" {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)

	for {
		select {
		case <-ticker.C:
			if u.ethService == nil {
				u.logger.Error("eth service not available for peer refresh")
				continue
			}

			refreshCtx, cancel := context.WithTimeout(u.ctx, 30*time.Second)
			endpointsResp, err := u.ethService.GetRegisteredEndpoints(refreshCtx, connect.NewRequest(&ethv1.GetRegisteredEndpointsRequest{}))
			cancel()
			if err != nil {
				u.logger.Error("failed to fetch registered nodes from eth service", "err", err)
				continue
			}

			peers := toPeers(endpointsResp.Msg.Endpoints)

			u.peersMu.Lock()
			u.Config.Peers = peers
			u.peersMu.Unlock()

			u.logger.Info("updated peers dynamically", "peers", len(peers))
		case <-u.ctx.Done():
			return
		}
	}
}

func (u *Uptime) pollHealth() {
	u.peersMu.RLock()
	peers := make([]registrar.Peer, len(u.Config.Peers))
	copy(peers, u.Config.Peers)
	u.peersMu.RUnlock()

	httpClient := http.Client{
		Timeout: time.Second,
	}
	wg := sync.WaitGroup{}
	wg.Add(len(peers))
	for _, peer := range peers {
		peer := peer
		go func() {
			defer wg.Done()
			req, err := http.NewRequest("GET", apiPath(peer.Host, "/health-check"), nil)
			if err != nil {
				u.recordNodeUptimeToDB(peer.Host, false)
				return
			}
			req.Header.Set("User-Agent", "peer health monitor "+u.Config.Self.Host)
			resp, err := httpClient.Do(req)
			if err != nil {
				u.recordNodeUptimeToDB(peer.Host, false)
				return
			}
			defer resp.Body.Close()

			// read body
			var response map[string]interface{}
			decoder := json.NewDecoder(resp.Body)
			err = decoder.Decode(&response)
			if err != nil {
				u.recordNodeUptimeToDB(peer.Host, false)
				return
			}

			// check if node is online and 200 for health check
			u.recordNodeUptimeToDB(peer.Host, resp.StatusCode == 200)
		}()
	}
	wg.Wait()
}

type UptimeResponse struct {
	Host             string         `json:"host"`
	UptimePercentage float64        `json:"uptime_percentage"`
	Duration         string         `json:"duration"`
	UptimeHours      map[string]int `json:"uptime_raw_data"`
}

func (u *Uptime) handleUptime(c echo.Context) error {
	host := c.QueryParam("host")
	if host == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Peer host is required")
	}

	durationHours := c.QueryParam("durationHours")
	if durationHours == "" {
		durationHours = "24"
	}

	hours, err := strconv.Atoi(durationHours)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid durationHours value")
	}

	duration := time.Duration(hours) * time.Hour

	uptimePercentage, uptimeHours, err := u.calculateUptime(host, duration)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Error calculating uptime: "+err.Error())
	}

	resp := UptimeResponse{
		Host:             host,
		UptimePercentage: uptimePercentage,
		Duration:         fmt.Sprintf("%dh", hours),
		UptimeHours:      uptimeHours,
	}

	return c.JSON(http.StatusOK, resp)
}

func (u *Uptime) calculateUptime(host string, duration time.Duration) (float64, map[string]int, error) {
	normalizedHost := httputil.RemoveTrailingSlash(strings.ToLower(host))

	var upCount, totalCount int
	uptimeHours := make(map[string]int)

	endTime := time.Now().UTC().Truncate(time.Hour)
	startTime := endTime.Add(-duration)

	err := u.DB.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(UptimeBucket)

		for t := endTime; !t.Before(startTime); t = t.Add(-time.Hour) {
			hourKey := t.Format("2006-01-02T15")
			hourTimestamp := t.Format(time.RFC3339)

			peerBucket := b.Bucket([]byte(hourKey))
			if peerBucket != nil {
				value := peerBucket.Get([]byte(normalizedHost))
				totalCount++
				if string(value) == "1" {
					uptimeHours[hourTimestamp] = 1 // online
					upCount++
				} else {
					uptimeHours[hourTimestamp] = 0 // offline
				}
			}
			// if there's no record, don't include the hour in the map
		}
		return nil
	})

	if err != nil {
		return 0, nil, err
	}

	if totalCount == 0 {
		return 0, uptimeHours, nil
	}

	uptimePercentage := (float64(upCount) / float64(totalCount)) * 100
	return uptimePercentage, uptimeHours, nil
}

func (u *Uptime) recordNodeUptimeToDB(host string, wasUp bool) {
	currentTime := time.Now().UTC().Truncate(time.Hour)
	u.DB.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(UptimeBucket)
		hourKey := []byte(currentTime.Format("2006-01-02T15"))
		peerBucket, err := b.CreateBucketIfNotExists(hourKey)
		if err != nil {
			return err
		}
		status := []byte("0") // assume down
		if wasUp {
			status = []byte("1") // up
		}
		return peerBucket.Put([]byte(host), status)
	})
}

func apiPath(parts ...string) string {
	host := parts[0]
	parts[0] = ""
	u, err := url.Parse(host)
	if err != nil {
		panic(err)
	}
	u = u.JoinPath(parts...)
	return u.String()
}

func start(ctx context.Context, isProd bool, nodeType, env string, ethService ethv1connect.EthServiceHandler) error {
	myEndpoint := mustGetenv("nodeEndpoint")
	logger := slog.With("endpoint", myEndpoint)

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	endpointsResp, err := ethService.GetRegisteredEndpoints(reqCtx, connect.NewRequest(&ethv1.GetRegisteredEndpointsRequest{}))
	if err != nil {
		return fmt.Errorf("failed to get registered endpoints from eth service: %w", err)
	}

	peers := toPeers(endpointsResp.Msg.Endpoints)

	logger.Info("fetched registered nodes", "peers", len(peers), "nodeType", nodeType, "env", env)

	config := Config{
		Self: registrar.Peer{
			Host: httputil.RemoveTrailingSlash(strings.ToLower(myEndpoint)),
		},
		Peers:      peers,
		ListenPort: "1996",
		Dir:        getenvWithDefault("uptimeDataDir", "/bolt"),
		Env:        env,
		NodeType:   nodeType,
	}

	ph, err := New(config)
	if err != nil {
		return fmt.Errorf("failed to init Uptime server: %w", err)
	}

	ph.ethService = ethService
	ph.ctx = ctx

	ph.Start()
	return nil
}

func mustGetenv(key string) string {
	val := os.Getenv(key)
	if val == "" {
		slog.Info(fmt.Sprintf("missing required env variable: %s. sleeping...", key))
		// if config is incorrect, sleep a bit to prevent container from restarting constantly
		time.Sleep(time.Hour)
		log.Fatal("missing required env variable: ", key)
	}
	return val
}

func getenvWithDefault(key string, fallback string) string {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	return val
}
