package dbpebble

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ginzip "github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"github.com/setavenger/blindbit-oracle/internal/config"
	"github.com/setavenger/blindbit-oracle/internal/server"
)

// What a scanner actually pays per block over loopback.
//
// With the oracle and the wallet on the same machine there is no round-trip
// latency to save, so a scan's "request time" is server work plus payload plus
// the client parsing it. This measures the first two against the real handler
// stack — real routes, real gzip middleware, real store — so the numbers can be
// compared against a reported scan instead of guessed at.

func e2eRouter(s *Store) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := server.NewHandler(s)
	r := gin.New()
	r.Use(ginzip.Gzip(ginzip.DefaultCompression))
	r.GET("/tweaks/:blockheight", h.GetTweaks)
	r.GET("/utxos/:blockheight", h.GetUtxos)
	r.GET("/compute-index/:blockheight", h.GetComputeIndex)
	r.GET("/range/tweaks", h.GetTweaksRange)
	r.GET("/range/utxos", h.GetUtxosRange)
	r.GET("/range/spent-outputs", h.GetSpentOutputsRange)
	r.GET("/range/compute-index", h.GetComputeIndexRange)
	return r
}

func timeRequest(tb testing.TB, r *gin.Engine, url string, gzipped bool) (dur time.Duration, size int) {
	tb.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	if gzipped {
		req.Header.Set("Accept-Encoding", "gzip")
	}
	w := httptest.NewRecorder()

	started := time.Now()
	r.ServeHTTP(w, req)
	body, err := io.ReadAll(w.Body)
	dur = time.Since(started)

	if err != nil {
		tb.Fatalf("read body: %v", err)
	}
	if w.Code != http.StatusOK {
		tb.Fatalf("status %d for %s: %s", w.Code, url, string(body[:min(200, len(body))]))
	}
	return dur, len(body)
}

func TestLoopbackCostPerBlock(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a store")
	}
	const blocks = 128
	config.MaxRangeBlocks = 100

	s, _ := benchStore(t, blocks)
	r := e2eRouter(s)

	const span = 100

	type row struct {
		name     string
		url      string
		perBlock bool
	}
	rows := []row{
		{"/range/tweaks (100 blocks)", "/range/tweaks?start=1&end=100", false},
		{"/range/utxos  (100 blocks)", "/range/utxos?start=1&end=100", false},
		{"/range/spent-outputs", "/range/spent-outputs?start=1&end=100", false},
		{"/compute-index (1 block)", "/compute-index/50", true},
		{"/tweaks (1 block)", "/tweaks/50", true},
		{"/utxos (1 block)", "/utxos/50", true},
	}

	t.Logf("block shape: %d txs, %d with a tweak, %d outputs each",
		benchTxsPerBlock, benchTxsPerBlock/benchTweakedPerRate, benchOutsPerTweaked)
	t.Logf("%-30s %10s %14s %14s", "endpoint", "time", "raw bytes", "gzipped")

	var rangeTotal time.Duration
	var rangeBytes int
	for _, rw := range rows {
		// Warm, then measure.
		timeRequest(t, r, rw.url, false)
		dur, raw := timeRequest(t, r, rw.url, false)
		_, gz := timeRequest(t, r, rw.url, true)

		label := rw.name
		if rw.perBlock {
			t.Logf("%-30s %10s %14d %14d   (x100 = %s, %d bytes raw)",
				label, dur.Round(time.Microsecond), raw, gz,
				(dur * 100).Round(time.Millisecond), raw*100)
		} else {
			t.Logf("%-30s %10s %14d %14d", label,
				dur.Round(time.Microsecond), raw, gz)
			rangeTotal += dur
			rangeBytes += raw
		}
	}

	t.Logf("")
	t.Logf("One 100-block batch over the range endpoints: %s of server time, %.1f MB raw JSON",
		rangeTotal.Round(time.Millisecond), float64(rangeBytes)/(1<<20))
	t.Logf("Per block that is %s and %.0f KB",
		(rangeTotal / span).Round(time.Microsecond),
		float64(rangeBytes)/span/1024)

}
