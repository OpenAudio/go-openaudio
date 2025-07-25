package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/core"
	"github.com/AudiusProject/audiusd/pkg/core/config"
	"github.com/AudiusProject/audiusd/pkg/core/console"
	coreServer "github.com/AudiusProject/audiusd/pkg/core/server"
	"github.com/AudiusProject/audiusd/pkg/eth"
	"github.com/AudiusProject/audiusd/pkg/etl"
	"github.com/AudiusProject/audiusd/pkg/lifecycle"
	"github.com/AudiusProject/audiusd/pkg/mediorum"
	"github.com/AudiusProject/audiusd/pkg/mediorum/server"
	"github.com/AudiusProject/audiusd/pkg/pos"
	"github.com/AudiusProject/audiusd/pkg/system"
	"github.com/AudiusProject/audiusd/pkg/uptime"
	"github.com/AudiusProject/audiusd/pkg/version"
	"go.akshayshah.org/connectproto"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	corev1connect "github.com/AudiusProject/audiusd/pkg/api/core/v1/v1connect"
	ethv1connect "github.com/AudiusProject/audiusd/pkg/api/eth/v1/v1connect"
	etlv1connect "github.com/AudiusProject/audiusd/pkg/api/etl/v1/v1connect"
	storagev1connect "github.com/AudiusProject/audiusd/pkg/api/storage/v1/v1connect"
	systemv1connect "github.com/AudiusProject/audiusd/pkg/api/system/v1/v1connect"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"golang.org/x/sync/errgroup"
)

const (
	initialBackoff = 10 * time.Second
	maxBackoff     = 10 * time.Minute
	maxRetries     = 10
)

var startTime time.Time

// Common global options
var (
	marshalOpts   = protojson.MarshalOptions{EmitUnpopulated: true}
	unmarshalOpts = protojson.UnmarshalOptions{DiscardUnknown: true}

	// Compose them into the Connect handler option
	connectJSONOpt = connectproto.WithJSON(marshalOpts, unmarshalOpts)
)

type proxyConfig struct {
	path   string
	target string
}

type serverConfig struct {
	httpPort   string
	httpsPort  string
	hostname   string
	tlsEnabled bool
}

func main() {
	startTime = time.Now().UTC()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := setupLogger()
	hostUrl := setupHostUrl()
	setupDelegateKeyPair(logger)
	posChannel := make(chan pos.PoSRequest)
	dbUrl := config.GetDbURL()

	rootLifecycle := lifecycle.NewLifecycle(ctx, "root lifecycle", logger)

	ethService := eth.NewEthService(dbUrl, config.GetEthRPC(), config.GetRegistryAddress(), logger, config.GetRuntimeEnvironment())
	coreService := coreServer.NewCoreService()
	storageService := server.NewStorageService()
	etlService := etl.NewETLService(coreService, logger)
	systemService := system.NewSystemService(coreService, storageService, etlService)

	services := []struct {
		name    string
		fn      func() error
		enabled bool
	}{
		{
			"audiusd-echo-server",
			func() error {
				return startEchoProxy(hostUrl, logger, coreService, storageService, etlService, systemService, ethService)
			},
			true,
		},
		{
			"core",
			func() error { return core.Run(ctx, rootLifecycle, logger, posChannel, coreService, ethService) },
			true,
		},
		{
			"mediorum",
			func() error { return mediorum.Run(rootLifecycle, posChannel, storageService, coreService) },
			isStorageEnabled(),
		},
		{
			"uptime",
			func() error { return uptime.Run(ctx, logger) },
			isUpTimeEnabled(hostUrl),
		},
		{
			"etl",
			func() error {
				etlService.SetDBURL(dbUrl)
				etlService.SetRunDownMigrations(os.Getenv("AUDIUSD_ETL_RUN_DOWN_MIGRATIONS") == "true")
				return etlService.Run()
			},
			os.Getenv("AUDIUSD_ETL_ENABLED") == "true",
		},
		{
			"eth",
			func() error { return ethService.Run(ctx) },
			true,
		},
	}

	for _, svc := range services {
		if svc.enabled {
			runWithRecover(svc.name, ctx, logger, svc.fn)
		}
	}

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	logger.Info("Received termination signal, shutting down...")
	cancel()
	<-ctx.Done()
	logger.Info("Shutdown complete!")
}

