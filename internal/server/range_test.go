package server

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/setavenger/blindbit-lib/proto/pb"
	"github.com/setavenger/blindbit-oracle/internal/config"
	"github.com/setavenger/blindbit-oracle/internal/database"
)

// A stand-in store with just enough behaviour to drive the handlers: heights
// 100..109 exist, everything else does not.
type fakeDB struct {
	firstHeight uint32
	lastHeight  uint32
	failAt      uint32 // height at which reads start failing; 0 disables
}

func (f *fakeDB) has(height uint32) bool {
	return height >= f.firstHeight && height <= f.lastHeight
}

func (f *fakeDB) hashFor(height uint32) []byte {
	h := make([]byte, 32)
	h[0] = byte(height)
	h[1] = byte(height >> 8)
	return h
}

func (f *fakeDB) GetChainTip() ([]byte, uint32, error) {
	return f.hashFor(f.lastHeight), f.lastHeight, nil
}

func (f *fakeDB) GetBlockHashByHeight(height uint32) ([]byte, error) {
	if !f.has(height) {
		return nil, nil
	}
	return f.hashFor(height), nil
}

// heightOf recovers the height a blockhash was minted for, so the per-block
// fetchers can return height-dependent data.
func (f *fakeDB) heightOf(blockhash []byte) uint32 {
	return uint32(blockhash[0]) | uint32(blockhash[1])<<8
}

func (f *fakeDB) TweaksForBlockAll(blockhash []byte) ([]*database.TweakRow, error) {
	height := f.heightOf(blockhash)
	if f.failAt != 0 && height >= f.failAt {
		return nil, fmt.Errorf("injected failure at height %d", height)
	}
	// One tweak per block, its first byte carrying the height so the test can
	// tell the blocks apart.
	row := new(database.TweakRow)
	row.Tweak[0] = byte(height)
	return []*database.TweakRow{row}, nil
}

func (f *fakeDB) FetchOutputsAll(blockhash []byte, tipheight uint32) ([]*database.Output, error) {
	height := f.heightOf(blockhash)
	if f.failAt != 0 && height >= f.failAt {
		return nil, fmt.Errorf("injected failure at height %d", height)
	}
	return []*database.Output{{
		Txid:   make([]byte, 32),
		Vout:   height,
		Amount: uint64(height) * 1000,
		Pubkey: make([]byte, 32),
	}}, nil
}

func (f *fakeDB) FetchSpentOutputsShort(blockhash []byte) ([]byte, error) {
	height := f.heightOf(blockhash)
	if f.failAt != 0 && height >= f.failAt {
		return nil, fmt.Errorf("injected failure at height %d", height)
	}
	out := make([]byte, 8)
	out[0] = byte(height)
	return out, nil
}

// Unused by the range handlers.
func (f *fakeDB) ApplyBlock(*database.DBBlock) error { return nil }
func (f *fakeDB) FlushBatch(bool) error              { return nil }
func (f *fakeDB) TweaksForBlockCutThrough([]byte, uint32) ([]database.TweakRow, error) {
	return nil, nil
}
func (f *fakeDB) ChainIterator(bool) (<-chan []byte, error) { return nil, nil }
func (f *fakeDB) FetchComputeIndex(uint32) ([]*pb.ComputeIndexTxItem, error) {
	return nil, nil
}
func (f *fakeDB) BlockhashInDB([]byte) (bool, error)         { return true, nil }
func (f *fakeDB) BatchSize() int                             { return 0 }
func (f *fakeDB) KeyExistsComputeIndex([]byte) (bool, error) { return true, nil }
func (f *fakeDB) FetchTxidOutpoints(_, _ []byte) ([][36]byte, error) {
	return nil, nil
}
func (f *fakeDB) FetchAllTxidOutpointsForBlock([]byte) (map[[32]byte][][36]byte, error) {
	return nil, nil
}

var _ database.DB = (*fakeDB)(nil)

func newTestRouter(db database.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewHandler(db)
	r := gin.New()
	r.GET("/info", h.GetInfo)
	r.GET("/range/tweaks", h.GetTweaksRange)
	r.GET("/range/utxos", h.GetUtxosRange)
	r.GET("/range/spent-outputs", h.GetSpentOutputsRange)
	return r
}

