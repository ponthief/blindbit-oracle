package dbpebble

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/setavenger/blindbit-lib/logging"
	"github.com/setavenger/blindbit-lib/utils"
	"github.com/setavenger/blindbit-oracle/internal/database"
)

// sortedTxidOrder returns the positions of txids in ascending txid order.
//
// The block-tx index is keyed by position in the block, so BlockTxids hands
// back txids in block order — which is random with respect to the key order of
// the tx and output indexes. Walking them in that order makes every lookup an
// independent descent of the LSM tree. Walking them in txid order instead lets
// a single iterator move forward only, so consecutive seeks land in the same
// sstable block and the block cache is hit rather than re-entered.
//
// The positions, not the txids, are returned so callers can put their results
// back into block order and keep the response identical to what per-txid
// lookups produced.
func sortedTxidOrder(txids [][]byte) []int {
	order := make([]int, len(txids))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		return bytes.Compare(txids[order[a]], txids[order[b]]) < 0
	})
	return order
}

// Best-chain map to test membership quickly.

type ActiveHeight func(blockHash []byte) (height uint32, ok bool)

// GetChainTip returns the Blockhash and height of the highest block
func (s *Store) GetChainTip() ([]byte, uint32, error) {
	lb, ub := BoundsCIHeight()
	it, err := s.DB.NewIter(&pebble.IterOptions{LowerBound: lb, UpperBound: ub})
	if err != nil {
		return nil, 0, err
	}
	defer it.Close()
	if !it.Last() {
		// edge case empty db we are at 0 height
		return nil, 0, nil
	}
	heightBytes := it.Key()
	blockhash := it.Value()

	if len(blockhash) != 32 {
		return nil, 0, fmt.Errorf("bad blockhash %x", blockhash)
	}

	height := binary.BigEndian.Uint32(heightBytes[1:])

	// Copy the blockhash to avoid returning invalid iterator memory
	result := make([]byte, len(blockhash))
	copy(result, blockhash)
	return result, height, nil
}

func (s *Store) FirstBlock() ([]byte, uint32, error) {
	lb, ub := BoundsCIHeight()
	it, err := s.DB.NewIter(&pebble.IterOptions{LowerBound: lb, UpperBound: ub})
	if err != nil {
		return nil, 0, err
	}
	defer it.Close()
	if !it.First() {
		// edge case empty db we are at 0 height
		return nil, 0, nil
	}
	heightBytes := it.Key()
	blockhash := it.Value()

	if len(blockhash) != 32 {
		return nil, 0, fmt.Errorf("bad blockhash %x", blockhash)
	}

	height := binary.BigEndian.Uint32(heightBytes[1:])

	// Copy the blockhash to avoid returning invalid iterator memory
	result := make([]byte, len(blockhash))
	copy(result, blockhash)
	return result, height, nil
}

func (s *Store) GetBlockHashByHeight(height uint32) ([]byte, error) {
	key := KeyCIHeight(height)
	blockhash, closer, err := s.DB.Get(key)
	if err != nil && !errors.Is(err, pebble.ErrNotFound) {
		return nil, err
	} else if errors.Is(err, pebble.ErrNotFound) {
		return nil, nil
	}
	defer closer.Close()

	// Copy the blockhash to avoid returning invalid memory after closer.Close()
	result := make([]byte, len(blockhash))
	copy(result, blockhash)
	return result, err
}

// ChainIterator returns a channel of block hashes in the chain
// if asc is true, the channel will be in ascending order
// if asc is false, the channel will be in descending order
func (s *Store) ChainIterator(asc bool) (<-chan []byte, error) {
	// todo: add context
	lb, ub := BoundsCIHeight()
	it, err := s.DB.NewIter(&pebble.IterOptions{LowerBound: lb, UpperBound: ub})
	if err != nil {
		return nil, err
	}

	blockhashChan := make(chan []byte)
	go func() {
		defer it.Close()
		defer close(blockhashChan)
		if asc {
			for ok := it.First(); ok; ok = it.Next() {
				// Copy the value before sending to channel to avoid iterator memory corruption
				blockhash := make([]byte, len(it.Value()))
				copy(blockhash, it.Value())
				blockhashChan <- blockhash
			}
		} else {
			for ok := it.Last(); ok; ok = it.Prev() {
				// Copy the value before sending to channel to avoid iterator memory corruption
				blockhash := make([]byte, len(it.Value()))
				copy(blockhash, it.Value())
				blockhashChan <- blockhash
			}
		}

		if err = it.Error(); err != nil {
			logging.L.Err(err).Msg("error iterating chain")
			return
		}
	}()

	return blockhashChan, nil
}

