# Toolfeedback producer contract

Status: prepared M3 source; runtime validation and integration are deferred.
The Huyang producer is implemented here. Consumer matching, administrative key
provisioning, and CLI producers are separate homelab-cli work.

## Original-call correlation: tf-call-v1

An enabled tool-call event retains its existing completion `ts`,
`host`, `client`, `session`, `proc`, `seq`, outcome, and M3 fields.
It additionally records original handler-entry time as millisecond UTC
`started_at`, for example `2026-10-17T08:00:00.123Z`. A missing or
zero start time is omitted, never reconstructed from completion.

When strict input and a provisioned key are available:

```json
{
  "correlation": {
    "v": "tf-call-v1",
    "kind": "mcp",
    "kid": "00112233445566778899aabbccddeeff",
    "mac": "<64 lowercase hexadecimal characters>"
  }
}
```

The kid above is public fixture material, not an operational key identifier.
A MAC is not a call ID: repeated identical calls in one key epoch intentionally
produce identical MACs. Neither session identity nor timing enters the MAC.

The producer captures `request.Params.Arguments` immediately after the
transport size check, before DecodeArguments, NormalizeArguments, aliases,
workspace defaults, relative root resolution, or dispatch. The independent
strict JSON path preserves exact numbers and treats JSON within a string as
a string. An immutable metadata value reaches both ordinary and validation
refusal logging. Oversized transport input is refused without correlation;
strict supported JSON rejected later by decoding or schema validation remains
correlatable.

## Canonical bytes

Parse one complete UTF-8 JSON value, preserving decimal number text. Reject
duplicate keys after escape decoding, invalid UTF-8, unpaired UTF-16 surrogate
escapes, non-JSON numbers (NaN/Infinity), trailing data, raw inputs over
1,048,576 bytes, and depth over 64 containers. The root container has depth 1;
scalars within 64 nested containers are supported. Bounds include whitespace.
Never truncate. Distinct Unicode normalizations remain distinct.

Encode recursively, with byte lengths and counts in unsigned decimal without
leading zeros:

- Null: `z`; false: `f`; true: `t`.
- String: `s<byte-length>:<UTF-8 bytes>`.
- Number: `n<byte-length>:<normalized decimal>`.
- Array: `a<count>:` followed by encoded elements.
- Object: `o<count>:` followed by encoded string-key/value pairs, in
  unsigned UTF-8 key-byte order.

Normalize nonzero numbers to signed integer coefficient + `e` + exponent,
with coefficient leading and trailing zeros removed and exponent adjusted.
Positive coefficients and exponents have no plus sign. Every zero, including
negative zero, is `0`. Thus `1`, `1.0`, and `1e0` all encode as
`n3:1e0`; `123.4500` is `n8:12345e-2`. Neither coefficient nor exponent
is rounded through floating point, and large exponents are not expanded into
coefficient digits.

The HMAC-SHA256 message is exactly:

```text
Encode(["tf-call-v1", "mcp", "huyang", canonical_tool_name, original_arguments])
```

The registered descriptor supplies the producer's canonical tool name.
Consumer aliases must be explicit and Huyang-scoped, such as
`mcp__huyang__read` and `huyang_read` mapping to `read`; unrelated
namespaces never match by suffix. Do not reproduce Huyang's argument
normalization downstream: `root: "."`, omitted root, aliases, nested
options, and embedded JSON strings retain their original shapes.

[Public vectors](../internal/service/testdata/friction-correlation-v1.json)
specify exact canonical UTF-8 strings, rejection cases, the public synthetic
32-byte key `00 01 … 1f`, and the literal read-message prefix
`a5:s10:tf-call-v1s3:mcps6:huyangs4:read`. Append the canonical argument
bytes to that prefix and use the fixture key with standard HMAC-SHA256.
Go and Python must consume the same vectors; no live keys or transcripts are
needed to validate this protocol.

## Host-local key files

Use `$XDG_CONFIG_HOME/toolfeedback/correlation`, defaulting to
`$HOME/.config/toolfeedback/correlation` on Unix:

```text
active.json                 {"kid":"<32 lowercase hex characters>"}
keys/<kid>.key              exactly 32 raw bytes (no hex/base64/newline)
```

Additional active.json metadata is ignored; malformed/duplicate metadata is
rejected. active.json is bounded to 4096 bytes. The toolfeedback, correlation,
and keys directories must belong to this Unix UID with mode 0700. Both active
and key files must be regular files belonging to this UID, mode 0600. The
reader rejects symlinks in every directory component and at either file;
nonblocking open also prevents special files such as FIFOs from hanging a call.
It validates opened descriptors, not a separate path check followed by open.

Only the separately implemented administrative
`toolfeedback correlation init-key` command provisions keys. The producer
never creates, repairs, rotates, or replaces them, and never prints a secret.
Publish active.json atomically after writing the new key, retaining old keys
for the spool retention period. This producer rereads the active key per
supported call, so rotation needs no daemon restart. Readers select retained
keys by event kid. Missing, unsafe, malformed, or deleted keys yield absent
correlation, never a fallback key or unkeyed hash.

Provision once per participating host and Unix account during the planned
deployment. The service and reader must share this config location; setting it
only on the adapter does not configure the service. Keep the key directory
outside spool, pull destinations, and shipping inputs. Keys remain on the
originating host, with pulled events resolved there through the remote workflow.

## Privacy, costs, and unavailable evidence

Explicitly disabled capture (`HUYANG_FRICTION=0`) returns before
canonicalization, key access, or spool access. The existing opt-in marker check
is unchanged for environments without an explicit flag. Supported enabled
calls perform bounded parsing, canonical encoding, local private key reads,
and HMAC-SHA256. No input bytes, secret key, private path, native call ID,
arbitrary MCP metadata, or unkeyed argument digest is copied into correlation
fields or correlation errors. Errors are fixed internal values, silently
discarded; the tool response remains authoritative and unchanged.

The MAC reveals equality within a host-key epoch. It does not provide an
offline dictionary-checkable unkeyed digest to Loki. Existing names-only args,
basename-only project roots, sub_codes, has_results, diagnostic metadata,
and once-per-connection capability names remain in place.

The consumer must require the originating host, compatible client family,
supported version, retained key, and explicit call-start time. Equal MACs
within ±5 seconds are candidates, not proof of a unique call. Resolve unique
native session/call identities across the complete bounded scan before
limiting display count, reject conflicting or ambiguous candidates, and render
only exact matched call IDs. Preserve producer session identity separately.
A MAC with no candidate means correlation not established, not not_found.

Historical events without correlation cannot be safely backfilled from error
similarity. Wrapped code-mode calls without explicit nested-call records remain
unavailable. Missing/unsupported input, keys, time, and incomplete transcript
scans must remain unavailable rather than weakening matching. Native adapter
session strings are producer identities; no native-ID provenance is invented.
