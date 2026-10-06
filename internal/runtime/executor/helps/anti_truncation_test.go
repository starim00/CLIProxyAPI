package helps

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
)

func antiConfig(attempts int) *config.Config {
	return &config.Config{SDKConfig: config.SDKConfig{Streaming: config.StreamingConfig{AntiTruncation: config.AntiTruncationConfig{Enabled: true, MaxAttempts: attempts}}}}
}

func TestAntiTruncationPrepare(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"tools":[{"functionDeclarations":[{"name":"lookup"}]}],"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["lookup"]}},"systemInstruction":{"parts":[{"text":"existing"}]}}`)
	original := bytes.Clone(body)
	out, anti, err := PrepareAntiTruncation(antiConfig(3), "gemini-test", body, "", true)
	if err != nil || anti == nil {
		t.Fatalf("prepare: %v", err)
	}
	if !bytes.Equal(body, original) {
		t.Fatal("input mutated")
	}
	if gjson.GetBytes(out, "tools.#").Int() != 2 || gjson.GetBytes(out, "systemInstruction.parts.0.text").String() != "existing" {
		t.Fatal(string(out))
	}
	if gjson.GetBytes(out, "toolConfig.functionCallingConfig.allowedFunctionNames.1").String() != antiTruncationTool {
		t.Fatal("synthetic tool missing from allowlist")
	}
	for _, cfg := range []*config.Config{nil, {}} {
		got, state, err := PrepareAntiTruncation(cfg, "gemini-test", body, "", true)
		if err != nil || state != nil || !bytes.Equal(got, body) {
			t.Fatal("disabled request changed")
		}
	}
	got, state, err := PrepareAntiTruncation(antiConfig(3), "claude-test", body, "", true)
	if err != nil || state != nil || !bytes.Equal(got, body) {
		t.Fatal("unmatched request changed")
	}
	if _, _, err := PrepareAntiTruncation(antiConfig(3), "gemini-test", out, "", true); err == nil {
		t.Fatal("tool collision accepted")
	}
	if _, _, err := PrepareAntiTruncation(antiConfig(3), "gemini-test", []byte(`{"generationConfig":{"candidateCount":2}}`), "", true); err == nil {
		t.Fatal("multiple candidates accepted")
	}
}

func TestAntiTruncationContinuation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, wrapped := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/wrapped=%v", stream, wrapped), func(t *testing.T) {
				var requests [][]byte
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					b, _ := io.ReadAll(r.Body)
					requests = append(requests, b)
					content, finish := "first", "MAX_TOKENS"
					if len(requests) == 2 {
						content, finish = "second", "STOP"
					}
					data := fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[{"text":"discard duplicate"},{"functionCall":{"name":"cpa_emit_answer","args":{"content":%q}}}]},"finishReason":%q}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5}}`, content, finish)
					if wrapped {
						data = `{"traceId":"retained","response":` + data + `}`
					}
					if stream {
						fmt.Fprintf(w, "data: %s\n\n", data)
					} else {
						_, _ = io.WriteString(w, data)
					}
				}))
				defer server.Close()
				payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"question"}]}]}`)
				root := ""
				if wrapped {
					payload = append(append([]byte(`{"request":`), payload...), '}')
					root = "request"
				}
				prepared, a, err := PrepareAntiTruncation(antiConfig(3), "gemini-test", payload, root, stream)
				if err != nil {
					t.Fatal(err)
				}
				request, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader(prepared))
				response, err := a.Do(server.Client(), request, func(b []byte) (*http.Request, error) { return RebuildAntiTruncationRequest(request, b), nil })
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if len(requests) != 2 || bytes.Contains(data, []byte("cpa_emit_answer")) || bytes.Contains(data, []byte("discard duplicate")) {
					t.Fatalf("requests=%d data=%s", len(requests), data)
				}
				answer, err := readAntiAnswer(bytes.NewReader(data), stream)
				if err != nil {
					t.Fatal(err)
				}
				_, text := answer.cleanParts()
				if text != "firstsecond" || answer.finish != "STOP" || answer.usage["totalTokenCount"] != float64(10) {
					t.Fatalf("text=%s finish=%s usage=%v", text, answer.finish, answer.usage)
				}
				prefix := ""
				if wrapped {
					prefix = "request."
				}
				if gjson.GetBytes(requests[1], prefix+"contents.#").Int() != 3 || gjson.GetBytes(requests[1], prefix+"contents.1.parts.0.text").String() != "first" {
					t.Fatalf("bad continuation: %s", requests[1])
				}
				if wrapped && !bytes.Contains(data, []byte("retained")) {
					t.Fatal("envelope metadata lost")
				}
			})
		}
	}
}

