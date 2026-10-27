package helps

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type embeddingError struct {
	code    int
	message string
}

func (e embeddingError) Error() string   { return e.message }
func (e embeddingError) StatusCode() int { return e.code }

// ExecuteGoogleEmbeddings uses the selected credential for the entire ordered batch.
func ExecuteGoogleEmbeddings(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options, endpoint string, predict bool, reporter *UsageReporter, send func(*http.Request) (*http.Response, error)) (result coreexecutor.Response, err error) {
	defer reporter.TrackFailure(ctx, &err)
	input, err := ParseEmbeddingRequest(req.Payload)
	if err != nil {
		return result, embeddingError{400, err.Error()}
	}
	if selected, _ := opts.Metadata[coreexecutor.AntiTruncationMetadataKey].(bool); selected {
		return result, embeddingError{400, "anti-truncation is unavailable for embeddings"}
	}
	input.Model = PayloadRequestedModel(opts, req.Model)
	vectors := make([][]float32, 0, len(input.Texts))
	var total int64
	for _, text := range input.Texts {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		original := EmbeddingPayload(input, text, predict)
		body := ApplyPayloadConfigWithRequest(cfg, req.Model, "gemini", opts.SourceFormat.String(), "", original, original, input.Model, "/v1/embeddings", opts.Headers)
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return result, err
		}
		r.Header.Set("Content-Type", "application/json")
		response, err := send(r)
		if err != nil {
			return result, err
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 16*1024*1024+1))
		_ = response.Body.Close()
		if readErr != nil {
			return result, readErr
		}
		if len(data) > 16*1024*1024 {
			return result, embeddingError{502, "embedding response exceeds 16 MiB"}
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return result, embeddingError{response.StatusCode, string(data)}
		}
		vector, tokens, err := DecodeEmbedding(data, predict)
		if err != nil {
			return result, embeddingError{502, err.Error()}
		}
		if len(vectors) > 0 && len(vector) != len(vectors[0]) {
			return result, embeddingError{502, "inconsistent embedding dimensions"}
		}
		vectors = append(vectors, vector)
		total += tokens
		result.Headers = response.Header.Clone()
	}
	result.Payload, err = EncodeEmbeddings(input, vectors, total)
	if err != nil {
		return result, fmt.Errorf("encode embeddings: %w", err)
	}
	reporter.Publish(ctx, usage.Detail{InputTokens: total, TotalTokens: total})
	return result, nil
}
