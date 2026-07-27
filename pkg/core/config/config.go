package config

import (
	"crypto/ecdsa"
	"fmt"
	"log"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/env"
	"github.com/OpenAudio/go-openaudio/pkg/rewards"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/types"
)

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

	ProdAcdcChainID  = 31524
	StageAcdcChainID = 1056801
	DevAcdcChainID   = 1337

	ProdEthRpc  = "https://eth-validator.audius.co"
	StageEthRpc = "https://eth-validator.staging.audius.co"
	DevEthRpc   = "http://eth-ganache:8545"

	DbURL = "postgresql://postgres:postgres@localhost:5432/openaudio"

	ProdDashboardURL  = "https://dashboard.audius.org"
	StageDashboardURL = "https://dashboard.staging.audius.org"
	DevDashboardURL   = "http://localhost"

	DefaultCoreRootDir = "/data/core"

	DefaultStateSyncSnapshotMinFreeBytes int64 = 80 * 1024 * 1024 * 1024 // 80 GiB
)

const (
	// Mainnet bootstrap peers. Keep this broad: every validator starts from this
	// list before PEX fills the address book, so a stale or tiny list becomes a
	// consensus-gossip availability risk.
	ProdPersistentPeers = "326d405aba6eab9df677ddf62d1331638e99da91@34.71.91.82:26656," +
		"edf0b62f900c6319fdb482b0379b91b8a3c0d773@104.154.119.194:26656," +
		"0b6bade75a38b6e7468d795dfdc7b2cf9f717255@val003.open-audio-validator.com:26656," +
		"0c196c272a34ad51ab563253234e71b664b309da@val007.open-audio-validator.com:26656," +
		"12abeaff908616575cec416af2c79c540bee63e1@audius-creator-4.theblueprint.xyz:26656," +
		"1375316fb25560f97bf7cf6e02cede670d840618@audius-creator-7.theblueprint.xyz:26656," +
		"13bcf9b4c1df378f4d2de4fb8801cabfa0a01a11@audius-creator-8.theblueprint.xyz:26656," +
		"29156c622bd3e4183c3994a435da878c0cdc9fa6@val016.open-audio-validator.com:26656," +
		"29d7892d176fb6f6a3740fdb753a618c612b9f0b@val011.open-audio-validator.com:26656," +
		"2ec3f5d35b751db7c38d008f965cc268a139a15d@audius-content-2.figment.io:26656," +
		"32b088725b4b82c3604cd05dc1f5ea3c8c09c5ba@audius-creator-5.theblueprint.xyz:26656," +
		"3317946736f9e99c01bcfeeccd44c41b0d53caee@val005.open-audio-validator.com:26656," +
		"33623aa633af52ef4d692320de0d22d1897fbbb9@audius-figment-1-validator-19.figment.io:26656," +
		"34df66133ab8e1e761d1e5a60c7453683dcb0ba7@audius-creator-9.theblueprint.xyz:26656," +
		"3714f1a5753d776e628be0524897be5a962db97e@audius-creator-6.theblueprint.xyz:26656," +
		"3bd77cde0aa19b7d0370c1b4d0d26cde8ac38aa9@audius-creator-3.theblueprint.xyz:26656," +
		"4868de2a01f29367796063cd87ed8d93b9866d84@val010.open-audio-validator.com:26656," +
		"4a1e3fd9a5a1c982b0c7372abb80305b5b4cceee@audius-creator-11.theblueprint.xyz:26656," +
		"51fc74a91a1daef3834e3028e3592d03633c44ed@audius-creator-15.theblueprint.xyz:26656," +
		"56049970fbad44d540b8bef6118800433d269049@audius-content-12.figment.io:26656," +
		"568224b2a1957bf45d9ce6835b9bbf346d0e7424@audius-creator-14.theblueprint.xyz:26656," +
		"59f72f7f31155f850181fea0c525073c74b93741@audius-content-3.figment.io:26656," +
		"5cba61b158b3b23705d859ddc98150a1ccb79b1d@cn1.shakespearetech.com:26656," +
		"61cc1a7db1c91fd0ff9c3670cf5e03939fa53a78@val002.open-audio-validator.com:26656," +
		"6210e9689aa8b3539b2191ea44879dbca7ca6691@audius-creator-17.theblueprint.xyz:26656"
	StagePersistentPeers = "f277f58522627a5cb890aececed8f08e7f13e097@35.193.20.31:26656,6a5d8207ed912eaa60cdfb8181fa97587d41dd1c@34.121.162.132:26656,8f27745ad44e08f449728960fa67827eb9477cf2@34.30.203.99:26656,96bba6b462e35f83866fbac271bfcee0a96d68e8@34.9.143.36:26656,1eec5742f64fb243d22594e4143e14e77a38f232@34.28.231.197:26656,2da43f6e1b5614ea8fc8b7e89909863033ca6a27@34.123.76.111:26656"
	DevPersistentPeers   = "ffad25668e060a357bbe534c8b7e5b4e1274368b@openaudio-1:26656"
)

