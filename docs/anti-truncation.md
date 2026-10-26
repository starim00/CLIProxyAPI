# Gateway anti-truncation

Select `抗截断/<model ID>` to use the gateway synthetic-answer strategy on
Gemini API, Antigravity, or native Vertex Gemini generation. Credential prefixes
are arbitrary and retained: `team/gemini-3.8-flash` becomes
`抗截断/team/gemini-3.8-flash`. Eligibility uses the registered canonical upstream
model, not the spelling of the credential prefix. Ordinary model IDs remain unchanged.

```yaml
requests:
  streaming:
    keepalive-seconds: 15
    anti-truncation:
      enabled: true
      models: ["gemini-*"]
      max-attempts: 3
```

The allowlist matches resolved upstream IDs. `max-attempts` includes the initial
request, defaults to 3 and is capped at 10. The feature is disabled by default.
Only one anti-truncation strategy is provided. `抗截断-v2/` is no longer listed or
accepted as a virtual prefix; clients must refresh their model selection.

## Request and response behavior

The implementation follows the default injection and empty-retry design of
[Antigravity-gateway](https://github.com/Xeltra233/Antigravity-gateway/tree/e83fc1a505e501d3ac6a13c12816df1ceb55b86b).

- A request gets an `agw_emit_<24 random lowercase hex characters>` tool with a
  required string `content` argument. Genuine client tools remain available.
- OpenAI chat control instructions are appended at the tail. Trailing system and
  developer messages are ignored when finding the last conversational role;
  assistant/model endings and empty/system-only conversations receive a user
  control turn. Other endings receive a system control message.
- Tool choice `none` becomes `auto`; `required` and explicitly forced real tools
  are preserved. Native Gemini requests use equivalent native tool declarations
  and system/user placement. User payload overrides remain the final semantic
  barrier and run exactly once per upstream attempt.
- Only the exact injected tool is converted into assistant text. Synthetic content
  replaces ordinary response text instead of being concatenated with it. Genuine
  tool calls and signatures remain intact.
- Synthetic content is emitted per decoded native SSE event. Ordinary fallback text
  waits until EOF to prevent duplicates. Native `partialArgs` is not supported.
- Own-tool argument strings can be repaired within the 16 MiB limit for Markdown
  fences, trailing commas, unclosed strings/braces and raw newline/tab characters.
  Wrong schemas, incomplete escapes, invalid envelopes and real tools are not repaired.
- Empty replies retry the original prepared request with the same tool name. No
  continuation prompts or accumulated answers are appended. Real tool calls,
  reasoning, non-text output, blocking reasons and output-limit finish reasons do
  not trigger retries. Exhausted empty replies return an error.
- Initial HTTP failures are preserved; cancellation closes the upstream request.
  These mechanisms do not guarantee that a model calls the tool or completes its answer.

## Scope

Vertex API keys and service accounts are supported for native Gemini
`generateContent` / `streamGenerateContent`. Vertex OpenAPI routing, native
Interactions, Home dispatch, plugins and image endpoints are not supported.
Non-text canonical model IDs do not receive virtual entries. Token counting uses
the ordinary model without tool injection. Multiple response candidates are rejected.

Credential routing remains unchanged: the outer anti-truncation prefix is removed
before authentication selection, while the credential prefix remains for routing.
`routing.force-model-prefix` retains its normal isolation behavior.

## Verification

```sh
go test ./internal/config ./internal/runtime/executor/helps ./internal/runtime/executor ./sdk/api/handlers/...
go build -o cli-proxy-api ./cmd/server
```
