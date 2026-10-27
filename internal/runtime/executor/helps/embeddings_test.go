package helps

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestEmbeddingValidation(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"model":"m","input":null}`, `{"model":"m","input":[]}`, `{"model":"m","input":[null]}`, `{"model":"m","input":[1,2]}`, `{"model":"m","input":"x","dimensions":1.5}`, `{"model":"m","input":"x","dimensions":0}`, `{"model":"m","input":"x","encoding_format":"other"}`} {
		if _, err := ParseEmbeddingRequest([]byte(body)); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
	for _, body := range []string{`{"model":"m","input":"x"}`, `{"model":"m","input":["x","y"],"dimensions":768}`} {
		if _, err := ParseEmbeddingRequest([]byte(body)); err != nil {
			t.Errorf("rejected %s: %v", body, err)
		}
	}
}

func TestEmbeddingUpstreamValidation(t *testing.T) {
	for _, body := range []string{`{}`, `{"embedding":{"values":[]}}`, `{"embedding":{"values":[null]}}`, `{"embedding":{"values":["bad"]}}`} {
		if _, _, err := DecodeEmbedding([]byte(body), false); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
	if _, _, err := DecodeEmbedding([]byte(`{"predictions":[{"embeddings":{"values":[1],"statistics":{"truncated":true}}}]}`), true); err == nil {
		t.Fatal("accepted truncated vector")
	}
}

func TestEmbeddingCancellationAndFailure(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if cancelFirst {
			cancel()
		}
		calls := 0
		req := coreexecutor.Request{Model: "gemini-embedding-001", Payload: []byte(`{"model":"gemini-embedding-001","input":["one","two"]}`)}
		_, err := ExecuteGoogleEmbeddings(ctx, &config.Config{}, nil, req, coreexecutor.Options{}, "http://localhost/embed", false, NewUsageReporter(ctx, "gemini", req.Model, nil), func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"quota"}}`))}, nil
		})
		cancel()
		if err == nil {
			t.Fatal("failure swallowed")
		}
		if cancelFirst {
			if calls != 0 || err != context.Canceled {
				t.Fatalf("cancellation: %d %v", calls, err)
			}
		} else {
			if calls != 1 || err.(interface{ StatusCode() int }).StatusCode() != 429 {
				t.Fatalf("upstream status: %d %v", calls, err)
			}
		}
	}
}