const (
	ProdStateSyncRpcs  = "https://creatornode.audius.co,https://rpc.audius.co,https://v.monophonic.digital"
	StageStateSyncRpcs = "https://creatornode11.audius.co,https://creatornode5.audius.co"
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
	RunDownMigration            bool
	SlaRollupInterval           int
	ValidatorVotingPower        int
	ValidatorPurgeMinValidators int
	ValidatorWardenIntervalMins int // how often the validator warden checks for underperformance (minutes)
	UseHttpsForSdk              bool

	StateSync *StateSyncConfig

	/* Entity Manager Config */
	AcdcEntityManagerAddress string
	AcdcChainID              uint

	/* Derived Config */
	GenesisFile *types.GenesisDoc
	EthereumKey *ecdsa.PrivateKey
	CometKey    *ed25519.PrivKey
	Rewards     []rewards.Reward

	/* Attestation Thresholds */
	AttRegistrationMin     int // minimum number of attestations needed to register a new node
	AttRegistrationRSize   int // rendezvous size for registration attestations (should be >= to AttRegistrationMin)
	AttDeregistrationMin   int // minimum number of attestations needed to deregister a node
	AttDeregistrationRSize int // rendezvous size for deregistration attestations (should be >= to AttDeregistrationMin)

	/* Feature flags */
	ProgrammableDistributionEnabled bool
	SkipEthRegistration             bool
	EnableETL                       bool
	EnableExplorer                  bool
	EnableGRPCReflection            bool
}

func (c *Config) IsDev() bool {
	return c.Environment == "dev"
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
	// minimum free bytes required on the snapshot disk before creating snapshots
	SnapshotMinFreeBytes int64
}