func (s *Store) BlockTxids(blockHash []byte) ([][]byte, error) {
	lb, ub := BoundsBlockTx(blockHash)
	it, err := s.DB.NewIter(&pebble.IterOptions{LowerBound: lb, UpperBound: ub})
	if err != nil {
		return nil, err
	}
	defer it.Close()

	// Txids are carved out of a shared backing array rather than allocated one
	// at a time. A busy block holds a few thousand transactions and this runs on
	// every /tweaks and /utxos request, so one allocation per transaction was
	// the single largest source of garbage in the read path.
	const txidsPerSlab = 512
	var slab []byte

	out := make([][]byte, 0, txidsPerSlab)
	for ok := it.First(); ok; ok = it.Next() {
		v := it.Value()
		if len(v) != SizeTxid {
			return nil, fmt.Errorf("bad txid length %d in block index", len(v))
		}
		if len(slab) < SizeTxid {
			slab = make([]byte, SizeTxid*txidsPerSlab)
		}
		copy(slab, v)
		// Capped so a caller appending to one txid cannot scribble on the next.
		out = append(out, slab[:SizeTxid:SizeTxid])
		slab = slab[SizeTxid:]
	}
	return out, it.Error()
}

func (s *Store) OutputsForTx(txid []byte) ([]*database.Output, error) {
	lb, ub := BoundsOut(txid)
	it, err := s.DB.NewIter(&pebble.IterOptions{LowerBound: lb, UpperBound: ub})
	if err != nil {
		return nil, err
	}
	defer it.Close()

	var outs []*database.Output
	for ok := it.First(); ok; ok = it.Next() {
		// parse vout from last 4 bytes of key
		k := it.Key()
		vout := binary.BigEndian.Uint32(k[len(k)-SizeVout:])

		amt, pk, err := ParseOutValue(it.Value())
		if err != nil {
			return nil, err
		}
		outs = append(outs, &database.Output{
			Txid:   txid,
			Vout:   vout,
			Amount: amt,
			Pubkey: pk,
		})
	}
	return outs, nil
}

func (s *Store) LoadTweak(txid []byte) ([]byte, bool, error) {
	val, closer, err := s.DB.Get(KeyTx(txid))
	if err != nil && !errors.Is(err, pebble.ErrNotFound) {
		return nil, false, err
	} else if errors.Is(err, pebble.ErrNotFound) {
		return nil, false, nil
	}
	defer closer.Close()
	out := make([]byte, len(val))
	copy(out, val)
	return out, true, nil
}

// IsSpentAt Is outpoint spent on best chain at height H?
func (s *Store) IsSpentAt(
	prevTxid []byte, prevVout, H uint32, active ActiveHeight,
) (bool, error) {
	lb, ub := BoundsSpend(prevTxid, prevVout)
	it, err := s.DB.NewIter(&pebble.IterOptions{LowerBound: lb, UpperBound: ub})
	if err != nil {
		return false, err
	}
	defer it.Close()

	for ok := it.First(); ok; ok = it.Next() {
		k := it.Key()
		// blockHash is the last 32 bytes of key
		blk := k[len(k)-SizeHash:]
		if h, ok := active(blk); ok && h <= H {
			return true, nil
		}
	}
	return false, nil
}

// Full query: tweaks for block with cut-through + dust >= X at height H