func runWithRecover(name string, ctx context.Context, logger *common.Logger, f func() error) {
	backoff := initialBackoff
	retries := 0

	var run func()
	run = func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Errorf("%s goroutine panicked: %v", name, r)
				logger.Errorf("%s stack trace: %s", name, string(debug.Stack()))

				select {
				case <-ctx.Done():
					logger.Infof("%s shutdown requested, not restarting", name)
					return
				default:
					if retries >= maxRetries {
						logger.Errorf("%s exceeded maximum retry attempts (%d). Not restarting.", name, maxRetries)
						return
					}

					retries++
					logger.Infof("%s will restart in %v (attempt %d/%d)", name, backoff, retries, maxRetries)
					time.Sleep(backoff)

					// Exponential backoff
					backoff = time.Duration(float64(backoff) * 2)
					if backoff > maxBackoff {
						backoff = maxBackoff
					}

					// Restart the goroutine
					go run()
				}
			}
		}()

		if err := f(); err != nil {
			logger.Errorf("%s error: %v", name, err)
			// Treat errors like panics and restart the service
			panic(fmt.Sprintf("%s error: %v", name, err))
		}
	}

	go run()
}

func setupLogger() *common.Logger {
	var slogLevel slog.Level
	switch os.Getenv("AUDIUSD_LOG_LEVEL") {
	case "debug":
		slogLevel = slog.LevelDebug
	case "info":
		slogLevel = slog.LevelInfo
	case "warn":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}

	return common.NewLogger(&slog.HandlerOptions{Level: slogLevel})
}

func setupHostUrl() *url.URL {
	endpoints := []string{
		os.Getenv("creatorNodeEndpoint"),
		os.Getenv("audius_discprov_url"),
	}

	for _, ep := range endpoints {
		if ep != "" {
			if u, err := url.Parse(ep); err == nil {
				return u
			}
		}
	}
	return &url.URL{Scheme: "http", Host: "localhost"}
}

func setupDelegateKeyPair(logger *common.Logger) {
	// Various applications across the protocol stack switch on these env var semantics
	// Check both discovery and content env vars
	// If neither discovery nor content node, (i.e. an audiusd rpc)
	// generate new keys and set as an implied "content node"
	audius_delegate_private_key := os.Getenv("audius_delegate_private_key")
	audius_delegate_owner_wallet := os.Getenv("audius_delegate_owner_wallet")
	delegatePrivateKey := os.Getenv("delegatePrivateKey")
	delegateOwnerWallet := os.Getenv("delegateOwnerWallet")

	if audius_delegate_private_key != "" || audius_delegate_owner_wallet != "" {
		logger.Infof("setupDelegateKeyPair: Node is discovery type")
		return
	}

	if delegatePrivateKey != "" || delegateOwnerWallet != "" {
		logger.Infof("setupDelegateKeyPair: Node is content type")
		return
	}

	privKey, ownerWallet := keyGen()
	os.Setenv("delegatePrivateKey", privKey)
	os.Setenv("delegateOwnerWallet", ownerWallet)
	logger.Infof("Generated and set delegate key pair for implied content node: %s", ownerWallet)
}

