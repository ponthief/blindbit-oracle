package config

import (
	"runtime"

	"github.com/setavenger/blindbit-lib/logging"
	"github.com/setavenger/blindbit-lib/utils"
)

// TaprootActivation
// todo might be inapplicable due to transactions that have taproot prevouts from before the activation
//
//	is relevant for the height-to-hash lookup in the db

var (
	LogLevel = "info"
)

const (
	ConfigFileName       string = "blindbit.toml"
	DefaultBaseDirectory string = "~/.blindbit-oracle"
)

var (
	TweaksOnly                  bool
	TweakIndexFullNoDust        bool
	TweakIndexFullIncludingDust bool
	TweaksCutThroughWithDust    bool
)

var (
	RpcEndpoint  = "http://127.0.0.1:8332" // default local node
	RestEndpoint = ""                      // default local node
	CookiePath   = ""
	RpcUser      = ""
	RpcPass      = ""

	BaseDirectory = ""

	HTTPHost = "127.0.0.1:8000"
	GRPCHost = "" // default value is empty (deactivated)
)

type chain int

const (
	Unknown chain = iota
	Mainnet
	Signet
	Regtest
	Testnet3
)

// control vars
var (
	SyncStartHeight uint32 = 842_579 // May 8, 2024, when BIP-352 was merged

	Chain = Unknown

	// SyncHeadersMaxPerCall how many headers will maximally be requested in one batched RPC call
	SyncHeadersMaxPerCall uint32 = 10_000

	// We default to max num cores - 2
	MaxCPUCores = max(1, runtime.NumCPU()-2)

	// MaxParallelRequests sets how many RPC calls will be made in parallel to the Node.
	// Waiting on the node, not computing, so it is worth running ahead of the
	// core count — the handlers below can only work on blocks that have landed.
	// Bounded by the node's own rpcworkqueue; lower it if the node starts
	// refusing calls.
	MaxParallelRequests uint16 = uint16(max(4, MaxCPUCores*2))
	// MaxParallelTweakComputations number of parallel processes which will be spawned in order to compute the tweaks for a given block.
	// This is elliptic-curve work, so it scales with cores and nothing else.
	//
	// Both of these used to default to 2 regardless of the machine, which left
	// an initial sync running at a fraction of the box's capacity unless the
	// operator happened to copy the example config.
	MaxParallelTweakComputations = MaxCPUCores

	// PruneFrequency every x blocks the data will be checked and pruned
	// possible routines: -remove utxos for 100% spent transaction
	PruneFrequency = 72

	// MaxRangeBlocks caps how many blocks one /range/* request may cover.
	//
	// A scanner walking the chain block by block spends nearly all of its time
	// waiting on round trips rather than on the oracle, so the range endpoints
	// let it ask for a span at once. The cap is what stops a single request
	// from turning into an unbounded amount of work; responses are streamed, so
	// it bounds latency and the client's buffer rather than the server's memory.
	MaxRangeBlocks uint32 = 100
)

// one has to call SetDirectories otherwise config.DBPath will be empty
var (
	DBPathHeaders              string
	DBPathHeadersInv           string // for height to blockHash mapping
	DBPathFilters              string
	DBPathTweaks               string
	DBPathTweakIndex           string
	DBPathUTXOs                string
	DBPathTweakIndexDust       string
	DBPathSpentOutpointsIndex  string
	DBPathSpentOutpointsFilter string
)

// NumsH = 0x50929b74c1a04954b78b4b6035e97a5e078a5a0f28ec96d547bfee9ace803ac0
var NumsH = []byte{80, 146, 155, 116, 193, 160, 73, 84, 183, 139, 75, 96, 53, 233, 122, 94, 7, 138, 90, 15, 40, 236, 150, 213, 71, 191, 238, 154, 206, 128, 58, 192}

func SetDirectories() {
	BaseDirectory = utils.ResolvePath(BaseDirectory)
}

func HeaderMustSyncHeight() uint32 {
	switch Chain {
	case Mainnet:
		// height based on heuristic checks to see where no old taproot style coins were locked
		return 500_000
	case Signet:
		return 1
	case Regtest:
		return 1
	case Testnet3:
		return 1
	case Unknown:
		logging.L.Panic().Msg("chain not defined")
		return 0
	default:
		return 1
	}
}

func ChainToString(c chain) string {
	switch c {
	case Mainnet:
		return "main"
	case Signet:
		return "signet"
	case Regtest:
		return "regtest"
	case Testnet3:
		return "testnet"
	default:
		logging.L.Panic().Msg("chain not defined")
		return ""
	}

}
