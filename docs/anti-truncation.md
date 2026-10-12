# Gemini and Antigravity anti-truncation

This fork provides two optional synthetic-answer strategies. The original strategy
follows the Kemini_Dramatron_v3.1 preset's transport script with bounded continuation.
The v2 strategy follows Antigravity-gateway's default injection and empty-retry design.
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
disabled by default. Enabling it adds opt-in `抗截断/<model ID>` and
`抗截断-v2/<model ID>` entries to model
lists for supported models. Ordinary model IDs retain their original behavior.
Select a prefixed entry for streaming or non-streaming anti-truncation generation:

- `gemini-3.7-flash` → `抗截断/gemini-3.7-flash`
- `api/Gemini 3.7 Flash` → `抗截断/api/Gemini 3.7 Flash`
- `gemini-3.8-flash-high` → `抗截断-v2/gemini-3.8-flash-high`

The outer prefix is removed before credential selection and upstream translation.
Existing aliases, credential prefixes, and thinking suffixes are preserved.
Patterns match resolved upstream model IDs; an empty list defaults to `gemini-*`.
Antigravity Claude models can be selected explicitly with `claude-*`.
`max-attempts` includes the first request, defaults to 3 for non-positive values,
and is capped at 10. Each additional attempt consumes upstream quota and tokens.

## Original strategy: 抗截断/

- The request adds `emit_complete_response_<24 lowercase hex characters>(content: string)`.
  The suffix comes from 12 cryptographically random bytes; existing tool names are
  checked for collisions. A continuation keeps the same name. Only that exact tool
  is consumed; client tools with the same prefix remain client tools.
- For OpenAI chat requests, injection happens before protocol translation. A system
  control message is inserted immediately before the first message whose string
  content contains `<format>`. Without that anchor it is appended as a system message,
  or as a user message when the last message is an assistant message. The tool schema,
  description and control prompt match the preset script. Existing tools are preserved
  and `tool_choice` is set to `auto`. Empty message lists and caller choices `none`,
  `required`, or an object bypass injection and continuation entirely.
- Other client protocols use native Gemini injection after translation with the same
  randomized name, schema and control prompt. Native Gemini has no inline system role:
  the prompt is appended to `systemInstruction`, or as a user turn after a model turn.
  Native `NONE`, `ANY`, or a function allowlist bypass injection. CPA's translators
  retain their existing treatment of inline system messages, including system-reminder
  wrappers where required by the destination protocol.
- Synthetic tool results become normal assistant text before CPA translates the
  response to OpenAI, Gemini, Claude, or Responses format. Real tool calls retain
  their arguments and signatures and are returned to the client without continuation.
- A normal `STOP` response is accepted even if the model ignores the synthetic tool.
  A complete synthetic tool answer with no finish marker is also accepted.
- `MAX_TOKENS`, a missing finish marker, or recoverable read interruptions can
  trigger another request using the same credential. The original conversation
  plus the accumulated visible answer and a continuation instruction is rebuilt
  for every attempt; previous continuation prompts are not appended repeatedly.
  This bounded continuation is CPA behavior in addition to the preset's injection.
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

## Gateway strategy: 抗截断-v2/

The reference is [Antigravity-gateway v1.0.9, commit e83fc1a5](https://github.com/Xeltra233/Antigravity-gateway/tree/e83fc1a505e501d3ac6a13c12816df1ceb55b86b).
Its default `prefer` mode, non-strict schema, system control role and tail placement
are adapted to CPA's Gemini and Antigravity native transports.

- Each request adds `agw_emit_<24 random lowercase hex characters>(content: string)`.
  The tool description and control prompt match the reference. Existing tools remain
  available. OpenAI `none` becomes `auto`; `required` and a specifically forced real
  tool are preserved. This deliberately differs from v1's bypass policy.
- The control prompt goes at the message tail. The last conversational role is found
  by skipping trailing system/developer messages. Assistant/model endings and empty
  or system-only conversations receive a user control turn; other endings receive a
  system control message. V2 does not search for a `<format>` anchor.
- Native Gemini input uses the same tool and prompt. An empty conversation or final
  model turn receives a user control turn; otherwise the prompt goes in the native
  system instruction. `NONE` becomes `AUTO`; other calling modes and allowlists remain.
- Only the exact injected tool name is extracted. Its content takes precedence over
  ordinary answer text, so the two sources are never concatenated. Genuine tool calls,
  arguments, signatures and reasoning parts remain available to downstream translation.
- Each decoded native SSE event containing synthetic content is forwarded immediately.
  Plain-text fallback is held until the response ends to prevent duplicate answers if
  a synthetic call arrives later. CPA receives native argument objects, not the reference
  gateway's OpenAI argument-string deltas; it therefore does not import that byte-stream
  decoder. Native `partialArgs` is not supported by this strategy.
- A malformed own-tool argument **string** can be repaired locally: Markdown fences,
  trailing commas, unclosed strings/braces, and raw newline/tab characters are handled
  within the 16 MiB limit. Native argument objects must contain a string `content`.
  Wrong schemas, incomplete escapes and malformed upstream envelopes remain errors;
  repair does not guess alternative field names or modify real tools.
- Empty replies retry the original prepared request with the same tool name and no
  added conversation history. `max-attempts` is the shared CPA total-attempt limit
  (default 3), rather than the reference's separate default of 3 additional retries.
  Exhausted empty responses return an error. Replies containing tool calls, reasoning,
  non-text parts, blocking reasons or output-limit finish reasons are not retried.
- V2 never appends continuation requests for nonempty truncated output; it preserves
  the upstream finish reason. Transport errors remain errors. Usage from empty attempts
  is included, cancellation remains active, and payload rules run last once per attempt.
- Non-text image/audio/video/embedding model names and canonical aliases do not receive
  v2 entries. The ordinary and v1 model entries remain available according to their
  existing eligibility rules. V2 shares the existing enable flag and model allowlist.

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