func getEchoServerConfig(hostUrl *url.URL) serverConfig {
	httpPort := getEnvString("AUDIUSD_HTTP_PORT", "80")
	httpsPort := getEnvString("AUDIUSD_HTTPS_PORT", "443")
	hostname := hostUrl.Hostname()

	// TODO: this is all gross
	if hostname == "altego.net" && httpPort == "80" && httpsPort == "443" {
		httpPort = "5000"
	}

	tlsEnabled := true
	switch {
	case os.Getenv("AUDIUSD_TLS_DISABLED") == "true":
		tlsEnabled = false
	case hasSuffix(hostname, []string{"altego.net", "bdnodes.net", "staked.cloud"}):
		tlsEnabled = false
	case hostname == "localhost":
		tlsEnabled = true
		if os.Getenv("AUDIUSD_TLS_SELF_SIGNED") == "" {
			os.Setenv("AUDIUSD_TLS_SELF_SIGNED", "true")
		}
	}
	// end gross

	return serverConfig{
		httpPort:   httpPort,
		httpsPort:  httpsPort,
		hostname:   hostname,
		tlsEnabled: tlsEnabled,
	}
}

func connectGET[Req any, Res any](
	handler func(ctx context.Context, req *connect.Request[Req]) (*connect.Response[Res], error),
) echo.HandlerFunc {
	return func(c echo.Context) error {
		queryParams := c.QueryParams()
		maxParams := 50
		count := 0
		paramMap := make(map[string]interface{})
		for k, v := range queryParams {
			if count >= maxParams {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": "too many query parameters"})
			}
			if len(v) == 1 {
				val := v[0]
				// Try int64
				if i, err := strconv.ParseInt(val, 10, 64); err == nil {
					paramMap[k] = i
				} else if f, err := strconv.ParseFloat(val, 64); err == nil {
					paramMap[k] = f
				} else if b, err := strconv.ParseBool(val); err == nil {
					paramMap[k] = b
				} else {
					paramMap[k] = val
				}
			} else {
				paramMap[k] = v
			}
			count++
		}

		// Marshal to JSON
		paramBytes, err := json.Marshal(paramMap)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid query parameters"})
		}

		// Unmarshal to your generic request type
		req := new(Req)
		if err := json.Unmarshal(paramBytes, req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "failed to map query parameters to request"})
		}

		// Call the ConnectRPC handler
		resp, err := handler(c.Request().Context(), connect.NewRequest(req))
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}

		msg, ok := any(resp.Msg).(proto.Message)
		if !ok {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "response is not proto.Message"})
		}

		jsonBytes, err := marshalOpts.Marshal(msg)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to marshal response: %v", err.Error())})
		}

		return c.JSONBlob(http.StatusOK, jsonBytes)

	}
}

