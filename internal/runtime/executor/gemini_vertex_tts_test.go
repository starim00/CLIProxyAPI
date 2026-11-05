package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestVertexNativeTTSAudioRoundTrip(t *testing.T) {
	const payload = `{"contents":[{"role":"user","parts":[{"text":"Hello","speechMetadata":{"style":"cheerful"}}]}],"generationConfig":{"responseModalities":["AUDIO"],"speechConfig":{"voiceConfig":{"voice":"Kore"}}}}`
	const response = `{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"audio/wav","data":"UklGRg=="}}]},"finishReason":"STOP"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		for path, want := range map[string]string{
			"generationConfig.responseModalities.0":           "AUDIO",
			"generationConfig.speechConfig.voiceConfig.voice": "Kore",
			"contents.0.parts.0.speechMetadata.style":         "cheerful",
		} {
			if got := ojson.GetBytes(body, path).String(); got != want {
				t.Errorf("%s = %q, want %q", path, got, want)
			}
		}
		if !strings.Contains(r.URL.Path, "gemini-3.8-flash-tts:") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+response+"\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, response)
		}
	}))
	defer server.Close()
	exec := NewGeminiVertexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "vertex-tts", Provider: "vertex", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
	req := cliproxyexecutor.Request{Model: "gemini-3.8-flash-tts", Payload: []byte(payload)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, OriginalRequest: req.Payload}
	result, err := exec.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	if ojson.GetBytes(result.Payload, "candidates.0.content.parts.0.inlineData.data").String() != "UklGRg==" {
		t.Fatalf("audio lost: %s", result.Payload)
	}
	stream, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		out.Write(chunk.Payload)
	}
	if !strings.Contains(out.String(), "UklGRg==") || !strings.Contains(out.String(), "audio/wav") {
		t.Fatalf("stream audio lost: %s", out.String())
	}
}
