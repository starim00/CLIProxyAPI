package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestEmbeddingsCredentialPrefixes(t *testing.T) {
	for _, provider := range []string{"gemini", "vertex-key", "vertex-sa"} {
		for _, prefix := range []string{"", "tenant", "private/team"} {
			for _, model := range []string{"gemini-embedding-001", "gemini-embedding-2"} {
				t.Run(provider+"/"+prefix+"/"+model, func(t *testing.T) {
					calls := 0
					predict := provider != "gemini" && model == "gemini-embedding-001"
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/token" {
							_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
							return
						}
						calls++
						action := "embedContent"
						if predict {
							action = "predict"
						}
						if !strings.HasSuffix(r.URL.Path, "/"+model+":"+action) {
							t.Errorf("wrong upstream path: %s", r.URL.Path)
						}
						if provider == "vertex-sa" {
							if r.Header.Get("Authorization") != "Bearer local-token" {
								t.Error("missing bearer")
							}
						} else if r.Header.Get("x-goog-api-key") != "mock" {
							t.Error("missing API key")
						}
						body, _ := io.ReadAll(r.Body)
						if bytes.Count(body, []byte("final-marker")) != 1 || gjson.GetBytes(body, "tools").Exists() {
							t.Errorf("payload barrier or anti tool leak: %s", body)
						}
						textPath, dimPath := "content.parts.0.text", "embedContentConfig.outputDimensionality"
						if predict {
							textPath, dimPath = "instances.0.content", "parameters.outputDimensionality"
						}
						if gjson.GetBytes(body, textPath).String() != fmt.Sprintf("text%d", calls) || gjson.GetBytes(body, dimPath).Int() != 2 {
							t.Errorf("wrong input/dimensions: %s", body)
						}
						if predict {
							fmt.Fprintf(w, `{"predictions":[{"embeddings":{"values":[%d,0.5],"statistics":{"token_count":2,"truncated":false}}}]}`, calls)
						} else {
							fmt.Fprintf(w, `{"embedding":{"values":[%d,0.5]},"usageMetadata":{"totalTokenCount":2}}`, calls)
						}
					}))
					defer server.Close()
					cfg := &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: true}}
					cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "gemini-embedding-*", Protocol: "gemini"}}, Params: map[string]any{"marker.-1": "final-marker"}}}
					ctx := context.Background()
					kind := "vertex"
					if provider == "gemini" {
						kind = "gemini"
					}
					auth := &coreauth.Auth{ID: t.Name(), Provider: kind, Prefix: prefix, Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "mock", "base_url": server.URL}}
					if provider == "vertex-sa" {
						var sa map[string]any
						if err := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &sa); err != nil {
							t.Fatal(err)
						}
						auth.Attributes = nil
						auth.Metadata = map[string]any{"project_id": "proxy-test", "location": "global", "service_account": sa}
						target, _ := parseTestURL(t, server.URL)
						transport := http.DefaultTransport.(*http.Transport).Clone()
						transport.Proxy = nil
						t.Cleanup(transport.CloseIdleConnections)
						ctx = context.WithValue(ctx, "cliproxy.roundtripper", issue6258RedirectTransport{target: target, base: transport})
					}
					manager := coreauth.NewManager(nil, nil, nil)
					manager.SetConfig(cfg)
					manager.RegisterExecutor(NewGeminiExecutor(cfg))
					manager.RegisterExecutor(NewGeminiVertexExecutor(cfg))
					if _, err := manager.Register(ctx, auth); err != nil {
						t.Fatal(err)
					}
					id := model
					if prefix != "" {
						id = prefix + "/" + model
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, kind, []*registry.ModelInfo{{ID: id, MetadataModelID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
					payload := fmt.Sprintf(`{"model":%q,"input":["text1","text2"],"dimensions":2,"encoding_format":"base64"}`, id)
					response, _, failure := base.ExecuteWithAuthManager(ctx, "openai", id, []byte(payload), "embeddings")
					if failure != nil {
						t.Fatal(failure.Error)
					}
					if calls != 2 || gjson.GetBytes(response, "model").String() != id || gjson.GetBytes(response, "usage.total_tokens").Int() != 4 {
						t.Fatalf("wrong response: %s calls=%d", response, calls)
					}
					encoded := gjson.GetBytes(response, "data.0.embedding").String()
					decoded, err := base64.StdEncoding.DecodeString(encoded)
					if err != nil || !bytes.Equal(decoded, []byte{0, 0, 128, 63, 0, 0, 0, 63}) {
						t.Fatalf("bad base64 float32: %s", encoded)
					}
					if prefix != "" {
						_, _, failure = base.ExecuteWithAuthManager(ctx, "openai", model, []byte(payload), "embeddings")
						if failure == nil {
							t.Fatal("unprefixed request used prefixed credential")
						}
					}
					if provider == "gemini" && prefix == "" {
						calls = 0
						router := gin.New()
						handler := openai.NewOpenAIAPIHandler(base)
						router.POST("/v1/embeddings", handler.Embeddings)
						w := httptest.NewRecorder()
						router.ServeHTTP(w, httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(payload)))
						if w.Code != 200 || calls != 2 {
							t.Fatalf("HTTP handler: %d %s", w.Code, w.Body.String())
						}
					}
				})
			}
		}
	}
}
