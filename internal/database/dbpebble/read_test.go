package dbpebble

import (
	"bytes"
	"testing"

	"github.com/setavenger/blindbit-oracle/internal/database"
)

// The naive implementations these functions replaced, kept verbatim as the
// reference. A scanner that misses an output reports someone's money as gone,
// so "faster" is only acceptable alongside "identical", and identical has to
// mean the same rows in the same order, not merely the same set.

func refTweaksForBlockAll(s *Store, blockhash []byte) ([]*database.TweakRow, error) {
	txids, err := s.BlockTxids(blockhash)
	if err != nil {
		return nil, err
	}
	out := make([]*database.TweakRow, 0, len(txids))
	for _, txid := range txids {
		tweak, ok, err := s.LoadTweak(txid)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		row := new(database.TweakRow)
		copy(row.Txid[:], txid)
		copy(row.Tweak[:], tweak)
		out = append(out, row)
	}
	return out, nil
}

func refFetchOutputs(s *Store, blockhash []byte) ([]*database.Output, error) {
	txids, err := s.BlockTxids(blockhash)
	if err != nil {
		return nil, err
	}
	out := make([]*database.Output, 0)
	for _, txid := range txids {
		outs, err := s.OutputsForTx(txid)
		if err != nil {
			return nil, err
		}
		out = append(out, outs...)
	}
	return out, nil
}

func TestTweaksForBlockAllMatchesReference(t *testing.T) {
	s, blockhash := benchStore(t, 4)

	want, err := refTweaksForBlockAll(s, blockhash)
	if err != nil {
		t.Fatalf("reference: %v", err)
	}
	got, err := s.TweaksForBlockAll(blockhash)
	if err != nil {
		t.Fatalf("TweaksForBlockAll: %v", err)
	}

	if len(want) == 0 {
		t.Fatal("fixture produced no tweaks; the test would prove nothing")
	}
	if len(got) != len(want) {
		t.Fatalf("got %d tweaks, reference has %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Txid != want[i].Txid || got[i].Tweak != want[i].Tweak {
			t.Fatalf("row %d differs:\n got txid=%x tweak=%x\nwant txid=%x tweak=%x",
				i, got[i].Txid, got[i].Tweak, want[i].Txid, want[i].Tweak)
		}
	}
}

func TestFetchOutputsAllMatchesReference(t *testing.T) {
	s, blockhash := benchStore(t, 4)

	want, err := refFetchOutputs(s, blockhash)
	if err != nil {
		t.Fatalf("reference: %v", err)
	}
	got, err := s.FetchOutputsAll(blockhash, 4)
	if err != nil {
		t.Fatalf("FetchOutputsAll: %v", err)
	}

	if len(want) == 0 {
		t.Fatal("fixture produced no outputs; the test would prove nothing")
	}
	if len(got) != len(want) {
		t.Fatalf("got %d outputs, reference has %d", len(got), len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if !bytes.Equal(g.Txid, w.Txid) || g.Vout != w.Vout ||
			g.Amount != w.Amount || !bytes.Equal(g.Pubkey, w.Pubkey) {
			t.Fatalf("output %d differs:\n got %x:%d amount=%d pubkey=%x\nwant %x:%d amount=%d pubkey=%x",
				i, g.Txid, g.Vout, g.Amount, g.Pubkey,
				w.Txid, w.Vout, w.Amount, w.Pubkey)
		}
	}
}

// An unknown block must come back empty rather than erroring or, worse,
// returning another block's rows — the seek-based loops share one iterator over
// the whole index, so a bad bound would show up here.
func TestUnknownBlockReturnsNothing(t *testing.T) {
	s, _ := benchStore(t, 4)
	absent := synthHash(0xbb, 9999)

	tweaks, err := s.TweaksForBlockAll(absent)
	if err != nil {
		t.Fatalf("TweaksForBlockAll: %v", err)
	}
	if len(tweaks) != 0 {
		t.Fatalf("unknown block returned %d tweaks", len(tweaks))
	}

	outs, err := s.FetchOutputsAll(absent, 4)
	if err != nil {
		t.Fatalf("FetchOutputsAll: %v", err)
	}
	if len(outs) != 0 {
		t.Fatalf("unknown block returned %d outputs", len(outs))
	}
}

// Every block in the fixture, not just the one the benchmarks sample: a seek
// that ran past the end of a transaction's outputs into the next transaction's
// would still match on a single block but not across all of them.
func TestAllBlocksMatchReference(t *testing.T) {
	const blocks = 4
	s, _ := benchStore(t, blocks)

	for h := 1; h <= blocks; h++ {
		blockhash := synthHash(0xbb, uint32(h))

		wantT, err := refTweaksForBlockAll(s, blockhash)
		if err != nil {
			t.Fatalf("block %d reference tweaks: %v", h, err)
		}
		gotT, err := s.TweaksForBlockAll(blockhash)
		if err != nil {
			t.Fatalf("block %d tweaks: %v", h, err)
		}
		if len(gotT) != len(wantT) {
			t.Fatalf("block %d: got %d tweaks, want %d", h, len(gotT), len(wantT))
		}
		for i := range wantT {
			if gotT[i].Txid != wantT[i].Txid || gotT[i].Tweak != wantT[i].Tweak {
				t.Fatalf("block %d tweak %d differs", h, i)
			}
		}

		wantO, err := refFetchOutputs(s, blockhash)
		if err != nil {
			t.Fatalf("block %d reference outputs: %v", h, err)
		}
		gotO, err := s.FetchOutputsAll(blockhash, uint32(blocks))
		if err != nil {
			t.Fatalf("block %d outputs: %v", h, err)
		}
		if len(gotO) != len(wantO) {
			t.Fatalf("block %d: got %d outputs, want %d", h, len(gotO), len(wantO))
		}
		for i := range wantO {
			if !bytes.Equal(gotO[i].Txid, wantO[i].Txid) ||
				gotO[i].Vout != wantO[i].Vout ||
				gotO[i].Amount != wantO[i].Amount ||
				!bytes.Equal(gotO[i].Pubkey, wantO[i].Pubkey) {
				t.Fatalf("block %d output %d differs", h, i)
			}
		}
	}
}
