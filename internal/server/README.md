
# Server HTTP API Specification

This document describes the HTTP API endpoints for the BlindBit Oracle server.

## Endpoint Types

The API provides the following endpoint types:
- **Tweaks** - Simple list of tweaks (33-byte public keys)
- **Outputs/UTXOs** - UTXO information for blocks
- **Spent Outputs** - Shortened spent output information
- **Compute Index** - Compact transaction index with tweak mappings
- **Full Block** - Complete block data with all transaction details

## API Endpoints

### Tweaks

Returns a simple list of tweaks as 33-byte public keys. For bandwidth-constrained clients, using 64/65-byte keys might be more ideal. For mappings with transaction IDs, use the Compute Index endpoint instead.

**Response Format:**
```json
{
    "block_identifier": {
        "block_hash": "0000003223acbdef....",
        "block_height": 894012
    },
    "index": [
        "03<x-only pubkey>",
        "02<x-only pubkey>",
        "03<x-only pubkey>",
        "03<x-only pubkey>",
        "02<x-only pubkey>",
        "03<x-only pubkey>",
        "02<x-only pubkey>"
    ]
}
```

### Outputs/UTXOs

Returns UTXO information for a specific block.

**Response Format:**
```json
{
    "block_identifier": {
        "block_hash": "0000003223acbdef....",
        "block_height": 894012
    },
    "index": [
        {
            "txid": "deadbeef",
            "vout": 0,
            "pubkey": "<x-only pubkey>",
            "amount": 210042
        },
        {
            "txid": "deadbeef",
            "vout": 1,
            "pubkey": "<x-only pubkey>",
            "amount": 220042
        },
        {
            "txid": "beefdead",
            "vout": 3,
            "pubkey": "<x-only pubkey>",
            "amount": 310021
        }
    ]
}
```

### Spent Outputs (Shortened)

Returns spent output information in a compact format using the first 8 bytes of output x-only pubkeys as an array of hex strings.

**Response Format:**
```json
{
    "block_identifier": {
        "block_hash": "0000003223acbdef....",
        "block_height": 894012
    },
    "index": [
        "12345acbdef12345",
        "67890fedcba98765",
        "abcdef1234567890"
    ]
}
```
_Open question: Should we jump straight to outpoints and not do this with shortened outputs?_

### Compute Index

Returns a compact transaction index with tweak mappings and output information.

**Response Format:**
```json
{
    "block_identifier": {
        "block_hash": "0000003223acbdef....",
        "block_height": 894012
    },
    "index": [
        {
            "txid": "deadbeef987654",
            "tweak": "02deadbeef",
            "outputs": [
                "12345acbdef12345",
                "67890fedcba98765",
                "abcdef1234567890"
            ]
        },
        {
            "txid": "beefdead1234",
            "tweak": "02deadbeef",
            "outputs": [
                "fedcba9876543210",
                "13579bdf2468ace0"
            ]
        },
        {
            "txid": "beef987654dead",
            "tweak": "02deadbeef",
            "outputs": [
                "2468ace13579bdf0"
            ]
        }
    ]
}
```

### Full Block

Returns complete block data with all transaction details and spent outpoints accelerator index. This endpoint provides comprehensive information but should be used sparingly due to the large amount of data.

**Response Format:**
```json
{
    "block_identifier": {
        "block_hash": "0000003223acbdef....",
        "block_height": 894012
    },
    "index": [
        {
            "txid": "deadbeef987654",
            "tweak": "02deadbeef",
            "inputs": [
                "<36byte outpoint hex>",
                "<36byte outpoint hex>",
            ],
            "utxos": [
                {
                    "vout": 0,
                    "pubkey": "<x-only pubkey>",
                    "amount": 210042
                },
                {
                    "vout": 1,
                    "pubkey": "<x-only pubkey>",
                    "amount": 220042
                }
            ]
        },
        {
            "txid": "987654deadbeef",
            "tweak": "02beefdeadefddeefdad",
            "inputs": [
                "<36byte outpoint hex>",
                "<36byte outpoint hex>",
            ],
            "utxos": [
                {
                    "vout": 0,
                    "pubkey": "<x-only pubkey>",
                    "amount": 380042
                },
                {
                    "vout": 1,
                    "pubkey": "<x-only pubkey>",
                    "amount": 380021
                }
            ]
        }
    ]
}
```

