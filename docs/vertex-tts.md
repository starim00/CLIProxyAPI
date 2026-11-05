# Vertex native text-to-speech

The default Vertex catalog includes `gemini-3.8-flash-tts`, including when the remote catalog omits it. Credential prefixes, explicit model allowlists, and exclusions still apply. TTS models do not receive anti-truncation aliases.

With a credential prefix of `vertex`, send a Gemini-native request to:

```http
POST /v1beta/models/vertex/gemini-3.8-flash-tts:generateContent
Authorization: Bearer <CPA_API_KEY>
Content-Type: application/json
```

```json
{
  "contents": [{"role": "user", "parts": [{"text": "Hello, welcome!"}]}],
  "generationConfig": {
    "responseModalities": ["AUDIO"],
    "speechConfig": {"voiceConfig": {"voice": "Kore"}}
  }
}
```

For SSE streaming use `:streamGenerateContent?alt=sse`. Audio remains in `candidates[].content.parts[].inlineData`; decode `data` from base64 and use `mimeType` to determine its format. Native speech configuration passes through to Google.

This enables the Gemini-native endpoints, not OpenAI `/v1/audio/speech` or Chat Completions audio-output conversion. Google project permissions and quota are still required.
