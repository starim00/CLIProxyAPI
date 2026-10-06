# Gemini and Antigravity anti-truncation

This fork provides an optional synthetic-answer tool and bounded continuation,
corresponding to the anti-truncation workflow in
[gcli2api](https://github.com/su-kaka/gcli2api/blob/master/src/converter/anti_truncation.py).
The Go implementation uses CPA's existing executors and protocol translators.

## Configuration

For v8 configuration:

```yaml
requests:
  streaming:
    keepalive-seconds: 15
    anti-truncation:
      enabled: true
      models: ["gemini-*"]
      max-attempts: 3
```

Merge this into the existing `requests.streaming` mapping. With the legacy
configuration layout, put `streaming` at the top level instead. The feature is
disabled by default. Enabling it adds opt-in `抗截断/<model ID>` entries to model
lists for supported models. Ordinary model IDs retain their original behavior.
Select a prefixed entry for streaming or non-streaming anti-truncation generation:

- `gemini-3.7-flash` → `抗截断/gemini-3.7-flash`
- `api/Gemini 3.7 Flash` → `抗截断/api/Gemini 3.7 Flash`

The outer prefix is removed before credential selection and upstream translation.
Existing aliases, credential prefixes, and thinking suffixes are preserved.
Patterns match resolved upstream model IDs; an empty list defaults to `gemini-*`.
Antigravity Claude models can be selected explicitly with `claude-*`.
`max-attempts` includes the first request, defaults to 3 for non-positive values,
and is capped at 10. Each additional attempt consumes upstream quota and tokens.

## Behavior

- The request adds `cpa_emit_answer(content: string)` without replacing existing
  tools or system instructions. Its name is reserved while this feature is enabled.
- Synthetic tool results become normal assistant text before CPA translates the
  response to OpenAI, Gemini, Claude, or Responses format. Real tool calls retain
  their arguments and signatures and are returned to the client without continuation.
- A normal `STOP` response is accepted even if the model ignores the synthetic tool.
  A complete synthetic tool answer with no finish marker is also accepted.
- `MAX_TOKENS`, a missing finish marker, or recoverable read interruptions can
  trigger another request using the same credential. The original conversation
  plus the accumulated visible answer and a continuation instruction is rebuilt
  for every attempt; previous continuation prompts are not appended repeatedly.
- Explicit refusal/blocking finish reasons and prompt blocks are returned without
  automatic continuation. HTTP failures and malformed response events are reported.
- User payload rules run last on every attempt. Overrides and filters can therefore
  change/remove the injected instructions or tools; they are never silently restored
  after finalization. Do not override the entire conversation if you need continuation.
- Streamed answers are buffered per attempt to discard duplicated ordinary text when
  a synthetic answer is also present. The client receives each completed attempt as
  a chunk, not token-by-token. SSE keepalive is recommended during the wait. This is
  not a separate fake-streaming mode: streaming still uses the upstream SSE endpoint.
- Usage token counters from completed attempts are summed for the final response.
  Canceling/closing the downstream response cancels the active upstream request.
- The response buffer is capped at 16 MiB across attempts. Exhaustion or an unresolved
  truncation at the attempt limit returns an error rather than a fabricated success.

## Scope and limits

Supported paths are Gemini `generateContent` / `streamGenerateContent` with API keys
and Antigravity generation (including its streaming-to-non-streaming adapter).
Home dispatch, plugin executors, native Interactions, and image endpoints do not
support prefixed models. Token counting accepts a prefixed model but counts the
ordinary request without synthetic tools or continuation. Other providers are unchanged. Multiple
response candidates are not supported with this feature enabled.

Continuation asks the model to resume; it is not a token-cursor replay protocol.
The model can repeat text or change its answer, and a natural early `STOP` cannot be
reliably identified as a truncation. This does not guarantee an unrestricted or
complete answer. Tests use local mock upstreams and do not establish live-model behavior.

Request diagnostics see reconstructed responses; no synthetic function calls are
exposed to the client. New Antigravity reasoning replay cache entries are disabled
for these reconstructed turns. Disable anti-truncation to inspect the
unmodified upstream stream when diagnosing provider-specific behavior.

## Verification

```sh
go test ./internal/config ./internal/runtime/executor/helps ./internal/runtime/executor ./sdk/api/handlers/... -run AntiTruncation -count=1
go build -o cli-proxy-api ./cmd/server
```