## Range Endpoints

A scanner has to look at every block between its last scan and the tip, and the
per-block endpoints cost three HTTP round trips per block. Against a remote
oracle those round trips dominate a scan: ten thousand blocks is thirty thousand
requests, which is minutes of waiting regardless of how fast either end is.

The range endpoints serve a span of blocks in one request:

| Endpoint | Per-block equivalent |
| --- | --- |
| `GET /range/tweaks?start=<h>&end=<h>` | `/tweaks/:blockheight` |
| `GET /range/utxos?start=<h>&end=<h>` | `/utxos/:blockheight` |
| `GET /range/spent-outputs?start=<h>&end=<h>` | `/spent-outputs/:blockheight` |
| `GET /range/compute-index?start=<h>&end=<h>` | `/compute-index/:blockheight` |

`/range/compute-index` is the one a scanner should prefer. Unlike `/tweaks` it
carries the **txid each tweak belongs to**, and that pairing is what lets a
scanner test a tweak against the outputs of its own transaction rather than
enumerating every output the tweak could produce. For a wallet with four labels
that is the difference between one curve operation per output and nine per
tweak. It is also keyed by height rather than by txid, so the oracle serves it
with a contiguous scan instead of a seek per transaction.

`start` and `end` are both inclusive. The span may not exceed the server's
`max_range_blocks` (default 100); a larger request is rejected with `400`.

**Response format** — a `blocks` array whose entries are exactly the objects the
per-block endpoint returns, in ascending height order:

```json
{
    "blocks": [
        {
            "block_identifier": {
                "block_hash": "0000003223acbdef....",
                "block_height": 894012
            },
            "index": ["03<x-only pubkey>", "02<x-only pubkey>"]
        },
        {
            "block_identifier": {
                "block_hash": "0000004471fedcba....",
                "block_height": 894013
            },
            "index": ["02<x-only pubkey>"]
        }
    ]
}
```

Heights the oracle has not indexed — above its sync tip, or below its
`sync_start_height` — are **omitted** from the array rather than returned as
empty blocks, so a client can tell "no data for you here" apart from "this block
was never indexed". Callers should therefore key off each entry's
`block_identifier.block_height` rather than assuming the array is contiguous.

Responses are streamed. If the oracle hits a database error partway through, it
abandons the stream without its closing bracket, so the response fails to parse
rather than arriving as a short list that looks complete. **Clients must treat a
JSON parse failure as a failed request and retry**; treating it as an empty or
partial result would silently skip blocks.

### Client discovery

`/info` reports `max_range_blocks`. An oracle that omits the field, or reports
`0`, has no range endpoints — fall back to the per-block routes:

```json
{
  "network": "signet",
  "height": 894012,
  "tweaks_only": false,
  "tweaks_full_basic": true,
  "tweaks_full_with_dust_filter": false,
  "tweaks_cut_through_with_dust_filter": false,
  "max_range_blocks": 100
}
```

## Data Format Notes

- **Block Hash**: 32-byte block hash represented as hex string
- **Block Height**: Unsigned 32-bit integer
- **Transaction ID**: 32-byte transaction hash represented as hex string
- **Tweak**: 33-byte compressed public key
- **Output Short**: First 8 bytes of x-only pubkey represented as hex string
- **Spent Outpoint**: 36-byte outpoint (32-byte txid + 4-byte vout) represented as hex string
- **Amount**: Transaction output amount in satoshis

## Full Block Response Details

The Full Block endpoint includes:

- **index**: Array of transaction items with tweaks and UTXOs
- **spent_outpoints**: Array of all outpoints (previous transaction outputs) that were spent in this block
  - Each outpoint is 36 bytes: 32-byte previous transaction ID + 4-byte previous output index
  - Transaction IDs are reversed (little-endian) for consistency with Bitcoin conventions
  - This accelerator index provides efficient access to all spent outputs without requiring individual transaction parsing