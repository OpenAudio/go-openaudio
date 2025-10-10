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
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/httputil"
	"github.com/OpenAudio/go-openaudio/pkg/registrar"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"go.etcd.io/bbolt"
	"golang.org/x/sync/errgroup"
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
	quit   chan os.Signal
	logger *slog.Logger
	Config Config
	DB     *bbolt.DB
}

func Run(ctx context.Context) error {
	env := ""      // prod || stage
	nodeType := "" // content || discovery
	if os.Getenv("audius_discprov_url") != "" {
		env = os.Getenv("audius_discprov_env")
		nodeType = "discovery"
	} else if os.Getenv("creatorNodeEndpoint") != "" {
		env = os.Getenv("MEDIORUM_ENV")
		nodeType = "content"
	} else if os.Getenv("nodeEndpoint") != "" {
		env = os.Getenv("MEDIORUM_ENV")
		nodeType = "validator"
	} else {
		slog.Info("no envs set. sleeping forever...")
		// block forever so container doesn't restart constantly
		c := make(chan struct{})
		<-c
	}
	slog.Info("starting", "env", env, "nodeType", nodeType)

	switch env {
	case "prod":
		startStagingOrProd(true, nodeType, env)
	case "stage":
		startStagingOrProd(false, nodeType, env)
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

func (u *Uptime) pollHealth() {
	httpClient := http.Client{
		Timeout: time.Second,
	}
	wg := sync.WaitGroup{}
	wg.Add(len(u.Config.Peers))
	for _, peer := range u.Config.Peers {
		peer := peer
		go func() {
			defer wg.Done()
			req, err := http.NewRequest("GET", apiPath(peer.Host, "/health_check"), nil)
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
				value := peerBucket.Get([]byte(host))
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

func startStagingOrProd(isProd bool, nodeType, env string) {
	// must have either a CN or DN endpoint configured, along with other env vars
	myEndpoint := ""

	if nodeType == "content" {
		myEndpoint = mustGetenv("creatorNodeEndpoint")
	} else if nodeType == "discovery" {
		myEndpoint = mustGetenv("audius_discprov_url")
	} else if nodeType == "validator" {
		myEndpoint = mustGetenv("nodeEndpoint")
	}

	logger := slog.With("endpoint", myEndpoint)

	// fetch peers
	g := registrar.NewMultiStaging()
	if isProd {
		g = registrar.NewMultiProd()
	}
	var peers []registrar.Peer
	var err error

	eg := new(errgroup.Group)
	eg.Go(func() error {
		switch nodeType {
		case "content", "validator":
			peers, err = g.Peers()
		case "discovery":
			peers, err = g.Signers()
		}
		return err
	})
	if err := eg.Wait(); err != nil {
		panic(err)
	}

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
		logger.Error("failed to init Uptime server", "err", err)
	}

	// go refreshPeersAndSigners(ph, g, nodeType)

	ph.Start()
}

// fetch registered nodes from chain / The Graph every 30 minutes and restart if they've changed
func refreshPeersAndSigners(ph *Uptime, g registrar.PeerProvider, nodeType string) {
	ticker := time.NewTicker(30 * time.Minute)
	for range ticker.C {
		var peers []registrar.Peer
		var err error

		eg := new(errgroup.Group)
		eg.Go(func() error {
			switch nodeType {
			case "content":
				peers, err = g.Peers()
			case "discovery":
				peers, err = g.Signers()
			}
			return err
		})
		if err := eg.Wait(); err != nil {
			slog.Error("failed to fetch registered nodes", "err", err)
			continue
		}

		var combined, configCombined []string

		for _, peer := range peers {
			combined = append(combined, fmt.Sprintf("%s,%s", httputil.RemoveTrailingSlash(strings.ToLower(peer.Host)), strings.ToLower(peer.Wallet)))
		}

		for _, configPeer := range ph.Config.Peers {
			configCombined = append(configCombined, fmt.Sprintf("%s,%s", httputil.RemoveTrailingSlash(strings.ToLower(configPeer.Host)), strings.ToLower(configPeer.Wallet)))
		}

		slices.Sort(combined)
		slices.Sort(configCombined)
		if !slices.Equal(combined, configCombined) {
			slog.Info("peers changed on chain. restarting...", "peers", len(peers), "combined", combined, "configCombined", configCombined)
			os.Exit(0) // restarting from inside the app is too error-prone so we'll let docker compose autoheal handle it
		}
	}
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
