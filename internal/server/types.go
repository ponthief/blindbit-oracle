package server

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
)

// These responses are almost entirely hex strings, and a block's worth of them
// runs to thousands of entries. Building each one as a Go string, collecting
// them into a []string and handing that to encoding/json means an allocation
// per entry and a second pass over every byte. Writing the hex straight into
// the output buffer removes both.

// appendHexString appends src as a quoted, lower-case hex JSON string.
func appendHexString(dst, src []byte) []byte {
	dst = append(dst, '"')
	dst = hex.AppendEncode(dst, src)
	return append(dst, '"')
}

// appendHexArray appends a JSON array of quoted hex strings. n is the number of
// elements and at returns element i, so it serves the fixed-size-array slices
// below without a copy.
func appendHexArray(dst []byte, n int, at func(i int) []byte) []byte {
	dst = append(dst, '[')
	for i := 0; i < n; i++ {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = appendHexString(dst, at(i))
	}
	return append(dst, ']')
}

type BlockIdentifier struct {
	BlockHash   []byte `json:"block_hash"`
	BlockHeight uint32 `json:"block_height"`
}

func (b BlockIdentifier) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		BlockHash   string `json:"block_hash"`
		BlockHeight uint32 `json:"block_height"`
	}{
		BlockHash:   hex.EncodeToString(b.BlockHash),
		BlockHeight: b.BlockHeight,
	})
}

type FullBlockResponse struct {
	BlockIdentifier BlockIdentifier `json:"block_identifier"`
	Index           []FullTxItem    `json:"index"`
}

// TweakIndexResponse is tweak index response
// It does not contain the txid, for txids use ComputeIndex
type TweakIndexResponse struct {
	BlockIdentifier BlockIdentifier `json:"block_identifier"`
	Index           TweakSlice      `json:"index"`
}

// ComputeIndexResponse ComputeIndexItem array for block
type ComputeIndexResponse struct {
	BlockIdentifier BlockIdentifier    `json:"block_identifier"`
	Index           []ComputeIndexItem `json:"index"`
}

type TweakSlice [][33]byte

func (t TweakSlice) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 0, 2+len(t)*(2*33+3))
	return appendHexArray(buf, len(t), func(i int) []byte { return t[i][:] }), nil
}

// OutputsShort is a slice of 8 bytes each
// Will be marshalled to one contiguous hex string
type OutputsShort [][8]byte // 8 bytes each

func (o OutputsShort) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 0, 2+len(o)*(2*8+3))
	return appendHexArray(buf, len(o), func(i int) []byte { return o[i][:] }), nil
}

// ComputeIndexItem contains compact information
// to probe if a txid could have an interesting new output
type ComputeIndexItem struct {
	TxId         [32]byte     `json:"txid"`
	Tweak        [33]byte     `json:"tweak"`
	OutputsShort OutputsShort `json:"outputs"`
}

// MarshalJSON answers the "todo: can this be done without unmarshalling?" that
// stood here: it can. The previous version marshalled the outputs to JSON,
// unmarshalled that back into a []string purely to fill a struct field, and
// then marshalled the whole thing again — three passes over every output to
// produce the same bytes one pass produces.
func (c ComputeIndexItem) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 0, 128+len(c.OutputsShort)*(2*8+3))
	buf = append(buf, `{"txid":`...)
	buf = appendHexString(buf, c.TxId[:])
	buf = append(buf, `,"tweak":`...)
	buf = appendHexString(buf, c.Tweak[:])
	buf = append(buf, `,"outputs":`...)
	buf = appendHexArray(buf, len(c.OutputsShort), func(i int) []byte {
		return c.OutputsShort[i][:]
	})
	return append(buf, '}'), nil
}

type SpentIndexResponse struct {
	BlockIdentifier BlockIdentifier `json:"block_identifier"`
	Index           SpentIndex      `json:"index"`
}

// SpentIndex is a slice of 8 bytes each
type SpentIndex [][8]byte // 8 bytes each

func (s SpentIndex) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 0, 2+len(s)*(2*8+3))
	return appendHexArray(buf, len(s), func(i int) []byte { return s[i][:] }), nil
}

// SpentOutpointsIndex is a slice of 36 bytes each (32-byte txid + 4-byte vout)
type SpentOutpoints [][36]byte // 36 bytes each

func (s SpentOutpoints) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 0, 2+len(s)*(2*36+3))
	return appendHexArray(buf, len(s), func(i int) []byte { return s[i][:] }), nil
}

// FullTxItem is a struct that contains the information for a full
// Will be sent for Full Block Batch lots of data,
// should be avoided if possible
type FullTxItem struct {
	TxId   [32]byte        `json:"txid"`
	Tweak  [33]byte        `json:"tweak"`
	Inputs SpentOutpoints  `json:"inputs"`
	UTXOs  []UTXOItemLight `json:"utxos"` // should probably be optional
}

func (f FullTxItem) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 0, 256+len(f.Inputs)*(2*36+3)+len(f.UTXOs)*128)
	buf = append(buf, `{"txid":`...)
	buf = appendHexString(buf, f.TxId[:])
	buf = append(buf, `,"tweak":`...)
	buf = appendHexString(buf, f.Tweak[:])
	buf = append(buf, `,"inputs":`...)
	buf = appendHexArray(buf, len(f.Inputs), func(i int) []byte { return f.Inputs[i][:] })
	buf = append(buf, `,"utxos":`...)
	if f.UTXOs == nil {
		// A nil slice marshals as null, not [], and clients parse this field.
		buf = append(buf, "null"...)
	} else {
		buf = append(buf, '[')
		for i := range f.UTXOs {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = f.UTXOs[i].append(buf)
		}
		buf = append(buf, ']')
	}
	return append(buf, '}'), nil
}

type UTXOItemLight struct {
	Vout   uint32   `json:"vout"`
	Amount uint64   `json:"amount"`
	Pubkey [32]byte `json:"pubkey"`
}

func (u UTXOItemLight) MarshalJSON() ([]byte, error) {
	return u.append(make([]byte, 0, 128)), nil
}

func (u UTXOItemLight) append(buf []byte) []byte {
	buf = append(buf, `{"vout":`...)
	buf = strconv.AppendUint(buf, uint64(u.Vout), 10)
	buf = append(buf, `,"amount":`...)
	buf = strconv.AppendUint(buf, u.Amount, 10)
	buf = append(buf, `,"pubkey":`...)
	buf = appendHexString(buf, u.Pubkey[:])
	return append(buf, '}')
}

type UTXOItem struct {
	TxId   [32]byte `json:"txid,omitempty"`
	Vout   uint32   `json:"vout"`
	Amount uint64   `json:"amount"`
	Pubkey [32]byte `json:"pubkey"`
}

// MarshalJSON is the hottest of these by volume — one call per UTXO, and a
// block carries thousands.
//
// The txid is always emitted. The struct tag said "omitempty", but the value it
// applied to was the hex encoding of a fixed 32-byte array, which is 64
// characters even when the txid is all zeroes and so never empty.
func (u UTXOItem) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 0, 192)
	buf = append(buf, `{"txid":`...)
	buf = appendHexString(buf, u.TxId[:])
	buf = append(buf, `,"vout":`...)
	buf = strconv.AppendUint(buf, uint64(u.Vout), 10)
	buf = append(buf, `,"amount":`...)
	buf = strconv.AppendUint(buf, u.Amount, 10)
	buf = append(buf, `,"pubkey":`...)
	buf = appendHexString(buf, u.Pubkey[:])
	return append(buf, '}'), nil
}
