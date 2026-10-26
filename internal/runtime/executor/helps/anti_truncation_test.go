package helps

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func antiConfig(attempts int) *config.Config {
	return &config.Config{SDKConfig: config.SDKConfig{Streaming: config.StreamingConfig{AntiTruncation: config.AntiTruncationConfig{Enabled: true, MaxAttempts: attempts}}}}
}

func TestAntiTruncationCloseCancelsUpstream(t *testing.T) {
	ended := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(ended)
	}))
	defer server.Close()
	body, a, err := PrepareAntiTruncation(antiConfig(3), "gemini-test", []byte(`{}`), "", true)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader(body))
	resp, err := a.Do(server.Client(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream was not canceled")
	}
}

func TestAntiTruncationPreservesHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429); _, _ = io.WriteString(w, "quota") }))
	defer server.Close()
	body, a, err := PrepareAntiTruncation(antiConfig(3), "gemini-test", []byte(`{}`), "", false)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader(body))
	resp, err := a.Do(server.Client(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != 429 || string(data) != "quota" {
		t.Fatal("HTTP failure changed")
	}
}