func (s *Store) TweaksForBlock(
	blockHash []byte,
	H uint32,
	dust uint64,
	active ActiveHeight,
) ([]database.TweakRow, error) {
	txids, err := s.BlockTxids(blockHash)
	if err != nil {
		return nil, err
	}

	var out []database.TweakRow
	for _, txid := range txids {
		tweak, ok, err := s.LoadTweak(txid)
		if err != nil || !ok {
			continue
		} // skip non-SP

		outs, err := s.OutputsForTx(txid)
		if err != nil {
			return nil, err
		}

		var hasUnspent bool
		var maxUnspent uint64
		for _, o := range outs {
			spent, err := s.IsSpentAt(txid, o.Vout, H, active)
			if err != nil {
				return nil, err
			}
			if !spent {
				hasUnspent = true
				if o.Amount > maxUnspent {
					maxUnspent = o.Amount
				}
			}
		}
		if hasUnspent && (dust == 0 || maxUnspent >= dust) {
			// row := &database.TweakRow{Txid: make([]byte, SizeTxid), Tweak: make([]byte, SizeTweak)}
			// copy(row.Txid, txid)
			// copy(row.Tweak, tweak)
			// out = append(out, row)

			var row database.TweakRow
			copy(row.Txid[:], txid)
			copy(row.Tweak[:], tweak)
			out = append(out, row)
		}
	}
	return out, nil

}

// BlockhashInDB maybe we can get the same result by simply checking for a height per blockhash reduces reduncancy of functions
func (s *Store) BlockhashInDB(blockhash []byte) (bool, error) {
	key := KeyCIBlock(blockhash)
	_, closer, err := s.DB.Get(key)
	if err != nil && !errors.Is(err, pebble.ErrNotFound) {
		return false, err
	} else if err != nil && errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	defer closer.Close()

	return true, nil
}

// --- new internal helpers ---------------------------------------------------

// heightIfOnBestChain returns (height,true) if blockHash is on best chain; otherwise (0,false).
func (s *Store) heightIfOnBestChain(blockHash []byte) (uint32, bool, error) {
	val, closer, err := s.DB.Get(KeyCIBlock(blockHash)) // ci:b:<blockHash> -> [4]heightBE
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return 0, false, nil
		} else {
			return 0, false, err
		}
	}

	defer closer.Close()
	if len(val) != SizeHeight {
		return 0, false, errors.New("bad ci:b value length")
	}
	h := binary.BigEndian.Uint32(val[:SizeHeight])
	return h, true, nil
}

