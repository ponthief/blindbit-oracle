package dbpebble

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/setavenger/blindbit-oracle/internal/config"
)

// The endpoints a scanner joins have to agree about what a txid looks like.
//
// A reverse-matching scanner takes (txid, tweak) from /compute-index and the
// full output pubkeys from /utxos, and joins them ON THE TXID. If the two
// endpoints disagree about byte order, the join silently finds nothing: no
// error, no warning, just a wallet that reports less money than it has.
//
// Nothing tested this. The scanner's own tests build both shapes from one
// synthetic truth, so they agree by construction and could never catch a
// mismatch here.

func decodeRange(t *testing.T, r *gin.Engine, url string) []struct {
	Height uint32
	Raw    json.RawMessage
} {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d for %s", w.Code, url)
	}
	var body struct {
		Blocks []struct {
			BlockIdentifier struct {
				BlockHeight uint32 `json:"block_height"`
			} `json:"block_identifier"`
			Index json.RawMessage `json:"index"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON from %s: %v", url, err)
	}
	out := make([]struct {
		Height uint32
		Raw    json.RawMessage
	}, 0, len(body.Blocks))
	for _, b := range body.Blocks {
		out = append(out, struct {
			Height uint32
			Raw    json.RawMessage
		}{b.BlockIdentifier.BlockHeight, b.Index})
	}
	return out
}

func TestComputeIndexAndUtxosAgreeOnTxids(t *testing.T) {
	const blocks = 16
	config.MaxRangeBlocks = 100

	s, _ := benchStore(t, blocks)
	r := e2eRouter(s)

	ciBlocks := decodeRange(t, r, "/range/compute-index?start=1&end=16")
	utxoBlocks := decodeRange(t, r, "/range/utxos?start=1&end=16")

	if len(ciBlocks) == 0 || len(utxoBlocks) == 0 {
		t.Fatal("one of the endpoints returned nothing; the test would prove nothing")
	}

	utxoTxidsByHeight := map[uint32]map[string]bool{}
	for _, b := range utxoBlocks {
		var items []struct {
			Txid string `json:"txid"`
		}
		if err := json.Unmarshal(b.Raw, &items); err != nil {
			t.Fatalf("bad utxo index at height %d: %v", b.Height, err)
		}
		set := map[string]bool{}
		for _, it := range items {
			set[it.Txid] = true
		}
		utxoTxidsByHeight[b.Height] = set
	}

	totalEntries, matched := 0, 0
	for _, b := range ciBlocks {
		var items []struct {
			Txid  string `json:"txid"`
			Tweak string `json:"tweak"`
		}
		if err := json.Unmarshal(b.Raw, &items); err != nil {
			t.Fatalf("bad compute-index at height %d: %v", b.Height, err)
		}
		utxoSet := utxoTxidsByHeight[b.Height]
		for _, it := range items {
			totalEntries++
			if len(it.Txid) != 64 {
				t.Fatalf("height %d: compute-index txid is not 32-byte hex: %q",
					b.Height, it.Txid)
			}
			if utxoSet[it.Txid] {
				matched++
				continue
			}
			// Diagnose the likely cause rather than just failing.
			raw, err := hex.DecodeString(it.Txid)
			if err != nil {
				t.Fatalf("height %d: txid not hex: %v", b.Height, err)
			}
			rev := make([]byte, len(raw))
			for i := range raw {
				rev[i] = raw[len(raw)-1-i]
			}
			if utxoSet[hex.EncodeToString(rev)] {
				t.Fatalf("height %d: compute-index and /utxos disagree on txid BYTE "+
					"ORDER.\n  compute-index: %s\n  /utxos:        %s\n"+
					"A scanner joining these on txid finds nothing and reports a "+
					"balance that is too low.", b.Height, it.Txid,
					hex.EncodeToString(rev))
			}
			t.Fatalf("height %d: compute-index txid %s has no matching /utxos entry "+
				"in either byte order", b.Height, it.Txid)
		}
	}

	if totalEntries == 0 {
		t.Fatal("no compute-index entries; the test would prove nothing")
	}
	if matched != totalEntries {
		t.Fatalf("only %d of %d compute-index entries joined to a UTXO",
			matched, totalEntries)
	}
	t.Logf("%d compute-index entries across %d blocks, all joined to /utxos by txid",
		totalEntries, len(ciBlocks))
}

// Every transaction that /tweaks reports must also appear in /compute-index,
// or a scanner that switched to the compute index is looking at fewer
// transactions than the one that did not.
func TestComputeIndexCoversEveryTweak(t *testing.T) {
	const blocks = 16
	config.MaxRangeBlocks = 100

	s, _ := benchStore(t, blocks)
	_ = e2eRouter(s)

	for h := uint32(1); h <= blocks; h++ {
		blockhash := synthHash(0xbb, h)

		tweakRows, err := s.TweaksForBlockAll(blockhash)
		if err != nil {
			t.Fatalf("tweaks at %d: %v", h, err)
		}
		ciRows, err := s.FetchComputeIndex(h)
		if err != nil {
			t.Fatalf("compute index at %d: %v", h, err)
		}

		if len(ciRows) != len(tweakRows) {
			t.Fatalf("height %d: /tweaks has %d entries, compute-index has %d",
				h, len(tweakRows), len(ciRows))
		}

		tweaks := map[string]bool{}
		for _, row := range tweakRows {
			tweaks[hex.EncodeToString(row.Tweak[:])] = true
		}
		for _, row := range ciRows {
			if !tweaks[hex.EncodeToString(row.Tweak)] {
				t.Fatalf("height %d: compute-index carries a tweak /tweaks does not",
					h)
			}
		}
	}
}
