package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntiTruncationOrdinaryRequestUnchanged(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{Streaming: config.StreamingConfig{AntiTruncation: config.AntiTruncationConfig{Enabled: true}}}}
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	out, anti, err := helps.PrepareAntiTruncationForRequest(cfg, "gemini-test", body, "", false, cliproxyexecutor.Options{})
	if err != nil || anti != nil || !bytes.Equal(out, body) {
		t.Fatalf("ordinary request changed: %s %v", out, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		if bytes.Contains(payload, []byte("cpa_emit_answer")) {
			t.Error("ordinary model injected synthetic tool")
		}
		data := `{"candidates":[{"content":{"role":"model","parts":[{"text":"ordinary"}]},"finishReason":"STOP"}]}`
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", data)
		} else {
			_, _ = io.WriteString(w, data)
		}
	}))
	defer server.Close()
	e := NewGeminiExecutor(cfg)
	auth := &cliproxyauth.Auth{ID: "ordinary", Provider: "gemini", Attributes: map[string]string{"api_key": "mock", "base_url": server.URL}}
	req := cliproxyexecutor.Request{Model: "gemini-test", Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
	if _, err := e.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatal(err)
	}
	stream, err := e.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
	}
}

// Exercise the production executors, their final payload rules and their
// downstream translators; no provider credentials or public API calls are used.
func TestAntiTruncationExecutors(t *testing.T) {
	for _, provider := range []string{"gemini", "antigravity", "antigravity-adapter"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", provider, stream), func(t *testing.T) {
				var requests [][]byte
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					requests = append(requests, body)
					text, finish := "first", "MAX_TOKENS"
					if len(requests) == 2 {
						text, finish = "second", "STOP"
					}
					data := fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"cpa_emit_answer","args":{"content":%q}}}]},"finishReason":%q}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`, text, finish)
					if provider != "gemini" {
						data = `{"response":` + data + `}`
					}
					if strings.Contains(r.URL.Path, "streamGenerateContent") {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(w, "data: %s\n\n", data)
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, data)
					}
				}))
				defer server.Close()
				cfg := &config.Config{SDKConfig: config.SDKConfig{Streaming: config.StreamingConfig{AntiTruncation: config.AntiTruncationConfig{Enabled: true, MaxAttempts: 3}}}}
				protocol := provider
				if provider == "antigravity-adapter" {
					protocol = "antigravity"
				}
				cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "gemini-*", Protocol: protocol, FromProtocol: "openai"}}, Params: map[string]any{"generationConfig.temperature": 0.42, "systemInstruction.parts.-1": map[string]string{"text": "final-marker"}}}}
				cfg.Payload.Filter = []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "gemini-*", Protocol: protocol}}, Params: []string{"generationConfig.topP"}}}
				auth := &cliproxyauth.Auth{ID: "anti-test-" + provider, Provider: protocol, Attributes: map[string]string{"api_key": "mock-key", "base_url": server.URL}, Metadata: map[string]any{"access_token": "mock-token", "project_id": "mock-project", "expiry": time.Now().Add(time.Hour).Format(time.RFC3339)}}
				model := "gemini-test"
				if provider == "antigravity-adapter" {
					model = "gemini-3-pro-preview"
				}
				req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"messages":[{"role":"user","content":"hello"}],"top_p":0.8}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{cliproxyexecutor.AntiTruncationMetadataKey: true}}
				var execute func(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
				var executeStream func(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
				if provider == "gemini" {
					e := NewGeminiExecutor(cfg)
					execute = e.Execute
					executeStream = e.ExecuteStream
				} else {
					e := NewAntigravityExecutor(cfg)
					execute = e.Execute
					executeStream = e.ExecuteStream
				}
				var output strings.Builder
				var text strings.Builder
				if stream {
					response, err := executeStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range response.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						output.Write(chunk.Payload)
						for _, line := range bytes.Split(chunk.Payload, []byte("\n")) {
							line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
							text.WriteString(gjson.GetBytes(line, "choices.0.delta.content").String())
						}
					}
				} else {
					response, err := execute(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					output.Write(response.Payload)
					text.WriteString(gjson.GetBytes(response.Payload, "choices.0.message.content").String())
				}
				if text.String() != "firstsecond" || strings.Contains(output.String(), "cpa_emit_answer") || len(requests) != 2 {
					t.Fatalf("calls=%d output=%s", len(requests), output.String())
				}
				prefix := ""
				if protocol == "antigravity" {
					prefix = "request."
				}
				for i, body := range requests {
					if gjson.GetBytes(body, prefix+"generationConfig.temperature").Float() != 0.42 || gjson.GetBytes(body, prefix+"generationConfig.topP").Exists() {
						t.Fatalf("payload rules lost on attempt %d: %s", i, body)
					}
					if bytes.Count(body, []byte("final-marker")) != 1 {
						t.Fatalf("payload rules applied more/less than once on attempt %d: %s", i, body)
					}
					if !bytes.Contains(body, []byte("cpa_emit_answer")) {
						t.Fatalf("tool missing: %s", body)
					}
				}
				if gjson.GetBytes(requests[1], prefix+"contents.#").Int() != 3 {
					t.Fatalf("continuation history malformed: %s", requests[1])
				}
			})
		}
	}
}