func do(t *testing.T, r *gin.Engine, url string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

type tweakRangeBody struct {
	Blocks []struct {
		BlockIdentifier struct {
			BlockHash   string `json:"block_hash"`
			BlockHeight uint32 `json:"block_height"`
		} `json:"block_identifier"`
		Index []string `json:"index"`
	} `json:"blocks"`
}

func TestTweaksRangeReturnsEveryBlockInOrder(t *testing.T) {
	config.MaxRangeBlocks = 100
	r := newTestRouter(&fakeDB{firstHeight: 100, lastHeight: 109})

	w := do(t, r, "/range/tweaks?start=100&end=109")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var body tweakRangeBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v\n%s", err, w.Body.String())
	}
	if len(body.Blocks) != 10 {
		t.Fatalf("got %d blocks, want 10", len(body.Blocks))
	}
	for i, blk := range body.Blocks {
		wantHeight := uint32(100 + i)
		if blk.BlockIdentifier.BlockHeight != wantHeight {
			t.Errorf("block %d: height = %d, want %d", i, blk.BlockIdentifier.BlockHeight, wantHeight)
		}
		if len(blk.Index) != 1 {
			t.Fatalf("block %d: %d tweaks, want 1", i, len(blk.Index))
		}
		// The fake puts the height in the tweak's first byte; this proves each
		// block's own data came back, not the same block ten times.
		raw, err := hex.DecodeString(blk.Index[0])
		if err != nil {
			t.Fatalf("block %d: tweak is not hex: %v", i, err)
		}
		if raw[0] != byte(wantHeight) {
			t.Errorf("block %d: tweak carries height %d, want %d", i, raw[0], wantHeight)
		}
	}
}

// A range that runs off the end of the index returns the blocks that exist and
// omits the rest, rather than inventing empty ones.
func TestRangeSkipsUnindexedHeights(t *testing.T) {
	config.MaxRangeBlocks = 100
	r := newTestRouter(&fakeDB{firstHeight: 100, lastHeight: 104})

	w := do(t, r, "/range/tweaks?start=98&end=110")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var body tweakRangeBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, w.Body.String())
	}
	if len(body.Blocks) != 5 {
		t.Fatalf("got %d blocks, want 5 (heights 100-104)", len(body.Blocks))
	}
	if body.Blocks[0].BlockIdentifier.BlockHeight != 100 {
		t.Errorf("first block height = %d, want 100", body.Blocks[0].BlockIdentifier.BlockHeight)
	}
	if body.Blocks[4].BlockIdentifier.BlockHeight != 104 {
		t.Errorf("last block height = %d, want 104", body.Blocks[4].BlockIdentifier.BlockHeight)
	}
}

func TestRangeRejectsBadRequests(t *testing.T) {
	config.MaxRangeBlocks = 10
	r := newTestRouter(&fakeDB{firstHeight: 100, lastHeight: 109})

	cases := []struct {
		name string
		url  string
	}{
		{"missing both", "/range/tweaks"},
		{"missing end", "/range/tweaks?start=100"},
		{"missing start", "/range/tweaks?end=100"},
		{"unparseable start", "/range/tweaks?start=abc&end=100"},
		{"unparseable end", "/range/tweaks?start=100&end=xyz"},
		{"end before start", "/range/tweaks?start=105&end=100"},
		{"span over the cap", "/range/tweaks?start=100&end=200"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, r, tc.url)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
			}
		})
	}

	// Exactly at the cap is allowed — an off-by-one here would silently halve
	// a client's batch size.
	w := do(t, r, "/range/tweaks?start=100&end=109")
	if w.Code != http.StatusOK {
		t.Errorf("a span of exactly max_range_blocks was rejected: %d %s", w.Code, w.Body.String())
	}
}

// A single-block range is the degenerate case a client hits at the chain tip.
func TestRangeOfOneBlock(t *testing.T) {
	config.MaxRangeBlocks = 100
	r := newTestRouter(&fakeDB{firstHeight: 100, lastHeight: 109})

	w := do(t, r, "/range/tweaks?start=103&end=103")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var body tweakRangeBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, w.Body.String())
	}
	if len(body.Blocks) != 1 || body.Blocks[0].BlockIdentifier.BlockHeight != 103 {
		t.Fatalf("got %+v, want exactly block 103", body.Blocks)
	}
}

// A range entirely outside the index is an empty list, not an error and not
// malformed JSON.
func TestRangeWithNoIndexedBlocks(t *testing.T) {
	config.MaxRangeBlocks = 100
	r := newTestRouter(&fakeDB{firstHeight: 100, lastHeight: 109})

	w := do(t, r, "/range/tweaks?start=500&end=520")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var body tweakRangeBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, w.Body.String())
	}
	if len(body.Blocks) != 0 {
		t.Fatalf("got %d blocks, want 0", len(body.Blocks))
	}
}

