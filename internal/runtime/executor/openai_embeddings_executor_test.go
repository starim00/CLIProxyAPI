package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestNativeOpenAIEmbeddingsRouting(t *testing.T) {
	for _, provider := range []string{"arbitrary-provider", "codex"} {
		for _, prefix := range []string{"", "tenant", "private/team"} {
			for _, input := range []string{`"hello"`, `["hello","world"]`, `[15339,1917]`, `[[15339],[1917]]`} {
				t.Run(provider+"/"+prefix+"/"+input, func(t *testing.T) {
					calls := 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer native-key" {
							t.Errorf("wrong endpoint or credential: %s", r.URL.Path)
						}
						body, _ := io.ReadAll(r.Body)
						if ojson.GetBytes(body, "model").String() != "text-embedding-3-small" || ojson.GetBytes(body, "input").Raw != input {
							t.Errorf("changed model/input: %s", body)
						}
						if ojson.GetBytes(body, "dimensions").Int() != 2 || ojson.GetBytes(body, "user").String() != "test-user" || ojson.GetBytes(body, "encoding_format").String() != "base64" {
							t.Errorf("lost parameters: %s", body)
						}
						if len(ojson.GetBytes(body, "final_marker").Array()) != 1 || ojson.GetBytes(body, "tools").Exists() || ojson.GetBytes(body, "messages").Exists() {
							t.Errorf("payload finalizer or chat injection: %s", body)
						}
						_, _ = io.WriteString(w, `{"object":"list","model":"text-embedding-3-small","data":[{"object":"embedding","index":0,"embedding":"AACAPwAAAD8="}],"usage":{"prompt_tokens":2,"total_tokens":2},"extension":"preserved"}`)
					}))
					defer server.Close()
					cfg := &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: true}}
					cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "text-embedding-*", Protocol: "openai"}}, Params: map[string]any{"final_marker.-1": "once"}}}
					manager := coreauth.NewManager(nil, nil, nil)
					manager.SetConfig(cfg)
					if provider == "codex" {
						manager.RegisterExecutor(NewCodexAutoExecutor(cfg))
					} else {
						manager.RegisterExecutor(NewOpenAICompatExecutor(provider, cfg))
					}
					id := "text-embedding-3-small"
					if prefix != "" {
						id = prefix + "/" + id
					}
					auth := &coreauth.Auth{ID: t.Name(), Provider: provider, Prefix: prefix, Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "native-key", "base_url": server.URL + "/v1/"}}
					if _, err := manager.Register(context.Background(), auth); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: id, MetadataModelID: "text-embedding-3-small"}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					if provider == "codex" {
						oauth := &coreauth.Auth{ID: t.Name() + "-oauth", Provider: provider, Prefix: prefix, Status: coreauth.StatusActive, Attributes: map[string]string{"priority": "100", "base_url": server.URL + "/v1"}, Metadata: map[string]any{"access_token": "oauth-only"}}
						if _, err := manager.Register(context.Background(), oauth); err != nil {
							t.Fatal(err)
						}
						registry.GetGlobalRegistry().RegisterClient(oauth.ID, provider, []*registry.ModelInfo{{ID: id}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(oauth.ID) })
					}
					handler := openai.NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
					router := gin.New()
					router.POST("/v1/embeddings", handler.Embeddings)
					payload := fmt.Sprintf(`{"model":%q,"input":%s,"dimensions":2,"encoding_format":"base64","user":"test-user"}`, id, input)
					w := httptest.NewRecorder()
					router.ServeHTTP(w, httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(payload)))
					if w.Code != 200 || calls != 1 {
						t.Fatalf("status=%d calls=%d body=%s", w.Code, calls, w.Body.String())
					}
					if ojson.Get(w.Body.String(), "data.0.embedding").String() != "AACAPwAAAD8=" || ojson.Get(w.Body.String(), "extension").String() != "preserved" {
						t.Fatalf("response changed: %s", w.Body.String())
					}
				})
			}
		}
	}
}

func TestNativeEmbeddingErrors(t *testing.T) {
	for _, code := range []int{401, 429, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(code)
				_, _ = io.WriteString(w, `{"error":{"message":"upstream error"}}`)
			}))
			defer server.Close()
			e := NewOpenAICompatExecutor("custom", &config.Config{})
			auth := &coreauth.Auth{Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "test"}}
			req := coreexecutor.Request{Model: "text-embedding-3-small", Payload: []byte(`{"model":"text-embedding-3-small","input":"test"}`)}
			_, err := e.Execute(context.Background(), auth, req, coreexecutor.Options{Alt: "embeddings"})
			if err == nil || err.(interface{ StatusCode() int }).StatusCode() != code {
				t.Fatalf("status lost: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewOpenAICompatExecutor("custom", &config.Config{}).Execute(ctx, &coreauth.Auth{Attributes: map[string]string{"base_url": "http://127.0.0.1:1/v1", "api_key": "test"}}, coreexecutor.Request{Model: "m", Payload: []byte(`{"model":"m","input":"test"}`)}, coreexecutor.Options{Alt: "embeddings"})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("cancellation lost: %v", err)
	}
}