func TestAntiTruncationTerminalAndFailures(t *testing.T) {
	cases := []struct {
		name, response string
		attempts       int
		wantError      bool
	}{
		{"natural_stop", `{"candidates":[{"content":{"parts":[{"text":"natural"}]},"finishReason":"STOP"}]}`, 1, false},
		{"synthetic_complete", `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"cpa_emit_answer","args":{"content":"answer"}}}]}}]}`, 1, false},
		{"real_tool", `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"id":1}},"thoughtSignature":"keep"}]}}]}`, 1, false},
		{"safety", `{"candidates":[{"finishReason":"SAFETY","safetyRatings":[{"blocked":true}]}]}`, 1, false},
		{"prompt_block", `{"promptFeedback":{"blockReason":"SAFETY"}}`, 1, false},
		{"empty", `{}`, 3, true},
		{"missing_finish", `{"candidates":[{"content":{"parts":[{"text":"part"}]}}]}`, 3, true},
		{"max_tokens", `{"candidates":[{"content":{"parts":[{"text":"part"}]},"finishReason":"MAX_TOKENS"}]}`, 3, true},
		{"error_event", `{"error":{"code":503}}`, 1, true},
		{"bad_json", `{"candidates":`, 1, true},
		{"invalid_synthetic", `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"cpa_emit_answer","args":{"content":4}}}]}}]}`, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				fmt.Fprintf(w, "data: %s\n\n", tc.response)
			}))
			defer server.Close()
			body, a, err := PrepareAntiTruncation(antiConfig(3), "gemini-test", []byte(`{"contents":[]}`), "", true)
			if err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader(body))
			resp, err := a.Do(server.Client(), req, func(b []byte) (*http.Request, error) { return RebuildAntiTruncationRequest(req, b), nil })
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if (err != nil) != tc.wantError || int(count.Load()) != tc.attempts {
				t.Fatalf("err=%v calls=%d data=%s", err, count.Load(), data)
			}
			if tc.name == "real_tool" && (!bytes.Contains(data, []byte("lookup")) || !bytes.Contains(data, []byte("keep"))) {
				t.Fatal("real tool changed")
			}
		})
	}
}

func TestAntiTruncationCloseCancelsUpstream(t *testing.T) {
	started, ended := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
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
	<-started
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
	body, a, _ := PrepareAntiTruncation(antiConfig(3), "gemini-test", []byte(`{}`), "", true)
	req, _ := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader(body))
	resp, err := a.Do(server.Client(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != 429 || string(data) != "quota" {
		t.Fatal("initial error changed")
	}
}

func TestAntiTruncationSSEMultilineAndLimit(t *testing.T) {
	response := "data: {\n" + "data: \"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n"
	answer, err := readAntiAnswer(strings.NewReader(response), true)
	if err != nil || answer.finish != "STOP" {
		t.Fatalf("multiline event: %v", err)
	}
	_, err = readAntiAnswer(strings.NewReader(strings.Repeat("x", antiTruncationLimit+1)), false)
	if err == nil {
		t.Fatal("oversize response accepted")
	}
}

func TestAntiTruncationInterruptedRead(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			// A complete event followed by a transport EOF must preserve its text.
			w.Header().Set("Content-Length", "4096")
			_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"first\"}]}}]}\n\n")
			return
		}
		body, _ := io.ReadAll(r.Body)
		if gjson.GetBytes(body, "contents.1.parts.0.text").String() != "first" {
			t.Errorf("lost salvaged text: %s", body)
		}
		_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"second\"}]},\"finishReason\":\"STOP\"}]}\n\n")
	}))
	defer server.Close()
	body, a, _ := PrepareAntiTruncation(antiConfig(3), "gemini-test", []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`), "", true)
	req, _ := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader(body))
	resp, err := a.Do(server.Client(), req, func(b []byte) (*http.Request, error) { return RebuildAntiTruncationRequest(req, b), nil })
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	answer, err := readAntiAnswer(bytes.NewReader(data), true)
	_, text := answer.cleanParts()
	if err != nil || text != "firstsecond" || calls.Load() != 2 {
		t.Fatalf("calls=%d text=%s err=%v", calls.Load(), text, err)
	}
}