// spentAtHeightTip: is (prevTxid,prevVout) spent on best chain at or before H?
func (s *Store) spentAtHeightTip(prevTxid []byte, prevVout, H uint32) (bool, error) {
	// todo: should we drop the H thing. Just check if in or not

	lb, ub := BoundsSpend(prevTxid, prevVout) // sp:<txid>:<vout>:<blockHash>
	it, err := s.DB.NewIter(&pebble.IterOptions{LowerBound: lb, UpperBound: ub})
	if err != nil {
		return false, err
	}
	defer it.Close()

	for ok := it.First(); ok; ok = it.Next() {
		k := it.Key()
		blk := k[len(k)-SizeHash:] // last 32 bytes
		if h, ok, err := s.heightIfOnBestChain(blk); err != nil {
			return false, err
		} else if ok && h <= H {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) FetchOutputsAll(
	blockhash []byte, tipHeight uint32,
) ([]*database.Output, error) {
	return s.fetchOutputs(blockhash)
}

func (s *Store) FetchOutputsCutThroughDustLimit(
	blockhash []byte, tipHeight uint32, dustLimit uint64,
) ([]*database.Output, error) {
	timeStart := time.Now()
	defer func() {
		logging.L.Trace().
			Dur("duration", time.Since(timeStart)).
			Uint64("dust_limit", dustLimit).
			Hex("blockhash", utils.ReverseBytesCopy(blockhash)).
			Msg("fetching_outputs_filtered_timing")
	}()

	outputs, err := s.fetchOutputs(blockhash)
	if err != nil {
		return nil, err
	}

	filteredOuts := make([]*database.Output, len(outputs))
	idxCounter := 0
	for i := range outputs {
		o := outputs[i]
		if o.Amount < dustLimit {
			continue
		}
		var spent bool
		spent, err = s.spentAtHeightTip(o.Txid, o.Vout, tipHeight)
		if err != nil {
			return nil, err
		}
		if spent {
			continue
		}

		// passed all filters
		filteredOuts[idxCounter] = o
		idxCounter++
	}

	return filteredOuts[:idxCounter], err
}

func (s *Store) fetchOutputs(
	blockhash []byte,
) ([]*database.Output, error) {
	// timing block on trace level
	timeStart := time.Now()
	defer func() {
		logging.L.Trace().
			Dur("duration", time.Since(timeStart)).
			Hex("blockhash", utils.ReverseBytesCopy(blockhash)).
			Msg("fetching_outputs_timing")
	}()

	txids, err := s.BlockTxids(blockhash)
	if err != nil {
		return nil, err
	}
	if len(txids) == 0 {
		return nil, nil
	}

	// One iterator over the whole output index, seeked once per txid, instead
	// of a fresh iterator per transaction. Constructing a Pebble iterator means
	// building a merged view over the memtables and every level of the LSM, so
	// at a few thousand transactions a block that construction cost — paid
	// mostly for transactions that have no outputs at all — dominated this
	// function.
	it, err := s.DB.NewIter(&pebble.IterOptions{
		LowerBound: []byte{KOut},
		UpperBound: []byte{KOut + 1},
	})
	if err != nil {
		return nil, err
	}
	defer it.Close()

	// Results are bucketed by the transaction's position in the block and
	// flattened afterwards, so the response stays in block order even though
	// the lookups run in txid order.
	perTx := make([][]*database.Output, len(txids))
	total := 0

	// Same slab treatment as the txids: the outputs and their pubkeys come out
	// of shared backing arrays rather than two allocations each.
	const outsPerSlab = 256
	var outSlab []database.Output
	var pkSlab []byte

	key := make([]byte, 1+SizeTxid+SizeVout)
	key[0] = KOut

	for _, pos := range sortedTxidOrder(txids) {
		txid := txids[pos]
		copy(key[1:1+SizeTxid], txid)
		// vout stays zero: seek to the first output of this transaction.
		for i := 1 + SizeTxid; i < len(key); i++ {
			key[i] = 0
		}

		ok := it.SeekGE(key)
		if !ok {
			// Nothing at or after this txid in the output index. Seeks run in
			// ascending order, so no later txid can find anything either.
			break
		}
		for ; ok; ok = it.Next() {
			k := it.Key()
			if !bytes.Equal(k[1:1+SizeTxid], txid) {
				break
			}
			v := it.Value()
			if len(v) != SizeAmt+SizePubKey {
				return nil, errors.New("bad out value length")
			}

			if len(outSlab) == 0 {
				outSlab = make([]database.Output, outsPerSlab)
			}
			if len(pkSlab) < SizePubKey {
				pkSlab = make([]byte, SizePubKey*outsPerSlab)
			}
			pk := pkSlab[:SizePubKey:SizePubKey]
			copy(pk, v[SizeAmt:])
			pkSlab = pkSlab[SizePubKey:]

			o := &outSlab[0]
			outSlab = outSlab[1:]
			o.Txid = txid
			o.Vout = binary.BigEndian.Uint32(k[1+SizeTxid:])
			o.Amount = binary.LittleEndian.Uint64(v[:SizeAmt])
			o.Pubkey = pk

			perTx[pos] = append(perTx[pos], o)
			total++
		}
	}
	if err := it.Error(); err != nil {
		return nil, err
	}

	out := make([]*database.Output, 0, total)
	for _, outs := range perTx {
		out = append(out, outs...)
	}
	return out, nil
}

func (s *Store) TweaksForBlockAll(blockhash []byte) ([]*database.TweakRow, error) {
	timeStart := time.Now()
	defer func() {
		logging.L.Trace().
			Dur("duration", time.Since(timeStart)).
			Hex("blockhash", utils.ReverseBytesCopy(blockhash)).
			Msg("fetching_tweaks_timing")
	}()
	txids, err := s.BlockTxids(blockhash)
	if err != nil {
		return nil, err
	}
	if len(txids) == 0 {
		return nil, nil
	}

	// As in fetchOutputs: one iterator seeked in txid order, rather than a
	// point lookup per transaction. Most transactions in a block carry no
	// tweak, so most of those lookups were misses that still cost a full
	// descent through the LSM's bloom filters and index blocks.
	it, err := s.DB.NewIter(&pebble.IterOptions{
		LowerBound: []byte{KTx},
		UpperBound: []byte{KTx + 1},
	})
	if err != nil {
		return nil, err
	}
	defer it.Close()

	// Bucketed by position so the result keeps block order.
	rows := make([]*database.TweakRow, len(txids))
	total := 0

	const rowsPerSlab = 256
	var rowSlab []database.TweakRow

	key := make([]byte, 1+SizeTxid)
	key[0] = KTx

	for _, pos := range sortedTxidOrder(txids) {
		txid := txids[pos]
		copy(key[1:], txid)

		if !it.SeekGE(key) {
			break // ascending seeks: nothing left to find
		}
		if !bytes.Equal(it.Key(), key) {
			continue // this transaction has no tweak
		}
		val := it.Value()
		if len(val) != SizeTweak {
			return nil, fmt.Errorf("bad tweak length %d for txid %x", len(val), txid)
		}
		if len(rowSlab) == 0 {
			rowSlab = make([]database.TweakRow, rowsPerSlab)
		}
		row := &rowSlab[0]
		rowSlab = rowSlab[1:]
		copy(row.Txid[:], txid)
		copy(row.Tweak[:], val)
		rows[pos] = row
		total++
	}
	if err := it.Error(); err != nil {
		return nil, err
	}

	out := make([]*database.TweakRow, 0, total)
	for _, row := range rows {
		if row != nil {
			out = append(out, row)
		}
	}
	return out, nil
}

// TweaksForBlockCutThrough Account for cut-through
//  2. Cut-through: exclude txs whose every tracked output is already spent
//     at or before tipHeight on the best chain.
func (s *Store) TweaksForBlockCutThrough(
	blockHash []byte, tipHeight uint32,
) ([]database.TweakRow, error) {
	txids, err := s.BlockTxids(blockHash)
	if err != nil {
		return nil, err
	}

	var out []database.TweakRow
	for _, txid := range txids {
		tweak, ok, err := s.LoadTweak(txid)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}

		outs, err := s.OutputsForTx(txid)
		if err != nil {
			return nil, err
		}

		// keep tweak if ANY tracked output is unspent at tip
		keep := false
		for _, o := range outs {
			spent, err := s.spentAtHeightTip(txid, o.Vout, tipHeight)
			if err != nil {
				return nil, err
			}
			if !spent {
				keep = true
				break
			}
		}
		if keep {
			// row := &database.TweakRow{
			// 	Txid:  make([]byte, SizeTxid),
			// 	Tweak: make([]byte, SizeTweak),
			// }
			// copy(row.Txid, txid)
			// copy(row.Tweak, tweak)
			// out = append(out, row)
			var row database.TweakRow
			copy(row.Txid[:], txid)
			copy(row.Tweak[:], tweak)
			out = append(out, row)
		}
	}
	return out, nil
}

func (s *Store) TweaksForBlockCutThroughDustLimit(
	blockHash []byte, tipHeight uint32, dustLimit uint64,
) ([]database.TweakRow, error) {
	// todo: spent at height
	txids, err := s.BlockTxids(blockHash)
	if err != nil {
		return nil, err
	}

	var out []database.TweakRow
	for _, txid := range txids {
		tweak, ok, err := s.LoadTweak(txid)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}

		outs, err := s.OutputsForTx(txid)
		if err != nil {
			return nil, err
		}

		// keep tweak if ANY tracked output is unspent at tip
		keep := false
		for _, o := range outs {
			spent, err := s.spentAtHeightTip(txid, o.Vout, tipHeight)
			if err != nil {
				return nil, err
			}
			if !spent {
				keep = true
				break
			}
		}
		if keep {
			// row := database.TweakRow{
			// 	Txid:  make([]byte, SizeTxid),
			// 	Tweak: make([]byte, SizeTweak),
			// }
			// copy(row.Txid, txid)
			// copy(row.Tweak, tweak)
			// out = append(out, row)

			var row database.TweakRow
			copy(row.Txid[:], txid)
			copy(row.Tweak[:], tweak)
			out = append(out, row)

		}
	}
	return out, nil
}

