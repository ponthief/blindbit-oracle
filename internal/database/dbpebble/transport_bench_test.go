package dbpebble

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/setavenger/blindbit-lib/proto/pb"
	"github.com/setavenger/blindbit-lib/utils"
)

// What one block costs on the wire, by transport.
//
// The HTTP API and the gRPC API are not two encodings of the same thing. The
// REST scan path is /tweaks plus /utxos, both keyed by txid and both hex inside
// JSON. The gRPC service has no equivalent of either — it exposes the compute
// index, keyed by height, as protobuf. So "switch to gRPC" is really two
// changes at once, and this separates them: how much is the encoding, and how
// much is the different index.

func blockPayloads(tb testing.TB, s *Store, height uint32, blockhash []byte) (
	jsonTweaksUtxos int, protoScanData int, jsonComputeIndex int,
) {
	tb.Helper()

	// What the scanner moves today, as the handlers build it.
	tweakRows, err := s.TweaksForBlockAll(blockhash)
	if err != nil {
		tb.Fatal(err)
	}
	outputs, err := s.FetchOutputsAll(blockhash, height)
	if err != nil {
		tb.Fatal(err)
	}
	// Hex in JSON: 33-byte tweak -> 66 chars plus quotes and a comma.
	jsonTweaksUtxos = len(tweakRows) * (33*2 + 3)
	// {"txid":"..64..","vout":N,"amount":N,"pubkey":"..64.."}
	for range outputs {
		jsonTweaksUtxos += 64 + 64 + 45
	}

	// What gRPC moves: the compute index plus spent outputs, one message.
	computeIndex, err := s.FetchComputeIndex(height)
	if err != nil {
		tb.Fatal(err)
	}
	spent, err := s.FetchSpentOutputsShort(blockhash)
	if err != nil {
		tb.Fatal(err)
	}
	msg := &pb.BlockScanDataShortResponse{
		BlockIdentifier: &pb.BlockIdentifier{
			BlockHash:   utils.ReverseBytesCopy(blockhash),
			BlockHeight: uint64(height),
		},
		CompIndex:    computeIndex,
		SpentOutputs: spent,
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		tb.Fatal(err)
	}
	protoScanData = len(raw)

	// The same compute index as JSON, to separate encoding from index choice.
	for _, item := range computeIndex {
		jsonComputeIndex += 64 + 66 + len(item.OutputsShort)*2 + 40
	}

	return
}

func TestWireCostByTransport(t *testing.T) {
	const blocks = 128
	s, blockhash := benchStore(t, blocks)
	height := uint32(blocks/2 + 1)

	jsonTU, protoSD, jsonCI := blockPayloads(t, s, height, blockhash)

	t.Logf("block shape: %d txs, %d tweaked, %d outputs each",
		benchTxsPerBlock, benchTxsPerBlock/benchTweakedPerRate, benchOutsPerTweaked)
	t.Logf("")
	t.Logf("%-46s %10s  %s", "per block, on the wire", "bytes", "vs today")
	t.Logf("%-46s %10d  %s", "REST today: /tweaks + /utxos (hex JSON)", jsonTU, "1.0x")
	t.Logf("%-46s %10d  %.1fx smaller", "same data, compute index as JSON", jsonCI,
		float64(jsonTU)/float64(jsonCI))
	t.Logf("%-46s %10d  %.1fx smaller", "gRPC: compute index + spent (protobuf)",
		protoSD, float64(jsonTU)/float64(protoSD))
	t.Logf("")
	t.Logf("over 119 blocks: %.1f MB -> %.1f MB",
		float64(jsonTU)*119/(1<<20), float64(protoSD)*119/(1<<20))
}

// The server-side cost of producing each, which on a shared machine competes
// with the scanner's own matching for CPU.
func BenchmarkBuildComputeIndexProto(b *testing.B) {
	s, blockhash := benchStore(b, 64)
	height := uint32(33)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ci, err := s.FetchComputeIndex(height)
		if err != nil {
			b.Fatal(err)
		}
		spent, err := s.FetchSpentOutputsShort(blockhash)
		if err != nil {
			b.Fatal(err)
		}
		msg := &pb.BlockScanDataShortResponse{
			BlockIdentifier: &pb.BlockIdentifier{BlockHeight: uint64(height)},
			CompIndex:       ci,
			SpentOutputs:    spent,
		}
		if _, err := proto.Marshal(msg); err != nil {
			b.Fatal(err)
		}
	}
}
