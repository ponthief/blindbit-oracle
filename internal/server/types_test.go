package server

import (
	"encoding/hex"
	"encoding/json"
	"testing"
)

// The marshallers these replaced, kept verbatim. They define the wire format
// that deployed scanners already parse, so the test is byte-for-byte equality
// rather than "parses to something equivalent".

func refTweakSlice(t TweakSlice) ([]byte, error) {
	out := make([]string, len(t))
	for i := range t {
		out[i] = hex.EncodeToString(t[i][:])
	}
	return json.Marshal(out)
}

func refOutputsShort(o OutputsShort) ([]byte, error) {
	out := make([]string, len(o))
	for i := range len(o) {
		out[i] = hex.EncodeToString(o[i][:])
	}
	return json.Marshal(out)
}

func refComputeIndexItem(c ComputeIndexItem) ([]byte, error) {
	outputsShortBytes, err := refOutputsShort(c.OutputsShort)
	if err != nil {
		return nil, err
	}
	var outputsShortStr []string
	json.Unmarshal(outputsShortBytes, &outputsShortStr)
	return json.Marshal(struct {
		TxId         string   `json:"txid"`
		Tweak        string   `json:"tweak"`
		OutputsShort []string `json:"outputs"`
	}{
		TxId:         hex.EncodeToString(c.TxId[:]),
		Tweak:        hex.EncodeToString(c.Tweak[:]),
		OutputsShort: outputsShortStr,
	})
}

func refUTXOItem(u UTXOItem) ([]byte, error) {
	return json.Marshal(struct {
		TxId   string `json:"txid,omitempty"`
		Vout   uint32 `json:"vout"`
		Amount uint64 `json:"amount"`
		Pubkey string `json:"pubkey"`
	}{
		TxId:   hex.EncodeToString(u.TxId[:]),
		Vout:   u.Vout,
		Amount: u.Amount,
		Pubkey: hex.EncodeToString(u.Pubkey[:]),
	})
}

func refUTXOItemLight(u UTXOItemLight) ([]byte, error) {
	return json.Marshal(struct {
		Vout   uint32 `json:"vout"`
		Amount uint64 `json:"amount"`
		Pubkey string `json:"pubkey"`
	}{
		Vout:   u.Vout,
		Amount: u.Amount,
		Pubkey: hex.EncodeToString(u.Pubkey[:]),
	})
}

func refFullTxItem(f FullTxItem) ([]byte, error) {
	inputs := make([]string, len(f.Inputs))
	for i, outpoint := range f.Inputs {
		inputs[i] = hex.EncodeToString(outpoint[:])
	}
	return json.Marshal(struct {
		TxId   string          `json:"txid"`
		Tweak  string          `json:"tweak"`
		Inputs []string        `json:"inputs"`
		UTXOs  []UTXOItemLight `json:"utxos"`
	}{
		TxId:   hex.EncodeToString(f.TxId[:]),
		Tweak:  hex.EncodeToString(f.Tweak[:]),
		Inputs: inputs,
		UTXOs:  f.UTXOs,
	})
}

func fill(b []byte, seed byte) {
	for i := range b {
		b[i] = seed + byte(i)
	}
}

func sampleTweaks(n int) TweakSlice {
	out := make(TweakSlice, n)
	for i := range out {
		fill(out[i][:], byte(i))
	}
	return out
}

func sampleShorts(n int) OutputsShort {
	out := make(OutputsShort, n)
	for i := range out {
		fill(out[i][:], byte(i*7))
	}
	return out
}

func sampleUTXO(i int) UTXOItem {
	var u UTXOItem
	fill(u.TxId[:], byte(i))
	fill(u.Pubkey[:], byte(i*3))
	u.Vout = uint32(i)
	u.Amount = uint64(i) * 1_000_003
	return u
}

func equal(t *testing.T, what string, got, want []byte) {
	t.Helper()
	if string(got) != string(want) {
		t.Errorf("%s wire format changed:\n got: %s\nwant: %s", what, got, want)
	}
}

func TestTweakSliceMatchesReference(t *testing.T) {
	// Nil and empty both have to keep producing [], not null.
	for _, n := range []int{0, 1, 2, 500} {
		v := sampleTweaks(n)
		got, err := v.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		want, err := refTweakSlice(v)
		if err != nil {
			t.Fatal(err)
		}
		equal(t, "TweakSlice", got, want)
	}
	var nilSlice TweakSlice
	got, _ := nilSlice.MarshalJSON()
	want, _ := refTweakSlice(nilSlice)
	equal(t, "nil TweakSlice", got, want)
}

func TestOutputsShortAndSpentIndexMatchReference(t *testing.T) {
	for _, n := range []int{0, 1, 3, 200} {
		o := sampleShorts(n)
		got, err := o.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		want, err := refOutputsShort(o)
		if err != nil {
			t.Fatal(err)
		}
		equal(t, "OutputsShort", got, want)

		// SpentIndex is the same shape and had the same implementation.
		s := SpentIndex(o)
		gotS, err := s.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		equal(t, "SpentIndex", gotS, want)
	}
}

func TestSpentOutpointsMatchesReference(t *testing.T) {
	for _, n := range []int{0, 1, 64} {
		v := make(SpentOutpoints, n)
		for i := range v {
			fill(v[i][:], byte(i))
		}
		got, err := v.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		ref := make([]string, len(v))
		for i := range v {
			ref[i] = hex.EncodeToString(v[i][:])
		}
		want, err := json.Marshal(ref)
		if err != nil {
			t.Fatal(err)
		}
		equal(t, "SpentOutpoints", got, want)
	}
}

