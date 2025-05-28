package config

import (
	"crypto/ecdsa"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/rewards"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/types"
)

type NodeType = int

const (
	Discovery NodeType = iota
	Content
	Identity
)

const (
	ModuleConsole = "console"
	ModuleDebug   = "debug"
	ModulePprof   = "pprof"
	ModuleComet   = "comet"
	ModuleGraphQL = "graphql"
)

// once completely released, remove debug and comet
var defaultModules = []string{ModuleConsole, ModuleDebug, ModulePprof, ModuleComet, ModuleGraphQL}

type RollupInterval struct {
	BlockInterval int
}

const (
	ProdRegistryAddress  = "0xd976d3b4f4e22a238c1A736b6612D22f17b6f64C"
	StageRegistryAddress = "0xc682C2166E11690B64338e11633Cb8Bb60B0D9c0"
	DevRegistryAddress   = "0xABbfF712977dB51f9f212B85e8A4904c818C2b63"

	ProdAcdcAddress  = "0x1Cd8a543596D499B9b6E7a6eC15ECd2B7857Fd64"
	StageAcdcAddress = "0x1Cd8a543596D499B9b6E7a6eC15ECd2B7857Fd64"
	DevAcdcAddress   = "0x254dffcd3277C0b1660F6d42EFbB754edaBAbC2B"

	ProdEthRpc  = "https://eth.audius.co"
	StageEthRpc = "https://eth.staging.audius.co"
	DevEthRpc   = "http://eth-ganache:8545"
)

const (
	ProdPersistentPeers  = "edf0b62f900c6319fdb482b0379b91b8a3c0d773@35.223.56.100:26656,f0d79ce5eb91847db0a1b9ad4c8a15824710f9c3@34.121.217.14:26656,326d405aba6eab9df677ddf62d1331638e99da91@34.45.240.197:26656,53a2506dcf34b267c3e04bb63e0ee4f563c7850d@34.67.133.214:26656,a3a9659fdd6e25e41324764adc8029b486814533@34.46.116.59:26656,25a80eb8f8755d73ab9b4e0e5cf31dcc0b757aab@35.222.113.66:26656,2c176c34a2fa881b72acfedc1e3815710c4f1bd5@34.28.164.31:26656"
	StagePersistentPeers = "0f4be2aaa70e9570eee3485d8fa54502cf1a9fc0@34.67.210.7:26656,2f13439b2ee4c34bafe643f89575f40b7863a079@34.136.137.33:26656,c9b1ed3d3040e0c2ac70e3215f0ea9b16b401bca@34.68.24.207:26656,1eec5742f64fb243d22594e4143e14e77a38f232@34.71.167.168:26656,2da43f6e1b5614ea8fc8b7e89909863033ca6a27@35.208.173.168:26656"
	DevPersistentPeers   = "ffad25668e060a357bbe534c8b7e5b4e1274368b@audiusd-1:26656"
)

const (
	mainnetValidatorVotingPower = 10
	testnetValidatorVotingPower = 10
	devnetValidatorVotingPower  = 25
	mainnetRollupInterval       = 2048
	testnetRollupInterval       = 512
	devnetRollupInterval        = 16
)

const dbUrlLocalPattern string = `^postgresql:\/\/\w+:\w+@(db|localhost|postgres):.*`

var isLocalDbUrlRegex = regexp.MustCompile(dbUrlLocalPattern)

var Version string

type Config struct {
	/* Comet Config */
	RootDir          string
	RPCladdr         string
	P2PLaddr         string
	PSQLConn         string
	PersistentPeers  string
	Seeds            string
	ExternalAddress  string
	AddrBookStrict   bool
	MaxInboundPeers  int
	MaxOutboundPeers int
	CometLogLevel    string
	RetainHeight     int64

	/* Audius Config */
	Environment     string
	WalletAddress   string
	ProposerAddress string
	GRPCladdr       string
	CoreServerAddr  string
	NodeEndpoint    string
	Archive         bool
	LogLevel        string

	/* Ethereum Config */
	EthRPCUrl          string
	EthRegistryAddress string

	/* System Config */
	RunDownMigration     bool
	SlaRollupInterval    int
	ValidatorVotingPower int
	UseHttpsForSdk       bool

	StateSync *StateSyncConfig

	/* Derived Config */
	GenesisFile *types.GenesisDoc
	EthereumKey *ecdsa.PrivateKey
	CometKey    *ed25519.PrivKey
	NodeType    NodeType
	Rewards     []rewards.Reward

	/* Optional Modules */
	ConsoleModule bool
	DebugModule   bool
	CometModule   bool
	PprofModule   bool

	/* Attestation Thresholds */
	AttRegistrationMin     int // minimum number of attestations needed to register a new node
	AttRegistrationRSize   int // rendezvous size for registration attestations (should be >= to AttRegistrationMin)
	AttDeregistrationMin   int // minimum number of attestations needed to deregister a node
	AttDeregistrationRSize int // rendezvous size for deregistration attestations (should be >= to AttDeregistrationMin)

	/* Feature flags */
	ERNAccessControlEnabled bool
}