func ReadConfig() (*Config, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Failed to get user home directory: %v", err)
	}

	var cfg Config
	// comet config
	cfg.CometLogLevel = env.Get("statesync:info,p2p:none,mempool:none,rpc:none,*:error", "OPENAUDIO_COMET_LOG_LEVEL", "audius_comet_log_level")
	cfg.RootDir = env.Get(homeDir+"/.audiusd", "OPENAUDIO_CORE_ROOT_DIR", "audius_core_root_dir")
	cfg.RPCladdr = env.Get("unix:///tmp/cometbft.rpc.sock", "OPENAUDIO_RPC_LADDR", "rpcLaddr")
	cfg.P2PLaddr = env.Get("tcp://0.0.0.0:26656", "OPENAUDIO_P2P_LADDR", "p2pLaddr")

	cfg.GRPCladdr = env.Get("0.0.0.0:50051", "OPENAUDIO_GRPC_LADDR", "grpcLaddr")
	cfg.CoreServerAddr = env.Get("0.0.0.0:26659", "OPENAUDIO_CORE_SERVER_ADDR", "coreServerAddr")

	// allow up to 200 inbound connections
	cfg.MaxInboundPeers = env.GetInt(200, "OPENAUDIO_MAX_INBOUND_PEERS", "maxInboundPeers")
	// actively connect to 50 peers
	cfg.MaxOutboundPeers = env.GetInt(50, "OPENAUDIO_MAX_OUTBOUND_PEERS", "maxOutboundPeers")

	// (default) approximately one week of blocks
	cfg.RetainHeight = int64(env.GetInt(604800, "OPENAUDIO_RETAIN_HEIGHT", "retainHeight"))
	cfg.Archive = env.Get("false", "OPENAUDIO_ARCHIVE", "archive") == "true"

	cfg.AttRegistrationMin = 5
	cfg.AttRegistrationRSize = 15
	cfg.AttDeregistrationMin = 5
	cfg.AttDeregistrationRSize = 15

	cfg.LogLevel = GetLogLevel()
	cfg.Environment = GetRuntimeEnvironment()
	cfg.ProgrammableDistributionEnabled = common.IsProgrammableDistributionEnabled(cfg.Environment)

	cfg.SkipEthRegistration = env.Get("false", "OPENAUDIO_SKIP_ETH_REGISTRATION", "skipEthRegistration") == "true"
	// Defaults to false for nodes that opt out of indexing, but the Go ETL is
	// the production indexer and OPENAUDIO_ETL_ENABLED is set to true in
	// production deployments.
	cfg.EnableETL = env.Get("false", "OPENAUDIO_ETL_ENABLED") == "true"
	cfg.EnableExplorer = env.Get("false", "OPENAUDIO_EXPLORER_ENABLED") == "true"
	cfg.EnableGRPCReflection = env.Get("false", "OPENAUDIO_GRPC_REFLECTION_ENABLED") == "true"

	ssRpcServers := ""
	switch cfg.Environment {
	case "prod", "production":
		ssRpcServers = ProdStateSyncRpcs
	case "stage", "staging":
		ssRpcServers = StageStateSyncRpcs
	}

	cfg.StateSync = &StateSyncConfig{
		ServeSnapshots:       env.Get("false", "OPENAUDIO_STATE_SYNC_SERVE_SNAPSHOTS", "stateSyncServeSnapshots") == "true",
		Enable:               env.Get("true", "OPENAUDIO_STATE_SYNC_ENABLE", "stateSyncEnable") == "true",
		Keep:                 env.GetInt(2, "OPENAUDIO_STATE_SYNC_KEEP", "stateSyncKeep"),
		BlockInterval:        int64(env.GetInt(100000, "OPENAUDIO_STATE_SYNC_BLOCK_INTERVAL", "stateSyncBlockInterval")),
		ChunkFetchers:        int32(env.GetInt(10, "OPENAUDIO_STATE_SYNC_CHUNK_FETCHERS", "stateSyncChunkFetchers")),
		SnapshotMinFreeBytes: env.GetInt64(DefaultStateSyncSnapshotMinFreeBytes, "OPENAUDIO_STATE_SYNC_SNAPSHOT_MIN_FREE_BYTES", "stateSyncSnapshotMinFreeBytes"),
		RPCServers:           strings.Split(env.Get(ssRpcServers, "OPENAUDIO_STATE_SYNC_RPC_SERVERS", "stateSyncRPCServers"), ","),
	}
	if cfg.StateSync.SnapshotMinFreeBytes < 0 {
		cfg.StateSync.SnapshotMinFreeBytes = 0
	}

	cfg.EthRPCUrl = GetEthRPC()

	delegatePrivateKey := env.String("OPENAUDIO_DELEGATE_PRIVATE_KEY", "delegatePrivateKey")
	// Strip 0x prefix if present
	if delegatePrivateKey != "" && (strings.HasPrefix(delegatePrivateKey, "0x") || strings.HasPrefix(delegatePrivateKey, "0X")) {
		delegatePrivateKey = delegatePrivateKey[2:]
	}

	cfg.PSQLConn = env.Get("postgresql://postgres:postgres@localhost:5432/openaudio", "OPENAUDIO_DB_URL", "dbUrl")
	nodeEndpoint := env.String("OPENAUDIO_NODE_ENDPOINT", "nodeEndpoint")

	if nodeEndpoint != "" {
		parsedURL, err := url.Parse(nodeEndpoint)
		if err != nil {
			return nil, fmt.Errorf("invalid nodeEndpoint URL: %v", err)
		}

		if parsedURL.Port() != "" {
			return nil, fmt.Errorf("nodeEndpoint must not include a port number. Remove ':port' from the URL (e.g., use 'https://example.com' instead of 'https://example.com:443')")
		}
		hostname := parsedURL.Hostname()
		if hostname == "" {
			return nil, fmt.Errorf("nodeEndpoint must include a valid hostname")
		}
		if !isFQDN(hostname) {
			return nil, fmt.Errorf("invalid hostname in nodeEndpoint: %q is not a valid FQDN", hostname)
		}
	}
	cfg.NodeEndpoint = nodeEndpoint

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
	cfg.UseHttpsForSdk = env.Get("true", "OPENAUDIO_USE_HTTPS_FOR_SDK", "useHttpsForSdk") == "true"
	cfg.ExternalAddress = env.String("OPENAUDIO_EXTERNAL_ADDRESS", "externalAddress")
	cfg.Seeds = env.Get("", "OPENAUDIO_SEEDS", "seeds")
	cfg.EthRegistryAddress = GetRegistryAddress()

	switch cfg.Environment {
	case "prod", "production", "mainnet":
		cfg.PersistentPeers = env.Get(ProdPersistentPeers, "OPENAUDIO_PERSISTENT_PEERS", "persistentPeers")
		cfg.SlaRollupInterval = mainnetRollupInterval
		cfg.ValidatorVotingPower = mainnetValidatorVotingPower
		cfg.ValidatorPurgeMinValidators = env.GetInt(30, "OPENAUDIO_VALIDATOR_PURGE_MIN_VALIDATORS")
		cfg.ValidatorWardenIntervalMins = env.GetInt(60, "OPENAUDIO_VALIDATOR_WARDEN_INTERVAL_MINS")
		cfg.Rewards = MakeRewards(ProdClaimAuthorities, ProdRewardExtensions)
		cfg.AcdcChainID = ProdAcdcChainID
		cfg.AcdcEntityManagerAddress = ProdAcdcAddress

	case "stage", "staging", "testnet":
		cfg.PersistentPeers = env.Get(StagePersistentPeers, "OPENAUDIO_PERSISTENT_PEERS", "persistentPeers")
		cfg.SlaRollupInterval = testnetRollupInterval
		cfg.ValidatorVotingPower = testnetValidatorVotingPower
		cfg.ValidatorPurgeMinValidators = env.GetInt(30, "OPENAUDIO_VALIDATOR_PURGE_MIN_VALIDATORS")
		cfg.ValidatorWardenIntervalMins = env.GetInt(60, "OPENAUDIO_VALIDATOR_WARDEN_INTERVAL_MINS")
		cfg.Rewards = MakeRewards(StageClaimAuthorities, StageRewardExtensions)
		cfg.AcdcChainID = StageAcdcChainID
		cfg.AcdcEntityManagerAddress = StageAcdcAddress

	case "dev", "development", "devnet", "local", "sandbox":
		cfg.PersistentPeers = env.Get(DevPersistentPeers, "OPENAUDIO_PERSISTENT_PEERS", "persistentPeers")
		cfg.AddrBookStrict = false
		cfg.SlaRollupInterval = devnetRollupInterval
		cfg.ValidatorVotingPower = devnetValidatorVotingPower
		cfg.ValidatorPurgeMinValidators = env.GetInt(3, "OPENAUDIO_VALIDATOR_PURGE_MIN_VALIDATORS")
		cfg.ValidatorWardenIntervalMins = env.GetInt(2, "OPENAUDIO_VALIDATOR_WARDEN_INTERVAL_MINS")
		cfg.Rewards = MakeRewards(DevClaimAuthorities, DevRewardExtensions)
		cfg.AcdcChainID = DevAcdcChainID
		cfg.AcdcEntityManagerAddress = DevAcdcAddress
	}

	// Disable ssl for local postgres db connection
	if !strings.HasSuffix(cfg.PSQLConn, "?sslmode=disable") && isLocalDbUrlRegex.MatchString(cfg.PSQLConn) {
		cfg.PSQLConn += "?sslmode=disable"
	}

	return &cfg, nil
}

