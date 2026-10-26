package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestAntiTruncationArbitraryCredentialPrefixes(t *testing.T) {
	for _, prefix := range []string{"vertex", "private/team", "image-tenant"} {
		for _, mode := range []string{"api_key", "service_account"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%v", prefix, mode, stream), func(t *testing.T) {
					calls := 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/token" {
							_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
							return
						}
						body, _ := io.ReadAll(r.Body)
						calls++
						if !strings.HasSuffix(r.URL.Path, "/gemini-test:generateContent") && !strings.HasSuffix(r.URL.Path, "/gemini-test:streamGenerateContent") {
							t.Errorf("prefix leaked to upstream URL: %s", r.URL.Path)
						}
						name := gjson.GetBytes(body, "tools.0.functionDeclarations.0.name").String()
						if !strings.HasPrefix(name, "agw_emit_") || bytes.Count(body, []byte("final-marker")) != 1 {
							t.Errorf("missing tool or finalizer repeated: %s", body)
						}
						data := `{"candidates":[{"finishReason":"STOP"}]}`
						if calls > 1 {
							data = fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":%q,"args":{"content":"PREFIX_OK"}}}]},"finishReason":"STOP"}]}`, name)
						}
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							fmt.Fprintf(w, "data: %s\n\n", data)
						} else {
							_, _ = io.WriteString(w, data)
						}
					}))
					defer server.Close()
					cfg := &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: true, Streaming: config.StreamingConfig{AntiTruncation: config.AntiTruncationConfig{Enabled: true, MaxAttempts: 2}}}}
					cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "gemini-*", Protocol: "gemini"}}, Params: map[string]any{"systemInstruction.parts.-1": map[string]string{"text": "final-marker"}}}}
					ctx := context.Background()
					auth := &coreauth.Auth{ID: t.Name(), Provider: "vertex", Prefix: prefix, Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "mock", "base_url": server.URL}}
					if mode == "service_account" {
						auth.Attributes = nil
						var sa map[string]any
						if err := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &sa); err != nil {
							t.Fatal(err)
						}
						auth.Metadata = map[string]any{"project_id": "proxy-test", "location": "global", "service_account": sa}
						target, err := parseTestURL(t, server.URL)
						if err != nil {
							t.Fatal(err)
						}
						transport := http.DefaultTransport.(*http.Transport).Clone()
						transport.Proxy = nil
						t.Cleanup(transport.CloseIdleConnections)
						ctx = context.WithValue(ctx, "cliproxy.roundtripper", issue6258RedirectTransport{target: target, base: transport})
					}
					manager := coreauth.NewManager(nil, nil, nil)
					manager.SetConfig(cfg)
					manager.RegisterExecutor(NewGeminiVertexExecutor(cfg))
					if _, err := manager.Register(ctx, auth); err != nil {
						t.Fatal(err)
					}
					model := prefix + "/gemini-test"
					registry.GetGlobalRegistry().RegisterClient(auth.ID, "vertex", []*registry.ModelInfo{{ID: model, MetadataModelID: "gemini-test"}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					h := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
					listed := h.WithAntiTruncationModels([]map[string]any{{"id": model}})
					if len(listed) != 2 || listed[1]["id"] != "抗截断/"+model {
						t.Fatalf("missing arbitrary prefix variant: %v", listed)
					}
					id := "抗截断/" + model
					body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":%v}`, id, stream))
					var output []byte
					if stream {
						chunks, _, errs := h.ExecuteStreamWithAuthManager(ctx, "openai", id, body, "")
						for b := range chunks {
							output = append(output, b...)
						}
						for err := range errs {
							if err != nil {
								t.Fatal(err.Error)
							}
						}
					} else {
						b, _, err := h.ExecuteWithAuthManager(ctx, "openai", id, body, "")
						if err != nil {
							t.Fatal(err.Error)
						}
						output = b
					}
					if calls != 2 || !bytes.Contains(output, []byte("PREFIX_OK")) || bytes.Contains(output, []byte("agw_emit_")) {
						t.Fatalf("calls=%d response=%s", calls, output)
					}
				})
			}
		}
	}
}
