# Text embeddings

`POST /v1/embeddings` accepts an OpenAI-compatible text embedding request and
routes it through OpenAI-compatible, Gemini API-key or Vertex API-key/service-account credentials.
Existing credential prefixes, model aliases, exclusions and
`routing.force-model-prefix` apply. Antigravity and anti-truncation models are
not embedding providers.

```json
{
  "model": "my-team/gemini-embedding-001",
  "input": ["First document", "Second document"],
  "dimensions": 768,
  "encoding_format": "float"
}
```

The response contains `object: "list"`, a model name, ordered
`data` entries with `object`, `index`, and `embedding`, and `usage` with
`prompt_tokens` and `total_tokens`. `encoding_format` may be `float` (default)
or `base64` (little-endian float32 bytes). `dimensions` is optional and must
be a positive integer supported by the upstream model. `task_type` and
`title` are optional Google extensions.

Input may be one non-empty string or an array of 1–2048 non-empty strings.
Native OpenAI-compatible providers additionally accept integer token-ID arrays
and arrays of token-ID arrays. Google credentials reject token-ID inputs because
their tokenizers differ. Multimodal inputs are not supported.

OpenAI-compatible providers forward the entire batch to their configured base
URL plus /embeddings. Input, dimensions, encoding_format, user and other request
fields are preserved. Responses retain upstream vectors and usage without
conversion. HTTP error statuses and retry hints remain available to the existing
credential scheduler.

Official OpenAI API keys configured in the Codex API-key group are supported.
When the base URL host is api.openai.com and no model allowlist is configured,
the catalog also includes text-embedding-3-small, text-embedding-3-large and
text-embedding-ada-002. Explicit model allowlists still take precedence.
ChatGPT/Codex OAuth credentials are excluded from embedding credential selection.
For other OpenAI-compatible providers, configure the upstream embedding models
and any desired aliases in that provider's model list.

The default Gemini and Vertex catalogs include `gemini-embedding-001` and
`gemini-embedding-2`. Explicit credential model allowlists remain authoritative;
add the embedding models there when using an allowlist. Model visibility is
not proof of upstream access: project permissions, billing, quotas and model
region availability still apply.

For Google providers, each text is sent separately, in order, with the same selected credential.
Gemini uses `embedContent`; Vertex's earlier text embedding models use
`predict`, while `gemini-embedding-2` uses `embedContent`. Input truncation is
disabled. User payload rules apply once to each translated request, last.
No generation settings, conversation messages, or anti-truncation tools are
injected. An upstream failure fails the entire batch, without partial vectors.
Normal credential retry/fallback may repeat already completed batch inputs.

Usage sums token counts supplied by Google. When an upstream response provides
no token count, that input contributes zero; CPA does not invent a tokenizer
estimate. Embeddings are returned unchanged without additional normalization.

References:
- https://ai.google.dev/api/embeddings
- https://docs.cloud.google.com/gemini-enterprise-agent-platform/reference/models/text-embeddings-api
- https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/embeddings/get-multimodal-embeddings