// The failure that matters most: a database error partway through a stream.
// The status line is already sent, so the response must end up UNPARSEABLE
// rather than looking like a short but complete block list — a scanner that
// accepted the latter would skip blocks and miss payments in them.
func TestMidStreamFailureTruncatesRatherThanTrimming(t *testing.T) {
	config.MaxRangeBlocks = 100
	r := newTestRouter(&fakeDB{firstHeight: 100, lastHeight: 109, failAt: 105})

	w := do(t, r, "/range/tweaks?start=100&end=109")

	var body tweakRangeBody
	err := json.Unmarshal(w.Body.Bytes(), &body)
	if err == nil {
		t.Fatalf("a mid-stream failure parsed as valid JSON with %d blocks; "+
			"a scanner would treat the missing blocks as empty", len(body.Blocks))
	}
}

func TestUtxosAndSpentOutputsRange(t *testing.T) {
	config.MaxRangeBlocks = 100
	r := newTestRouter(&fakeDB{firstHeight: 100, lastHeight: 104})

	t.Run("utxos", func(t *testing.T) {
		w := do(t, r, "/range/utxos?start=100&end=104")
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		var body struct {
			Blocks []struct {
				BlockIdentifier struct {
					BlockHeight uint32 `json:"block_height"`
				} `json:"block_identifier"`
				Index []struct {
					Txid   string `json:"txid"`
					Vout   uint32 `json:"vout"`
					Amount uint64 `json:"amount"`
					Pubkey string `json:"pubkey"`
				} `json:"index"`
			} `json:"blocks"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON: %v\n%s", err, w.Body.String())
		}
		if len(body.Blocks) != 5 {
			t.Fatalf("got %d blocks, want 5", len(body.Blocks))
		}
		for i, blk := range body.Blocks {
			want := uint32(100 + i)
			if len(blk.Index) != 1 {
				t.Fatalf("block %d has %d utxos, want 1", want, len(blk.Index))
			}
			if blk.Index[0].Vout != want {
				t.Errorf("block %d: vout = %d, want %d", want, blk.Index[0].Vout, want)
			}
			if blk.Index[0].Amount != uint64(want)*1000 {
				t.Errorf("block %d: amount = %d, want %d", want, blk.Index[0].Amount, uint64(want)*1000)
			}
			if len(blk.Index[0].Txid) != 64 || len(blk.Index[0].Pubkey) != 64 {
				t.Errorf("block %d: txid/pubkey not 32-byte hex: %q %q",
					want, blk.Index[0].Txid, blk.Index[0].Pubkey)
			}
		}
	})

	t.Run("spent-outputs", func(t *testing.T) {
		w := do(t, r, "/range/spent-outputs?start=100&end=104")
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		var body struct {
			Blocks []struct {
				BlockIdentifier struct {
					BlockHeight uint32 `json:"block_height"`
				} `json:"block_identifier"`
				Index []string `json:"index"`
			} `json:"blocks"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON: %v\n%s", err, w.Body.String())
		}
		if len(body.Blocks) != 5 {
			t.Fatalf("got %d blocks, want 5", len(body.Blocks))
		}
		for i, blk := range body.Blocks {
			want := uint32(100 + i)
			if len(blk.Index) != 1 {
				t.Fatalf("block %d has %d entries, want 1", want, len(blk.Index))
			}
			raw, err := hex.DecodeString(blk.Index[0])
			if err != nil || len(raw) != 8 {
				t.Fatalf("block %d: entry is not 8-byte hex: %q", want, blk.Index[0])
			}
			if raw[0] != byte(want) {
				t.Errorf("block %d: got data for height %d", want, raw[0])
			}
		}
	})
}

// Clients feature-detect the range endpoints from /info, so the field has to be
// present and non-zero alongside every field that was there before.
func TestInfoAdvertisesRangeSupport(t *testing.T) {
	config.MaxRangeBlocks = 100
	config.Chain = config.Signet
	r := newTestRouter(&fakeDB{firstHeight: 100, lastHeight: 109})

	w := do(t, r, "/info")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if v, ok := body["max_range_blocks"]; !ok {
		t.Error("info is missing max_range_blocks")
	} else if v.(float64) != 100 {
		t.Errorf("max_range_blocks = %v, want 100", v)
	}
	// The pre-existing fields must still be top level, not nested under the
	// embedded struct's name.
	for _, key := range []string{"network", "height", "tweaks_only", "tweaks_full_basic"} {
		if _, ok := body[key]; !ok {
			t.Errorf("info is missing pre-existing field %q; embedding did not promote it", key)
		}
	}
}