func startEchoProxy(hostUrl *url.URL, logger *common.Logger, coreService *coreServer.CoreService, storageService *server.StorageService, etlService *etl.ETLService, systemService *system.SystemService, ethService *eth.EthService) error {
	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Logger(), middleware.Recover(), common.InjectRealIP())

	rpcGroup := e.Group("")
	corePath, coreHandler := corev1connect.NewCoreServiceHandler(coreService, connectJSONOpt)
	rpcGroup.POST(corePath+"*", echo.WrapHandler(coreHandler))

	storagePath, storageHandler := storagev1connect.NewStorageServiceHandler(storageService, connectJSONOpt)
	rpcGroup.POST(storagePath+"*", echo.WrapHandler(storageHandler))

	etlPath, etlHandler := etlv1connect.NewETLServiceHandler(etlService, connectJSONOpt)
	rpcGroup.POST(etlPath+"*", echo.WrapHandler(etlHandler))

	systemPath, systemHandler := systemv1connect.NewSystemServiceHandler(systemService, connectJSONOpt)
	rpcGroup.POST(systemPath+"*", echo.WrapHandler(systemHandler))

	ethPath, ethHandler := ethv1connect.NewEthServiceHandler(ethService, connectJSONOpt)
	rpcGroup.POST(ethPath+"*", echo.WrapHandler(ethHandler))

	// register GET routes
	rpcGroup.GET(corev1connect.CoreServiceGetStatusProcedure, connectGET(coreService.GetStatus))
	rpcGroup.GET(corev1connect.CoreServiceGetNodeInfoProcedure, connectGET(coreService.GetNodeInfo))
	rpcGroup.GET(corev1connect.CoreServiceGetBlockProcedure, connectGET(coreService.GetBlock))
	rpcGroup.GET(corev1connect.CoreServiceGetTransactionProcedure, connectGET(coreService.GetTransaction))
	rpcGroup.GET(corev1connect.CoreServiceGetStoredSnapshotsProcedure, connectGET(coreService.GetStoredSnapshots))
	rpcGroup.GET(corev1connect.CoreServiceGetRewardAttestationProcedure, connectGET(coreService.GetRewardAttestation))
	rpcGroup.GET(corev1connect.CoreServiceGetRewardsProcedure, connectGET(coreService.GetRewards))

	go func() {
		grpcServer := echo.New()
		grpcServerGroup := grpcServer.Group("")
		grpcServerGroup.Any(corePath+"*", echo.WrapHandler(coreHandler))
		grpcServerGroup.Any(storagePath+"*", echo.WrapHandler(storageHandler))
		grpcServerGroup.Any(etlPath+"*", echo.WrapHandler(etlHandler))
		grpcServerGroup.Any(systemPath+"*", echo.WrapHandler(systemHandler))
		grpcServerGroup.Any(ethPath+"*", echo.WrapHandler(ethHandler))

		// Create h2c-compatible server
		h2cServer := &http.Server{
			Addr:    ":50051",
			Handler: h2c.NewHandler(grpcServer, &http2.Server{}),
		}

		if err := h2cServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("grpcServer on 50051 failed: %v", err)
			return
		}
	}()

	e.GET("/", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]int{"a": 440})
	})

	e.GET("/health-check", func(c echo.Context) error {
		return c.JSON(http.StatusOK, getHealthCheckResponse(hostUrl))
	})

	if os.Getenv("audius_discprov_url") != "" && !isCoreOnly() {
		e.GET("/health_check", func(c echo.Context) error {
			return c.JSON(http.StatusOK, getHealthCheckResponse(hostUrl))
		})
	}

	e.GET("/console", func(c echo.Context) error {
		return c.Redirect(http.StatusMovedPermanently, "/console/overview")
	})

	proxies := []proxyConfig{
		{"/console/*", "http://localhost:26659"},
		{"/core/*", "http://localhost:26659"},
	}

	// dashboard compatibility - country flags + version info
	locationHandler := func(c echo.Context) error {
		type ipInfoResponse struct {
			Country string `json:"country"`
			Loc     string `json:"loc"`
		}

		response := struct {
			Country   string  `json:"country"`
			Version   string  `json:"version"`
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		}{
			Version: version.Version.Version,
		}

		resp, err := http.Get("https://ipinfo.io")
		if err == nil && resp.StatusCode == http.StatusOK {
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err == nil {
				var ipInfo ipInfoResponse
				if err := json.Unmarshal(body, &ipInfo); err == nil {
					response.Country = ipInfo.Country
					// parse lat long
					if loc := strings.Split(ipInfo.Loc, ","); len(loc) == 2 {
						response.Latitude, _ = strconv.ParseFloat(loc[0], 64)
						response.Longitude, _ = strconv.ParseFloat(loc[1], 64)
					}
				}
			}
		}
		// dashboard expected format
		return c.JSON(http.StatusOK, map[string]interface{}{
			"data": response,
			"version": map[string]string{
				"version": version.Version.Version,
			},
		})
	}

	corsGroup := e.Group("", middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{http.MethodGet},
	}))

	corsGroup.GET("/version", locationHandler)
	corsGroup.GET("/location", locationHandler)
	// end dashboard compatibility

	if isUpTimeEnabled(hostUrl) {
		proxies = append(proxies, proxyConfig{"/d_api/*", "http://localhost:1996"})
	}

	if isStorageEnabled() {
		proxies = append(proxies, proxyConfig{"/*", "http://localhost:1991"})
	}

	for _, proxy := range proxies {
		target, err := url.Parse(proxy.target)
		if err != nil {
			logger.Error("Failed to parse URL:", err)
			continue
		}
		e.Any(proxy.path, echo.WrapHandler(httputil.NewSingleHostReverseProxy(target)))
	}

	config := getEchoServerConfig(hostUrl)

	if config.tlsEnabled {
		return startWithTLS(e, config.httpPort, config.httpsPort, hostUrl, logger)
	}
	return e.Start(":" + config.httpPort)
}

