package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A config that names only the keys an operator must set. Everything else has
// to come from defaults — which is exactly the case that used to leave the
// indexer with no workers.
const minimalConfig = `
chain = "signet"
core_rpc_endpoint = ""
`

func TestDefaultsKeepIndexerRunnable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blindbit.toml")
	if err := os.WriteFile(path, []byte(minimalConfig), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	LoadConfigs(path)

	// The values the sync loop turns into goroutine counts and channel sizes.
	// At zero, SyncBlocks starts no handlers and nothing drains the block
	// channel, so the indexer reports no error and indexes nothing.
	if MaxParallelTweakComputations < 1 {
		t.Errorf("MaxParallelTweakComputations = %d; with no handler goroutines the indexer stalls",
			MaxParallelTweakComputations)
	}
	if MaxParallelRequests < 1 {
		t.Errorf("MaxParallelRequests = %d; no block would ever be pulled", MaxParallelRequests)
	}
	if MaxCPUCores < 1 {
		t.Errorf("MaxCPUCores = %d; GOMAXPROCS would be set to a non-positive value", MaxCPUCores)
	}

	// Indexing from 0 means re-deriving several hundred thousand pre-BIP-352
	// blocks that cannot contain a silent payment.
	if SyncStartHeight < 1 {
		t.Errorf("SyncStartHeight = %d; the sync would start from genesis", SyncStartHeight)
	}
}

// An explicit zero in a config file has to be clamped too, not just an absent
// key — it reaches the same code and stalls the same way.
func TestExplicitZeroesAreClamped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blindbit.toml")
	body := `
chain = "signet"
core_rpc_endpoint = ""
max_parallel_tweak_computations = 0
max_parallel_requests = 0
max_cpu_cores = 0
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	LoadConfigs(path)

	if MaxParallelTweakComputations < 1 {
		t.Errorf("MaxParallelTweakComputations = %d, want >= 1", MaxParallelTweakComputations)
	}
	if MaxParallelRequests < 1 {
		t.Errorf("MaxParallelRequests = %d, want >= 1", MaxParallelRequests)
	}
	if MaxCPUCores < 1 {
		t.Errorf("MaxCPUCores = %d, want >= 1", MaxCPUCores)
	}
}