// Check if the hostname is a valid FQDN (Fully Qualified Domain Name)
// which means it includes a protocol, valid hostname, and optional port number.
// https://regex101.com/r/kIowvx/2
func isFQDN(hostname string) bool {
	fqdnRegex := regexp.MustCompile(`(?:^|[ \t])((https?:\/\/)?(?:localhost|[\w-]+(?:\.[\w-]+)+)(:\d+)?(\/\S*)?)`)
	return fqdnRegex.MatchString(hostname)
}

// Deprecated: Use env.Get instead.
func GetEnvWithDefault(key, defaultValue string) string {
	return env.Get(defaultValue, key)
}

func GetEthRPC() string {
	return env.Get(DefaultEthRPC(), "OPENAUDIO_ETH_PROVIDER_URL", "ethProviderUrl")
}

func GetDbURL() string {
	dbUrl := env.Get(DbURL, "OPENAUDIO_DB_URL", "dbUrl")
	if !strings.HasSuffix(dbUrl, "?sslmode=disable") && isLocalDbUrlRegex.MatchString(dbUrl) {
		dbUrl += "?sslmode=disable"
	}
	return dbUrl
}

func GetRegistryAddress() string {
	return env.Get(DefaultRegistryAddress(), "OPENAUDIO_ETH_REGISTRY_ADDRESS", "ethRegistryAddress")
}

func GetRuntimeEnvironment() string {
	return env.Get("prod", "OPENAUDIO_ENV")
}

func GetLogLevel() string {
	return env.Get("info", "OPENAUDIO_LOG_LEVEL")
}

func DefaultEthRPC() string {
	switch GetRuntimeEnvironment() {
	case "prod", "production", "mainnet":
		return ProdEthRpc
	case "stage", "staging", "testnet":
		return StageEthRpc
	case "dev", "development", "devnet", "local", "sandbox":
		return DevEthRpc
	default:
		return ""
	}
}

func DefaultRegistryAddress() string {
	switch GetRuntimeEnvironment() {
	case "prod", "production", "mainnet":
		return ProdRegistryAddress
	case "stage", "staging", "testnet":
		return StageRegistryAddress
	case "dev", "development", "devnet", "local", "sandbox":
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

func GetProtocolDashboardURL() string {
	switch GetRuntimeEnvironment() {
	case "prod", "production", "mainnet":
		return ProdDashboardURL
	case "stage", "staging", "testnet":
		return StageDashboardURL
	case "dev", "development", "devnet", "local", "sandbox":
		return DevDashboardURL
	default:
		return ""
	}
}