func startWithTLS(e *echo.Echo, httpPort, httpsPort string, hostUrl *url.URL, logger *common.Logger) error {
	useSelfSigned := os.Getenv("AUDIUSD_TLS_SELF_SIGNED") == "true"

	if useSelfSigned {
		logger.Info("Using self-signed certificate")
		cert, key, err := generateSelfSignedCert(hostUrl.Hostname())
		if err != nil {
			logger.Errorf("Failed to generate self-signed certificate: %v", err)
			return fmt.Errorf("failed to generate self-signed certificate: %v", err)
		}

		certDir := getEnvString("audius_core_root_dir", "/audius-core") + "/echo/certs"
		logger.Infof("Creating certificate directory: %s", certDir)
		if err := os.MkdirAll(certDir, 0755); err != nil {
			logger.Errorf("Failed to create certificate directory: %v", err)
			return fmt.Errorf("failed to create certificate directory: %v", err)
		}

		certFile := certDir + "/cert.pem"
		keyFile := certDir + "/key.pem"

		logger.Infof("Writing certificate to: %s", certFile)
		if err := os.WriteFile(certFile, cert, 0644); err != nil {
			logger.Errorf("Failed to write cert file: %v", err)
			return fmt.Errorf("failed to write cert file: %v", err)
		}

		logger.Infof("Writing private key to: %s", keyFile)
		if err := os.WriteFile(keyFile, key, 0600); err != nil {
			logger.Errorf("Failed to write key file: %v", err)
			return fmt.Errorf("failed to write key file: %v", err)
		}

		logger.Infof("Starting HTTPS server on port %s", httpsPort)

		tlsCert, err := tls.X509KeyPair(cert, key)
		if err != nil {
			logger.Errorf("Failed to load X509 key pair: %v", err)
			return fmt.Errorf("failed to load X509 key pair: %v", err)
		}
		go func() {
			tlsConfig := &tls.Config{
				NextProtos: []string{"h2"},
			}
			tlsConfig.Certificates = []tls.Certificate{
				tlsCert,
			}

			h2Server := &http2.Server{}
			server := &http.Server{
				Addr:      ":" + httpsPort,
				Handler:   e,
				TLSConfig: tlsConfig,
			}

			http2.ConfigureServer(server, h2Server)

			if err := server.ListenAndServeTLS(certFile, keyFile); err != nil && err != http.ErrServerClosed {
				logger.Errorf("Failed to start HTTPS server: %v", err)
			}
		}()

		logger.Infof("Starting HTTP server on port %s", httpPort)
		return e.Start(":" + httpPort)
	}

	whitelist := []string{hostUrl.Hostname(), "localhost"}
	addrs, _ := net.InterfaceAddrs()
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				whitelist = append(whitelist, ip4.String())
			}
		}
	}

	logger.Info("TLS host whitelist: " + strings.Join(whitelist, ", "))
	e.AutoTLSManager.HostPolicy = autocert.HostWhitelist(whitelist...)
	e.AutoTLSManager.Cache = autocert.DirCache(getEnvString("audius_core_root_dir", "/audius-core") + "/echo/cache")
	e.Pre(middleware.HTTPSRedirect())

	eg := errgroup.Group{}

	eg.Go(func() error {
		return e.StartAutoTLS(":" + httpsPort)
	})

	eg.Go(func() error {
		h2s := &http2.Server{}
		h1s := &http.Server{
			Addr:    ":" + httpPort,
			Handler: h2c.NewHandler(e, h2s),
		}
		return h1s.ListenAndServe()
	})

	return eg.Wait()
}

