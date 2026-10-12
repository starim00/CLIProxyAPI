package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntiTruncationV2Injection(t *testing.T) {
	for _, tc := range []struct{ name, messages, choice, role, wantChoice string }{
		{"user", `[{"role":"user","content":"<format>hi"}]`, `"none"`, "system", `"auto"`},
		{"prefill_rules", `[{"role":"assistant","content":"prefill"},{"role":"system","content":"rules"},{"role":"developer","content":"more"}]`, `"required"`, "user", `"required"`},
		{"system_only", `[{"role":"system","content":"rules"}]`, `"auto"`, "user", `"auto"`},
		{"empty", `[]`, `"auto"`, "user", `"auto"`},
		{"forced_real", `[{"role":"user","content":"hi"}]`, `{"type":"function","function":{"name":"lookup"}}`, "system", `{"type":"function","function":{"name":"lookup"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"messages":` + tc.messages + `,"tool_choice":` + tc.choice + `,"tools":[{"type":"function","function":{"name":"lookup"}}]}`)
			opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: body, Metadata: map[string]any{coreexecutor.AntiTruncationMetadataKey: true, coreexecutor.AntiTruncationV2MetadataKey: true}}
			req, next, err := PrepareAntiTruncationSource(coreexecutor.Request{Payload: body}, opts)
			if err != nil {
				t.Fatal(err)
			}
			name := gjson.GetBytes(req.Payload, "tools.1.function.name").String()
			if !regexp.MustCompile(`^agw_emit_[0-9a-f]{24}$`).MatchString(name) {
				t.Fatal(name)
			}
			messages := gjson.GetBytes(req.Payload, "messages").Array()
			last := messages[len(messages)-1]
			if last.Get("role").String() != tc.role || last.Get("content").String() != antiTruncationV2Instruction(name) {
				t.Fatal(string(req.Payload))
			}
			if gjson.GetBytes(req.Payload, "tool_choice").Raw != tc.wantChoice || gjson.GetBytes(req.Payload, "tools.0.function.name").String() != "lookup" {
				t.Fatal(string(req.Payload))
			}
			if !bytes.Equal(req.Payload, next.OriginalRequest) || opts.Metadata[antiTruncationPreparationKey] != nil {
				t.Fatal("request state mutated")
			}
			if gjson.GetBytes(req.Payload, "tools.1.function.description").String() != "Use this transport tool exactly once for the final user-visible answer. Put the complete answer in content. Never wrap genuine tool calls in it." {
				t.Fatal("tool description differs")
			}
		})
	}
}

func TestAntiTruncationV2Repair(t *testing.T) {
	for _, s := range []string{`{"content":"你好😀"}`, "```json\n{\"content\":\"你好😀\"}\n```", `{"content":"你好😀`, `{"content":"你好😀",}`} {
		text, err := repairAntiTruncationV2Arguments(s)
		if err != nil || text != "你好😀" {
			t.Fatalf("%q => %q %v", s, text, err)
		}
	}
	for _, s := range []string{`{"content":null}`, `{"content":4}`, `{"unrelated":"text"}`, `{"content":"broken\`, strings.Repeat("x", antiTruncationLimit+1)} {
		if _, err := repairAntiTruncationV2Arguments(s); err == nil {
			t.Fatal("invalid schema accepted")
		}
	}
}

func TestAntiTruncationV2Recovery(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name, first string
			calls       int
			wantError   bool
		}{
			{"empty_then_answer", `{}`, 2, false},
			{"empty_stop", `{"candidates":[{"finishReason":"STOP"}]}`, 2, false},
			{"length_no_continuation", `{"candidates":[{"content":{"parts":[{"text":"partial"}]},"finishReason":"MAX_TOKENS"}]}`, 1, false},
			{"real_tool", `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"id":1}},"thoughtSignature":"keep"}]},"finishReason":"STOP"}]}`, 1, false},
			{"blocked", `{"promptFeedback":{"blockReason":"SAFETY"}}`, 1, false},
			{"malformed", `{`, 1, true},
		} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, stream), func(t *testing.T) {
				var requests [][]byte
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					requests = append(requests, body)
					data := tc.first
					if len(requests) > 1 {
						name := gjson.GetBytes(body, "tools.0.functionDeclarations.0.name").String()
						data = fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"text":"discard"},{"functionCall":{"name":%q,"args":{"content":"answer"}}}]},"finishReason":"STOP"}]}`, name)
					}
					if stream {
						fmt.Fprintf(w, "data: %s\n\n", data)
					} else {
						_, _ = io.WriteString(w, data)
					}
				}))
				defer server.Close()
				body, a, err := prepareAntiTruncationV2(antiConfig(3), "gemini-test", []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`), "", stream, "")
				if err != nil {
					t.Fatal(err)
				}
				req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader(body))
				resp, err := a.Do(server.Client(), req, func(next []byte) (*http.Request, error) { return RebuildAntiTruncationRequest(req, next), nil })
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if (err != nil) != tc.wantError || len(requests) != tc.calls {
					t.Fatalf("calls=%d err=%v data=%s", len(requests), err, data)
				}
				if tc.calls == 2 && (!bytes.Equal(requests[0], requests[1]) || !bytes.Contains(data, []byte("answer")) || bytes.Contains(data, []byte("discard")) || bytes.Contains(data, []byte("agw_emit_"))) {
					t.Fatalf("invalid retry/normalization: %s", data)
				}
				if tc.name == "real_tool" && (!bytes.Contains(data, []byte("lookup")) || !bytes.Contains(data, []byte("keep"))) {
					t.Fatal("real tool lost")
				}
			})
		}
	}
}

func TestAntiTruncationV2FlushBeforeEOF(t *testing.T) {
	reader, writer := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := readAntiAnswerV2(reader, true, "agw_emit_test", outputWriter)
		_ = outputWriter.CloseWithError(err)
		done <- err
	}()
	go func() {
		data := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"functionCall": map[string]any{"name": "agw_emit_test", "args": map[string]string{"content": "早😀"}}}}}}}}
		b, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(writer, "data: %s\n\n", b)
	}()
	buf := make([]byte, 4096)
	n, err := outputReader.Read(buf)
	if err != nil || !bytes.Contains(buf[:n], []byte("早😀")) {
		t.Fatalf("first event=%s err=%v", buf[:n], err)
	}
	// EOF is deliberately withheld until the transformed event is received.
	_ = writer.Close()
	_, _ = io.ReadAll(outputReader)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	_ = outputReader.Close()
}