type StateSyncConfig struct {
	// will periodically save pg_dumps to disk and serve them to other nodes
	ServeSnapshots bool
	// will download pg_dumps from other nodes on initial sync
	Enable bool
	// list of rpc endpoints to download pg_dumps from
	RPCServers []string
	// number of snapshots to keep on disk
	Keep int
	// interval to save snapshots in blocks
	BlockInterval int64
	// number of chunk fetchers to use
	ChunkFetchers int32
}

func ReadConfig(logger *common.Logger) (*Config, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Failed to get user home directory: %v", err)
	}

	var cfg Config
	// comet config
	cfg.CometLogLevel = GetEnvWithDefault("audius_comet_log_level", "statesync:info,p2p:none,mempool:none,rpc:none,*:error")
	cfg.RootDir = GetEnvWithDefault("audius_core_root_dir", homeDir+"/.audiusd")
	cfg.RPCladdr = GetEnvWithDefault("rpcLaddr", "tcp://0.0.0.0:26657")
	cfg.P2PLaddr = GetEnvWithDefault("p2pLaddr", "tcp://0.0.0.0:26656")

	cfg.GRPCladdr = GetEnvWithDefault("grpcLaddr", "0.0.0.0:50051")
	cfg.CoreServerAddr = GetEnvWithDefault("coreServerAddr", "0.0.0.0:26659")

	// allow up to 200 inbound connections
	cfg.MaxInboundPeers = getEnvIntWithDefault("maxInboundPeers", 200)
	// actively connect to 50 peers
	cfg.MaxOutboundPeers = getEnvIntWithDefault("maxOutboundPeers", 50)

	// (default) approximately one week of blocks
	cfg.RetainHeight = int64(getEnvIntWithDefault("retainHeight", 604800))
	cfg.Archive = GetEnvWithDefault("archive", "false") == "true"

	cfg.AttRegistrationMin = 5
	cfg.AttRegistrationRSize = 10
	cfg.AttDeregistrationMin = 5
	cfg.AttDeregistrationRSize = 10

	cfg.LogLevel = GetEnvWithDefault("AUDIUSD_LOG_LEVEL", "info")

	cfg.StateSync = &StateSyncConfig{
		ServeSnapshots: GetEnvWithDefault("stateSyncServeSnapshots", "false") == "true",
		Enable:         GetEnvWithDefault("stateSyncEnable", "false") == "true",
		Keep:           getEnvIntWithDefault("stateSyncKeep", 6),
		BlockInterval:  int64(getEnvIntWithDefault("stateSyncBlockInterval", 100)),
		ChunkFetchers:  int32(getEnvIntWithDefault("stateSyncChunkFetchers", 10)),
		RPCServers:     strings.Split(GetEnvWithDefault("stateSyncRPCServers", ""), ","),
	}

	// check if discovery specific key is set
	isDiscovery := os.Getenv("audius_delegate_private_key") != ""
	var delegatePrivateKey string
	if isDiscovery {
		delegatePrivateKey = os.Getenv("audius_delegate_private_key")
		cfg.NodeType = Discovery
		cfg.Environment = os.Getenv("audius_discprov_env")
		cfg.NodeEndpoint = os.Getenv("audius_discprov_url")
		cfg.PSQLConn = GetEnvWithDefault("audius_db_url", "postgresql://postgres:postgres@localhost:5432/audius_discovery")
		cfg.EthRPCUrl = os.Getenv("audius_web3_eth_provider_url")
	} else {
		delegatePrivateKey = os.Getenv("delegatePrivateKey")
		cfg.NodeType = Content
		cfg.Environment = os.Getenv("MEDIORUM_ENV")
		cfg.PSQLConn = GetEnvWithDefault("dbUrl", "postgresql://postgres:postgres@localhost:5432/audius_creator_node")
		cfg.EthRPCUrl = os.Getenv("ethProviderUrl")
		cfg.NodeEndpoint = os.Getenv("creatorNodeEndpoint")
	}

	ethKey, err := common.EthToEthKey(delegatePrivateKey)
	if err != nil {
		return nil, fmt.Errorf("creating eth key %v", err)
	}
	cfg.EthereumKey = ethKey

	ethAddress := common.PrivKeyToAddress(ethKey)
	cfg.WalletAddress = ethAddress

	key, err := common.EthToCometKey(cfg.EthereumKey)
	if err != nil {
		return nil, fmt.Errorf("creating key %v", err)
	}
	cfg.CometKey = key

	cfg.AddrBookStrict = true
	cfg.UseHttpsForSdk = GetEnvWithDefault("useHttpsForSdk", "true") == "true"
	switch cfg.Environment {
	case "prod", "production", "mainnet":
		cfg.PersistentPeers = GetEnvWithDefault("persistentPeers", ProdPersistentPeers)
		cfg.EthRegistryAddress = ProdRegistryAddress
		if cfg.EthRPCUrl == "" {
			cfg.EthRPCUrl = ProdEthRpc
		}

		cfg.SlaRollupInterval = mainnetRollupInterval
		cfg.ValidatorVotingPower = mainnetValidatorVotingPower
		cfg.Rewards = MakeRewards(ProdClaimAuthorities, ProdRewardExtensions)
		cfg.ERNAccessControlEnabled = false

	case "stage", "staging", "testnet":
		cfg.PersistentPeers = GetEnvWithDefault("persistentPeers", StagePersistentPeers)
		cfg.EthRegistryAddress = StageRegistryAddress
		if cfg.EthRPCUrl == "" {
			cfg.EthRPCUrl = StageEthRpc
		}
		cfg.SlaRollupInterval = testnetRollupInterval
		cfg.ValidatorVotingPower = testnetValidatorVotingPower
		cfg.Rewards = MakeRewards(StageClaimAuthorities, StageRewardExtensions)
		cfg.ERNAccessControlEnabled = false

	case "dev", "development", "devnet", "local", "sandbox":
		cfg.PersistentPeers = GetEnvWithDefault("persistentPeers", DevPersistentPeers)
		cfg.ExternalAddress = os.Getenv("externalAddress")
		cfg.AddrBookStrict = false
		if cfg.EthRPCUrl == "" {
			cfg.EthRPCUrl = DevEthRpc
		}
		if cfg.EthRegistryAddress == "" {
			cfg.EthRegistryAddress = DevRegistryAddress
		}
		cfg.SlaRollupInterval = devnetRollupInterval
		cfg.ValidatorVotingPower = devnetValidatorVotingPower
		cfg.Rewards = MakeRewards(DevClaimAuthorities, DevRewardExtensions)
		cfg.ERNAccessControlEnabled = true
	}

	// Disable ssl for local postgres db connection
	if !strings.HasSuffix(cfg.PSQLConn, "?sslmode=disable") && isLocalDbUrlRegex.MatchString(cfg.PSQLConn) {
		cfg.PSQLConn += "?sslmode=disable"
	}

	enableModules(&cfg)

	return &cfg, nil
}