func generateSelfSignedCert(hostname string) ([]byte, []byte, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour) // Valid for 1 year

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, nil, err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Audius Self-Signed Certificate"},
			CommonName:   hostname,
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{hostname, "localhost"},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: derBytes,
	})

	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})

	return certPEM, privateKeyPEM, nil
}

// TODO: I don't love this, but it is kinof the only way to make this work rn
func isCoreOnly() bool {
	return os.Getenv("AUDIUSD_CORE_ONLY") == "true"
}

func isUpTimeEnabled(hostUrl *url.URL) bool {
	return hostUrl.Hostname() != "localhost"
}

// TODO: I don't love this, but it works safely for now
func isStorageEnabled() bool {
	if isCoreOnly() {
		return false
	}
	if os.Getenv("audius_discprov_url") != "" {
		return false
	}
	if os.Getenv("AUDIUSD_STORAGE_ENABLED") == "true" {
		return true
	}
	return os.Getenv("creatorNodeEndpoint") != ""
}

func keyGen() (string, string) {
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		log.Fatalf("Failed to generate private key: %v", err)
	}
	privateKeyBytes := crypto.FromECDSA(privateKey)
	return hex.EncodeToString(privateKeyBytes), crypto.PubkeyToAddress(privateKey.PublicKey).Hex()
}

func getEnvString(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func hasSuffix(domain string, suffixes []string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(domain, suffix) {
			return true
		}
	}
	return false
}

func getHealthCheckResponse(hostUrl *url.URL) map[string]interface{} {
	response := map[string]interface{}{
		"git":       os.Getenv("GIT_SHA"),
		"hostname":  hostUrl.Hostname(),
		"timestamp": time.Now().UTC(),
		"uptime":    time.Since(startTime).String(),
		// TODO: legacy version data for uptime health check
		"data": map[string]interface{}{
			"version": version.Version.Version,
		},
	}

	storageResponse := map[string]interface{}{
		"enabled": isStorageEnabled(),
	}

	if isStorageEnabled() {
		resp, err := http.Get("http://localhost:1991/health_check")
		if err == nil {
			defer resp.Body.Close()
			var storageHealth server.HealthCheckResponse
			if err := json.NewDecoder(resp.Body).Decode(&storageHealth); err == nil {
				healthBytes, _ := json.Marshal(storageHealth)
				var tempResponse map[string]interface{}
				json.Unmarshal(healthBytes, &tempResponse)

				// TODO: remove cruft as we favor comet status for peering
				if data, ok := tempResponse["data"].(map[string]interface{}); ok {
					for k, v := range data {
						if k != "signers" && k != "unreachablePeers" {
							storageResponse[k] = v
						}
					}
					delete(tempResponse, "data")
				}

				for k, v := range tempResponse {
					storageResponse[k] = v
				}

				storageResponse["enabled"] = true
			}
		}
	}
	response["storage"] = storageResponse

	resp, err := http.Get("http://localhost:26659/console/health_check")
	if err == nil {
		defer resp.Body.Close()
		var coreHealth console.HealthCheckResponse
		if err := json.NewDecoder(resp.Body).Decode(&coreHealth); err == nil {
			// TODO: remove cruft
			healthBytes, _ := json.Marshal(coreHealth)
			var coreMap map[string]interface{}
			json.Unmarshal(healthBytes, &coreMap)
			delete(coreMap, "git")
			response["core"] = coreMap
		}
	}

	return response
}
