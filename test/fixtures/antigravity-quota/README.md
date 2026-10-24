# Antigravity quota contract fixtures

These are **synthetic, protocol-derived fixtures**, not captured Google responses.
Messages, domains, reset times, and the presence of details are test choices.
There is no assertion that every 429 has this exact body or all these fields.
No account was deliberately exhausted and no credentials were used to obtain them.

Evidence inspected on 2026-10-24:

- Gemini macOS 1.135.1.1291, bundled `Contents/Helpers/agy`.
- Binary SHA-256: `f8959099efc203051c38a22baa3b4ece2a4718b861751ee3811bb7e8253e8128`.
- Go pclntab identifies `cortex/utils/utils.extractErrorInfoReasonFromHTTPError`
  at `0x101276c60`. Disassembly shows JSON decoding of the outer error and
  protobuf-JSON decoding, followed by matching the Any type URL
  `type.googleapis.com/google.rpc.ErrorInfo` and reading its reason.
- `extractRetryInfoFromHTTPError` is at `0x101276750`;
  `ExtractRetryDelayFromError`, at `0x101277bd0`, calls it and converts the
  protobuf Duration to a retry delay. GenAI errors separately read `retryDelay`.
- `classifyGenAIAPIError`, at `0x1012772f0`, compares reason values
  `QUOTA_EXHAUSTED`, `RATE_LIMIT_EXCEEDED`, `MODEL_CAPACITY_EXHAUSTED`, and
  `VALIDATION_REQUIRED`. Some literals are compiler-inlined word comparisons;
  absence from a plain strings search does not mean absence from the client.
- The bundled protobuf descriptor defines POST
  `/v1internal:retrieveUserQuotaSummary`, request `project`, and response `groups`
  containing `buckets`. Root `buckets` is deprecated. `QuotaSummaryBucket` fields:
  `bucket_id=1`, `display_name=2`, `window=3`, `remaining_fraction=4` OR
  `remaining_amount=5` (oneof), `reset_time=6`, `description=7`, `disabled=8`.
  Proto JSON uses camelCase, an RFC3339 Timestamp, and string-encoded int64.
- `quotaResetDelay` occurs in the transcription quota helper in this build;
  it is **not** evidence that every generation 429 contains that metadata key.

Supporting public protocol definitions:

- https://google.aip.dev/193
- https://github.com/googleapis/googleapis/blob/master/google/rpc/error_details.proto

This establishes what the inspected client can consume. It cannot establish
which optional fields a future backend response will actually include. A future
redacted real capture can supplement these fixtures without changing provenance.

Run `python3 tools/inspect_agy_quota.py /path/to/agy` to reproduce the bounded
symbol/descriptor inventory without launching the app or reading its auth store.
Use `xcrun llvm-objdump --disassemble --start-address=... --stop-address=... agy`
for the corresponding function ranges on macOS. Addresses change with versions.