func enableModules(config *Config) {
	moduleSettings := defaultModules
	// TODO: set module settings from env var
	for _, module := range moduleSettings {
		switch module {
		case ModuleComet:
			config.CometModule = true
		case ModuleDebug:
			config.DebugModule = true
		case ModulePprof:
			config.PprofModule = true
		case ModuleConsole:
			config.ConsoleModule = true
		}
	}
}

func GetEnvWithDefault(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

func getEnvIntWithDefault(key string, defaultValue int) int {
	if value, exists := os.LookupEnv(key); exists {
		val, err := strconv.Atoi(value)
		if err == nil {
			return val
		}
		return defaultValue
	}
	return defaultValue
}

func DefaultEthRPC() string {
	env := os.Getenv("MEDIORUM_ENV")
	switch env {
	case "prod":
		return ProdEthRpc
	case "stage":
		return StageEthRpc
	case "dev":
		return DevEthRpc
	default:
		return ""
	}
}

func DefaultRegistryAddress() string {
	env := os.Getenv("MEDIORUM_ENV")
	switch env {
	case "prod":
		return ProdRegistryAddress
	case "stage":
		return StageRegistryAddress
	case "dev":
		return DevRegistryAddress
	default:
		return ""
	}
}

func (c *Config) RunDownMigrations() bool {
	return c.RunDownMigration
}

type SandboxVars struct {
	SdkEnvironment string
	EthChainID     uint64
	EthRpcURL      string
}

func (c *Config) NewSandboxVars(env ...string) *SandboxVars {
	environment := c.Environment
	if len(env) > 0 {
		environment = env[0]
	}
	var sandboxVars SandboxVars
	switch environment {
	case "prod":
		sandboxVars.SdkEnvironment = "production"
		sandboxVars.EthChainID = 31524
	case "stage":
		sandboxVars.SdkEnvironment = "staging"
		sandboxVars.EthChainID = 1056801
	default:
		sandboxVars.SdkEnvironment = "development"
		sandboxVars.EthChainID = 1337
	}

	sandboxVars.EthRpcURL = fmt.Sprintf("%s/core/erpc", c.NodeEndpoint)
	return &sandboxVars
}
