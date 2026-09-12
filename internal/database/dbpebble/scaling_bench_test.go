package dbpebble

import (
	"fmt"
	"testing"
)

// How the per-block read cost scales with the size of the database.
//
// The earlier benchmarks used an eight-block store, which is not a database —
// it is one or two LSM levels with everything in cache, and it flatters any
// access pattern. A real oracle holds every block since BIP-352 activation, so
// the index is deep and the block cache holds a small fraction of it. The
// question these answer is whether the per-block read path stays flat as the
// store grows, or whether its cost is a function of how much else is in there.
//
// It matters because /tweaks and /utxos are keyed by txid: reading one block
// means a seek per transaction, scattered across the whole keyspace. The
// compute index is keyed by height, so reading one block is a contiguous scan.
// On a small store both look fine.

func benchScaling(b *testing.B, nBlocks int) {
	s, blockhash := benchStore(b, nBlocks)
	target := uint32(nBlocks/2 + 1)

	b.Run(fmt.Sprintf("blocks=%d/tweaks+utxos_by_txid", nBlocks), func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := s.TweaksForBlockAll(blockhash); err != nil {
				b.Fatal(err)
			}
			if _, err := s.FetchOutputsAll(blockhash, uint32(nBlocks)); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run(fmt.Sprintf("blocks=%d/compute_index_by_height", nBlocks), func(b *testing.B) {
		rows, err := s.FetchComputeIndex(target)
		if err != nil {
			b.Fatal(err)
		}
		if len(rows) == 0 {
			b.Fatal("compute index empty; the comparison would be meaningless")
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := s.FetchComputeIndex(target); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkScaling8(b *testing.B)   { benchScaling(b, 8) }
func BenchmarkScaling64(b *testing.B)  { benchScaling(b, 64) }
func BenchmarkScaling256(b *testing.B) { benchScaling(b, 256) }
