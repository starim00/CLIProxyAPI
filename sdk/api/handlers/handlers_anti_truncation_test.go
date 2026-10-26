package handlers

import (
	"context"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

type antiTruncationCaptureExecutor struct {
	failOnceStreamExecutor
	req  coreexecutor.Request
	opts coreexecutor.Options
}

func TestAntiTruncationV2Eligibility(t *testing.T) {
	r := registry.GetGlobalRegistry()
	const client = "anti-v2-eligibility"
	r.RegisterClient(client, "gemini", []*registry.ModelInfo{{ID: "picture-alias", MetadataModelID: "gemini-image"}, {ID: "gemini-chat", MetadataModelID: "gemini-test"}})
	t.Cleanup(func() { r.UnregisterClient(client) })
	h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{AntiTruncation: sdkconfig.AntiTruncationConfig{Enabled: true}}}, nil)
	listed := h.WithAntiTruncationModels([]map[string]any{{"id": "picture-alias"}})
	if len(listed) != 1 {
		t.Fatalf("non-text v2 exposed or v1 changed: %v", listed)
	}
	for _, tc := range []struct {
		model, protocol string
		image           bool
	}{
		{"抗截断/picture-alias", "openai", false},
		{"抗截断/gemini-chat", "interactions", false},
		{"抗截断/gemini-chat", "openai", true},
		{"抗截断-v2/gemini-chat", "openai", false},
	} {
		if _, _, _, err := h.resolveAntiTruncationModel(tc.model, nil, tc.protocol, tc.image); err == nil {
			t.Fatalf("accepted unsupported %s", tc.model)
		}
	}
	name, _, selected, err := h.resolveAntiTruncationModel("抗截断/gemini-chat(high)", nil, "openai", false)
	if err != nil || !selected || name != "gemini-chat(high)" {
		t.Fatalf("thinking suffix lost: %s %v", name, err)
	}
}

func (e *antiTruncationCaptureExecutor) Identifier() string { return "gemini" }
func (e *antiTruncationCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.req, e.opts = req, opts
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}
func (e *antiTruncationCaptureExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}
func (e *antiTruncationCaptureExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	resp, _ := e.Execute(ctx, auth, req, opts)
	ch := make(chan coreexecutor.StreamChunk, 1)
	ch <- coreexecutor.StreamChunk{Payload: resp.Payload}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func TestAntiTruncationModelSelection(t *testing.T) {
	const model = "api/Anti Test Alias"
	r := registry.GetGlobalRegistry()
	auth := &coreauth.Auth{ID: "anti-model-selection", Provider: "gemini", Prefix: "api", Status: coreauth.StatusActive}
	r.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model, MetadataModelID: "gemini-test", Type: "gemini"}})
	t.Cleanup(func() { r.UnregisterClient(auth.ID) })
	mgr := coreauth.NewManager(nil, nil, nil)
	e := &antiTruncationCaptureExecutor{}
	mgr.RegisterExecutor(e)
	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	cfg := &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{AntiTruncation: sdkconfig.AntiTruncationConfig{Enabled: true}}}
	h := NewBaseAPIHandlers(cfg, mgr)
	for _, prefix := range []string{"", "抗截断/"} {
		selected := prefix != ""
		for _, mode := range []string{"nonstream", "stream", "count"} {
			t.Run(fmt.Sprintf("prefix=%s/%s", prefix, mode), func(t *testing.T) {
				id := model
				if selected {
					id = prefix + id
				}
				body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, id))
				ctx := context.Background()
				switch mode {
				case "nonstream":
					if _, _, err := h.ExecuteWithAuthManager(ctx, "openai", id, body, ""); err != nil {
						t.Fatal(err.Error)
					}
				case "count":
					if _, _, err := h.ExecuteCountWithAuthManager(ctx, "openai", id, body, ""); err != nil {
						t.Fatal(err.Error)
					}
				case "stream":
					ch, _, errs := h.ExecuteStreamWithAuthManager(ctx, "openai", id, body, "")
					if ch == nil {
						for err := range errs {
							if err != nil {
								t.Fatal(err.Error)
							}
						}
						t.Fatal("stream missing")
					}
					for range ch {
					}
					for err := range errs {
						if err != nil {
							t.Fatal(err.Error)
						}
					}
				}
				flag, _ := e.opts.Metadata[coreexecutor.AntiTruncationMetadataKey].(bool)
				if flag != (selected && mode != "count") {
					t.Fatalf("flag=%v", flag)
				}
				if e.opts.Metadata[coreexecutor.RequestedModelMetadataKey] != model || gjson.GetBytes(e.req.Payload, "model").String() != model {
					t.Fatalf("virtual prefix leaked into routing: %+v %+v", e.req, e.opts.Metadata)
				}
			})
		}
	}
	models := []map[string]any{{"id": model, "display_name": "Alias"}, {"id": "gpt-no-variant"}}
	listed := h.WithAntiTruncationModels(models)
	if len(listed) != 3 || listed[2]["id"] != "抗截断/"+model || models[0]["id"] != model {
		t.Fatalf("bad model list: %v", listed)
	}
	native := h.WithAntiTruncationModels([]map[string]any{{"name": "models/" + model}})
	if len(native) != 2 || native[1]["name"] != "models/抗截断/"+model {
		t.Fatalf("bad native list: %v", native)
	}
	if len(h.WithAntiTruncationModels(listed)) != 3 {
		t.Fatal("duplicate variants")
	}
	for _, protocol := range []string{"interactions", "openai"} {
		id := "抗截断/" + model
		if protocol == "openai" {
			id = "抗截断/unknown"
		}
		if _, _, _, err := h.resolveAntiTruncationModel(id, nil, protocol, false); err == nil {
			t.Fatal("unsupported selection accepted")
		}
	}
	cfg.Streaming.AntiTruncation.Enabled = false
	if len(h.WithAntiTruncationModels(models)) != 2 {
		t.Fatal("disabled variants exposed")
	}
	if _, _, _, err := h.resolveAntiTruncationModel("抗截断/"+model, nil, "openai", false); err == nil {
		t.Fatal("disabled selection accepted")
	}
}