// -----Statics ------

func (s *Store) FetchSpentOutputsShort(blockhash []byte) ([]byte, error) {
	// Returns first 8 bytes of x-only pubkeys for spent outputs
	val, closer, err := s.DB.Get(KeySpentOutputsShort(blockhash))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return make([]byte, 0), nil
		}
		return nil, err
	}
	defer closer.Close()

	// Copy the data to avoid returning invalid memory after closer.Close()
	result := make([]byte, len(val))
	copy(result, val)
	return result, nil
}

// ---------------- Key exists checks ----------------

func (s *Store) KeyExistsComputeIndex(blockhash []byte) (bool, error) {
	// Check if any compute index entries exist for this block
	// We need to get the height first, then check if any compute index entries exist
	height, ok, err := s.heightIfOnBestChain(blockhash)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil // Block not on best chain
	}

	lb, ub := BoundsComputeIndexOneHeight(height)
	it, err := s.DB.NewIter(&pebble.IterOptions{LowerBound: lb, UpperBound: ub})
	if err != nil {
		return false, err
	}
	defer it.Close()

	// If we can find at least one entry, the compute index exists for this block
	return it.First(), nil
}

// FetchTxidOutpoints retrieves all outpoints for a given transaction ID in a specific block
func (s *Store) FetchTxidOutpoints(blockhash, txid []byte) ([][36]byte, error) {
	start := time.Now()
	defer func() {
		logging.L.Debug().
			Dur("duration", time.Since(start)).
			Hex("blockhash", blockhash).
			Hex("txid", txid).
			Msg("fetching_txid_outpoints_timing")
	}()

	val, closer, err := s.DB.Get(KeyTxidOutpoints(blockhash, txid))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return [][36]byte{}, nil // No outpoints found for this txid
		}
		return nil, err
	}
	defer closer.Close()

	return ParseTxidOutpointsValue(val)
}

