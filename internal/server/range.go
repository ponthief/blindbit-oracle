package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/setavenger/blindbit-lib/logging"
	"github.com/setavenger/blindbit-lib/utils"
	"github.com/setavenger/blindbit-oracle/internal/config"
)

// Range endpoints serve a span of blocks in one request.
//
// A scanner has to look at every block between its last scan and the tip, and
// the per-block endpoints make that three HTTP round trips per block. Against a
// remote oracle the round trips, not the work at either end, are what a scan
// costs: ten thousand blocks is thirty thousand requests, and at a 20 ms round
// trip that is ten minutes of pure waiting even with a warm connection pool and
// requests in flight concurrently.
//
// These endpoints are additive. The per-block routes keep working unchanged,
// and /info advertises max_range_blocks so a client can tell whether an oracle
// supports them before relying on them.

// rangeParams reads and validates ?start= and ?end= (both inclusive).
func rangeParams(c *gin.Context) (start, end uint32, ok bool) {
	startStr, endStr := c.Query("start"), c.Query("end")
	if startStr == "" || endStr == "" {
		c.JSON(http.StatusBadRequest, NewErrorResponse(
			errors.New("both start and end query parameters are required")))
		return 0, 0, false
	}

	s, err := strconv.ParseUint(startStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse(errors.New("could not parse start")))
		return 0, 0, false
	}
	e, err := strconv.ParseUint(endStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse(errors.New("could not parse end")))
		return 0, 0, false
	}

	if e < s {
		c.JSON(http.StatusBadRequest, NewErrorResponse(errors.New("end must be >= start")))
		return 0, 0, false
	}
	if span := e - s + 1; span > uint64(config.MaxRangeBlocks) {
		c.JSON(http.StatusBadRequest, NewErrorResponse(
			errors.New("requested range of "+strconv.FormatUint(span, 10)+
				" blocks exceeds max_range_blocks of "+
				strconv.FormatUint(uint64(config.MaxRangeBlocks), 10))))
		return 0, 0, false
	}

	return uint32(s), uint32(e), true
}

// streamBlocks writes {"blocks":[...]} , calling encode once per height in the
// range and skipping heights the node has not indexed.
//
// The response is streamed rather than assembled: a hundred blocks of UTXOs is
// a large object to hold in memory per in-flight request, and there may be many.
//
// A database error partway through cannot become a 500 — the status line and
// the start of the body are already on the wire. The stream is abandoned
// instead, without its closing bracket, so the client's JSON parser rejects a
// truncated response rather than accepting a short block list as complete. For
// a wallet scanner that distinction is the difference between an error and
// silently missing money.
func (h *Handler) streamBlocks(
	c *gin.Context, start, end uint32,
	encode func(height uint32, blockhash []byte) (any, error),
) {
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Status(http.StatusOK)

	w := c.Writer
	enc := json.NewEncoder(w)

	if _, err := w.WriteString(`{"blocks":[`); err != nil {
		return
	}

	written := 0
	for height := start; height <= end; height++ {
		blockhash, err := h.db.GetBlockHashByHeight(height)
		if err != nil {
			logging.L.Err(err).Uint32("height", height).
				Msg("could not fetch block hash mid-range; truncating response")
			return
		}
		if blockhash == nil {
			// Not indexed (above the sync tip, or below the start height).
			// Skipped rather than reported as an empty block, which a scanner
			// could not tell apart from "nothing for you here".
			continue
		}

		payload, err := encode(height, blockhash)
		if err != nil {
			logging.L.Err(err).Uint32("height", height).
				Msg("could not build range entry; truncating response")
			return
		}

		if written > 0 {
			if _, err := w.WriteString(","); err != nil {
				return
			}
		}
		// Encode writes a trailing newline, which is valid whitespace between
		// JSON array elements.
		if err := enc.Encode(payload); err != nil {
			logging.L.Err(err).Uint32("height", height).Msg("failed encoding range entry")
			return
		}
		written++

		if height == end { // avoid uint32 overflow when end is MaxUint32
			break
		}
	}

	w.WriteString(`]}`)
}

// GetTweaksRange serves /tweaks for a span of blocks in one request.
func (h *Handler) GetTweaksRange(c *gin.Context) {
	start, end, ok := rangeParams(c)
	if !ok {
		return
	}

	h.streamBlocks(c, start, end, func(height uint32, blockhash []byte) (any, error) {
		tweakRows, err := h.db.TweaksForBlockAll(blockhash)
		if err != nil {
			return nil, err
		}
		tweaksOut := make(TweakSlice, 0, len(tweakRows))
		for _, tweakRow := range tweakRows {
			if tweakRow != nil {
				tweaksOut = append(tweaksOut, tweakRow.Tweak)
			}
		}
		return TweakIndexResponse{
			BlockIdentifier: BlockIdentifier{
				BlockHash:   utils.ReverseBytesCopy(blockhash),
				BlockHeight: height,
			},
			Index: tweaksOut,
		}, nil
	})
}

// GetUtxosRange serves /utxos for a span of blocks in one request.
func (h *Handler) GetUtxosRange(c *gin.Context) {
	start, end, ok := rangeParams(c)
	if !ok {
		return
	}

	// Fetched once for the whole range rather than per block, as the per-block
	// handler does.
	_, syncTip, err := h.db.GetChainTip()
	if err != nil {
		logging.L.Err(err).Msg("could not fetch chain tip")
		c.JSON(http.StatusInternalServerError, NewErrorResponse(errors.New("could not fetch chain tip")))
		return
	}

	h.streamBlocks(c, start, end, func(height uint32, blockhash []byte) (any, error) {
		outputs, err := h.db.FetchOutputsAll(blockhash, syncTip)
		if err != nil {
			return nil, err
		}
		utxoItems := make([]UTXOItem, 0, len(outputs))
		for _, output := range outputs {
			if output == nil {
				continue
			}
			var pubkey [32]byte
			copy(pubkey[:], output.Pubkey)
			utxoItems = append(utxoItems, UTXOItem{
				TxId:   [32]byte(utils.ReverseBytesCopy(output.Txid)),
				Vout:   output.Vout,
				Amount: output.Amount,
				Pubkey: pubkey,
			})
		}
		return struct {
			BlockIdentifier BlockIdentifier `json:"block_identifier"`
			Index           []UTXOItem      `json:"index"`
		}{
			BlockIdentifier: BlockIdentifier{
				BlockHash:   utils.ReverseBytesCopy(blockhash),
				BlockHeight: height,
			},
			Index: utxoItems,
		}, nil
	})
}

// GetSpentOutputsRange serves /spent-outputs for a span of blocks in one request.
func (h *Handler) GetSpentOutputsRange(c *gin.Context) {
	start, end, ok := rangeParams(c)
	if !ok {
		return
	}

	h.streamBlocks(c, start, end, func(height uint32, blockhash []byte) (any, error) {
		spentOutputsData, err := h.db.FetchSpentOutputsShort(blockhash)
		if err != nil {
			return nil, err
		}
		spentOutputsShort := make(SpentIndex, 0, len(spentOutputsData)/8)
		for i := 0; i+8 <= len(spentOutputsData); i += 8 {
			var outputBytes [8]byte
			copy(outputBytes[:], spentOutputsData[i:i+8])
			spentOutputsShort = append(spentOutputsShort, outputBytes)
		}
		return SpentIndexResponse{
			BlockIdentifier: BlockIdentifier{
				BlockHash:   utils.ReverseBytesCopy(blockhash),
				BlockHeight: height,
			},
			Index: spentOutputsShort,
		}, nil
	})
}