func TestComputeIndexItemMatchesReference(t *testing.T) {
	for _, n := range []int{0, 1, 5} {
		var c ComputeIndexItem
		fill(c.TxId[:], 0x10)
		fill(c.Tweak[:], 0x20)
		c.OutputsShort = sampleShorts(n)

		got, err := c.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		want, err := refComputeIndexItem(c)
		if err != nil {
			t.Fatal(err)
		}
		equal(t, "ComputeIndexItem", got, want)
	}
}

func TestUTXOItemMatchesReference(t *testing.T) {
	for i := 0; i < 8; i++ {
		u := sampleUTXO(i)
		got, err := u.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		want, err := refUTXOItem(u)
		if err != nil {
			t.Fatal(err)
		}
		equal(t, "UTXOItem", got, want)
	}

	// A zero txid still has to be emitted: "omitempty" never fired on the hex
	// of a fixed-size array, so dropping the field would be a silent change.
	var zero UTXOItem
	got, _ := zero.MarshalJSON()
	want, _ := refUTXOItem(zero)
	equal(t, "zero UTXOItem", got, want)
}

func TestUTXOItemLightMatchesReference(t *testing.T) {
	for i := 0; i < 8; i++ {
		var u UTXOItemLight
		fill(u.Pubkey[:], byte(i))
		u.Vout = uint32(i)
		u.Amount = uint64(i) * 7919
		got, err := u.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		want, err := refUTXOItemLight(u)
		if err != nil {
			t.Fatal(err)
		}
		equal(t, "UTXOItemLight", got, want)
	}
}

func TestFullTxItemMatchesReference(t *testing.T) {
	build := func(nIn, nOut int, nilUTXOs bool) FullTxItem {
		var f FullTxItem
		fill(f.TxId[:], 0x30)
		fill(f.Tweak[:], 0x40)
		f.Inputs = make(SpentOutpoints, nIn)
		for i := range f.Inputs {
			fill(f.Inputs[i][:], byte(i))
		}
		if !nilUTXOs {
			f.UTXOs = make([]UTXOItemLight, nOut)
			for i := range f.UTXOs {
				fill(f.UTXOs[i].Pubkey[:], byte(i))
				f.UTXOs[i].Vout = uint32(i)
				f.UTXOs[i].Amount = uint64(i * 11)
			}
		}
		return f
	}

	cases := []FullTxItem{
		build(0, 0, true),  // nil UTXOs must stay null, not []
		build(0, 0, false), // empty UTXOs must stay []
		build(3, 4, false),
	}
	for _, f := range cases {
		got, err := f.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		want, err := refFullTxItem(f)
		if err != nil {
			t.Fatal(err)
		}
		equal(t, "FullTxItem", got, want)
	}
}

// Whole responses, marshalled the way gin marshals them, so the nesting is
// covered and not only the leaf types.
func TestWholeResponsesMatchReference(t *testing.T) {
	bi := BlockIdentifier{BlockHash: make([]byte, 32), BlockHeight: 840_000}
	fill(bi.BlockHash, 0x50)

	tweakResp := TweakIndexResponse{BlockIdentifier: bi, Index: sampleTweaks(3)}
	got, err := json.Marshal(tweakResp)
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		BlockIdentifier struct {
			BlockHash   string `json:"block_hash"`
			BlockHeight uint32 `json:"block_height"`
		} `json:"block_identifier"`
		Index []string `json:"index"`
	}
	if err := json.Unmarshal(got, &probe); err != nil {
		t.Fatalf("tweak response is not valid JSON: %v\n%s", err, got)
	}
	if probe.BlockIdentifier.BlockHeight != 840_000 {
		t.Errorf("block height = %d, want 840000", probe.BlockIdentifier.BlockHeight)
	}
	if len(probe.Index) != 3 {
		t.Fatalf("index has %d entries, want 3", len(probe.Index))
	}
	if probe.Index[0] != hex.EncodeToString(tweakResp.Index[0][:]) {
		t.Errorf("tweak 0 = %s, want %s", probe.Index[0], hex.EncodeToString(tweakResp.Index[0][:]))
	}
}

func BenchmarkTweakSliceMarshal(b *testing.B) {
	v := sampleTweaks(500)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := v.MarshalJSON(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTweakSliceMarshalReference(b *testing.B) {
	v := sampleTweaks(500)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := refTweakSlice(v); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUtxoResponseMarshal(b *testing.B) {
	items := make([]UTXOItem, 1000)
	for i := range items {
		items[i] = sampleUTXO(i)
	}
	resp := struct {
		BlockIdentifier BlockIdentifier `json:"block_identifier"`
		Index           []UTXOItem      `json:"index"`
	}{
		BlockIdentifier: BlockIdentifier{BlockHash: make([]byte, 32), BlockHeight: 1},
		Index:           items,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(resp); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkComputeIndexItemMarshal(b *testing.B) {
	var c ComputeIndexItem
	fill(c.TxId[:], 1)
	fill(c.Tweak[:], 2)
	c.OutputsShort = sampleShorts(4)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.MarshalJSON(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkComputeIndexItemMarshalReference(b *testing.B) {
	var c ComputeIndexItem
	fill(c.TxId[:], 1)
	fill(c.Tweak[:], 2)
	c.OutputsShort = sampleShorts(4)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := refComputeIndexItem(c); err != nil {
			b.Fatal(err)
		}
	}
}