// FetchAllTxidOutpointsForBlock retrieves all txid-outpoints mappings for a given block
// This uses contiguous iteration for efficient retrieval
func (s *Store) FetchAllTxidOutpointsForBlock(blockhash []byte) (map[[32]byte][][36]byte, error) {
	// I guess a map is fine as we have lost the "original" ordering of the block anyways
	// <blockhash><txid> is the key and sorts by txid on second hierarchy level
	start := time.Now()
	defer func() {
		logging.L.Debug().
			Dur("duration", time.Since(start)).
			Hex("blockhash", blockhash).
			Msg("fetching_all_txid_outpoints_for_block_timing")
	}()

	lb, ub := BoundsTxidOutpoints(blockhash)
	it, err := s.DB.NewIter(&pebble.IterOptions{LowerBound: lb, UpperBound: ub})
	if err != nil {
		return nil, err
	}
	defer it.Close()

	result := make(map[[32]byte][][36]byte)

	for it.First(); it.Valid(); it.Next() {
		key := it.Key()
		if len(key) != 1+SizeHash+SizeTxid {
			err := errors.New("malformed key")
			logging.L.Err(err).Hex("key", key).Msg("skipping malformed key")
			return nil, err
		}

		txid := key[1+SizeHash:]
		outpoints, err := ParseTxidOutpointsValue(it.Value())
		if err != nil {
			logging.L.Err(err).Hex("txid", txid).Msg("failed to parse txid outpoints")
			return nil, err
		}

		result[[32]byte(txid)] = outpoints
	}

	return result, nil
}
