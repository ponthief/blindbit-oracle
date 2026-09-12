package dbpebble

import (
	"encoding/binary"
	"os"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/rs/zerolog"
	"github.com/setavenger/blindbit-lib/logging"
	"github.com/setavenger/blindbit-oracle/internal/database"
)

// The package logs at trace level by default, which at benchmark rates buries
// the results and distorts them.
func TestMain(m *testing.M) {
	logging.SetLogLevel(zerolog.Disabled)
	os.Exit(m.Run())
}

// A block shaped like a busy mainnet one: most transactions leave only spend
// records behind, a minority carry a tweak and therefore outputs. Those are the
// proportions that decide how much of the read path is wasted work, so the
// benchmark has to reproduce them rather than index a uniform block.
const (
	benchTxsPerBlock    = 3000
	benchTweakedPerRate = 6 // one tx in six has a tweak
	benchOutsPerTweaked = 2
	benchInsPerTx       = 2
)

func synthHash(kind byte, n uint32) []byte {
	h := make([]byte, 32)
	h[0] = kind
	binary.BigEndian.PutUint32(h[1:5], n)
	// Spread the rest so txids do not all share a long common prefix, which
	// would give the LSM unrealistically good locality.
	for i := 5; i < 32; i++ {
		h[i] = byte(n*2654435761 + uint32(i)*97)
	}
	return h
}

func benchBlock(height uint32) *database.DBBlock {
	var blockHash chainhash.Hash
	copy(blockHash[:], synthHash(0xbb, height))

	txs := make([]*database.Tx, 0, benchTxsPerBlock)
	for i := 0; i < benchTxsPerBlock; i++ {
		txid := synthHash(0x11, height*benchTxsPerBlock+uint32(i))

		ins := make([]*database.In, 0, benchInsPerTx)
		for j := 0; j < benchInsPerTx; j++ {
			ins = append(ins, &database.In{
				SpendTxid: txid,
				Idx:       uint32(j),
				PrevTxid:  synthHash(0x22, height*7919+uint32(i*benchInsPerTx+j)),
				PrevVout:  uint32(j),
				Pubkey:    synthHash(0x33, uint32(i*benchInsPerTx+j))[:32],
			})
		}

		tx := &database.Tx{Txid: txid, Ins: ins}
		if i%benchTweakedPerRate == 0 {
			var tweak [33]byte
			tweak[0] = 0x02
			copy(tweak[1:], synthHash(0x44, uint32(i)))
			tx.Tweak = &tweak
			for v := 0; v < benchOutsPerTweaked; v++ {
				tx.Outs = append(tx.Outs, &database.Output{
					Txid:   txid,
					Vout:   uint32(v),
					Amount: uint64(10_000 + v),
					Pubkey: synthHash(0x55, uint32(i*benchOutsPerTweaked+v))[:32],
				})
			}
		}
		txs = append(txs, tx)
	}
	return &database.DBBlock{Height: height, Hash: &blockHash, Txs: txs}
}

// benchStore builds an on-disk store holding nBlocks synthetic blocks and
// returns it with the hash of a block in the middle — the middle so lookups are
// not answered disproportionately from the memtable or from the newest sstable.
func benchStore(tb testing.TB, nBlocks int) (*Store, []byte) {
	tb.Helper()

	opts := (&pebble.Options{}).EnsureDefaults()
	opts.FS = vfs.Default
	opts.Cache = pebble.NewCache(256 << 20)
	defer opts.Cache.Unref()

	db, err := pebble.Open(tb.TempDir(), opts)
	if err != nil {
		tb.Fatalf("open pebble: %v", err)
	}
	tb.Cleanup(func() { _ = db.Close() })

	s := NewStore(db)
	for h := 1; h <= nBlocks; h++ {
		if err := s.ApplyBlock(benchBlock(uint32(h))); err != nil {
			tb.Fatalf("apply block %d: %v", h, err)
		}
	}
	if err := s.FlushBatch(true); err != nil {
		tb.Fatalf("flush: %v", err)
	}
	s.WaitForPendingCommits()
	// Compact so the benchmark measures steady-state reads rather than a
	// pathological L0 stack left over from the bulk load.
	if err := db.Compact([]byte{0x00}, []byte{0xff, 0xff}, true); err != nil {
		tb.Fatalf("compact: %v", err)
	}

	target := uint32(nBlocks/2 + 1)
	return s, synthHash(0xbb, target)
}

func BenchmarkTweaksForBlockAll(b *testing.B) {
	s, blockhash := benchStore(b, 8)
	rows, err := s.TweaksForBlockAll(blockhash)
	if err != nil {
		b.Fatal(err)
	}
	b.Logf("tweaks in block: %d", len(rows))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := s.TweaksForBlockAll(blockhash)
		if err != nil {
			b.Fatal(err)
		}
		if len(out) != len(rows) {
			b.Fatalf("got %d tweaks, want %d", len(out), len(rows))
		}
	}
}

func BenchmarkFetchOutputsAll(b *testing.B) {
	s, blockhash := benchStore(b, 8)
	outs, err := s.FetchOutputsAll(blockhash, 8)
	if err != nil {
		b.Fatal(err)
	}
	b.Logf("outputs in block: %d", len(outs))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := s.FetchOutputsAll(blockhash, 8)
		if err != nil {
			b.Fatal(err)
		}
		if len(out) != len(outs) {
			b.Fatalf("got %d outputs, want %d", len(out), len(outs))
		}
	}
}

// BenchmarkBlockRead is the pair of calls a single /tweaks + /utxos scan of one
// block actually costs the oracle.
func BenchmarkBlockRead(b *testing.B) {
	s, blockhash := benchStore(b, 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.TweaksForBlockAll(blockhash); err != nil {
			b.Fatal(err)
		}
		if _, err := s.FetchOutputsAll(blockhash, 8); err != nil {
			b.Fatal(err)
		}
	}
}
