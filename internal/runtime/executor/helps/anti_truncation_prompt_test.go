package helps

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntiTruncationPresetInjection(t *testing.T) {
	for _, tc := range []struct {
		name, messages, role string
		index                int
	}{
		{"anchor", `[{"role":"system","content":"base"},{"role":"user","content":"<format>one"},{"role":"user","content":"<format>two"}]`, "system", 1},
		{"last_user", `[{"role":"user","content":"hello"}]`, "system", 1},
		{"last_assistant", `[{"role":"user","content":"hello"},{"role":"assistant","content":"prefill"}]`, "user", 2},
		{"array_is_not_anchor", `[{"role":"user","content":[{"type":"text","text":"<format>"}]}]`, "system", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"messages":` + tc.messages + `,"tools":[{"type":"function","function":{"name":"lookup"}}]}`)
			req := coreexecutor.Request{Payload: body}
			opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: body, Metadata: map[string]any{coreexecutor.AntiTruncationMetadataKey: true}}
			prepared, preparedOpts, err := PrepareAntiTruncationSource(req, opts)
			if err != nil {
				t.Fatal(err)
			}
			name := gjson.GetBytes(prepared.Payload, "tools.1.function.name").String()
			if !regexp.MustCompile(`^emit_complete_response_[0-9a-f]{24}$`).MatchString(name) {
				t.Fatalf("invalid tool name %q", name)
			}
			if gjson.GetBytes(prepared.Payload, "tools.0.function.name").String() != "lookup" || gjson.GetBytes(prepared.Payload, "tool_choice").String() != "auto" {
				t.Fatal(string(prepared.Payload))
			}
			messages := gjson.GetBytes(prepared.Payload, "messages").Array()
			if messages[tc.index].Get("role").String() != tc.role || messages[tc.index].Get("content").String() != "Call the `"+name+"` function exactly once and put your complete final reply in its `content` argument. Do not write any of the final reply outside that call. Use any other available tools normally when they are needed." {
				t.Fatal(string(prepared.Payload))
			}
			if !bytes.Equal(prepared.Payload, preparedOpts.OriginalRequest) || opts.Metadata[antiTruncationPreparationKey] != nil || !bytes.Equal(req.Payload, body) {
				t.Fatal("original request/options mutated")
			}
			second, _, err := PrepareAntiTruncationSource(req, opts)
			if err != nil || gjson.GetBytes(second.Payload, "tools.1.function.name").String() == name {
				t.Fatal("request tool name reused")
			}
			again, _, err := PrepareAntiTruncationSource(prepared, preparedOpts)
			if err != nil || !bytes.Equal(again.Payload, prepared.Payload) {
				t.Fatal("adapter injected twice")
			}
			if gjson.GetBytes(prepared.Payload, "tools.1.function.description").String() != "Emit the complete final user-visible reply exactly once. Put the entire reply in content and write no reply text outside this call." || gjson.GetBytes(prepared.Payload, "tools.1.function.parameters.properties.content.description").String() != "The complete final reply shown to the user." {
				t.Fatal("preset tool schema changed")
			}
		})
	}
}

func TestAntiTruncationPresetBypass(t *testing.T) {
	for _, body := range []string{
		`{"messages":[]}`, `{}`,
		`{"messages":[{"role":"user","content":"hi"}],"tool_choice":"none"}`,
		`{"messages":[{"role":"user","content":"hi"}],"tool_choice":"required"}`,
		`{"messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"function","function":{"name":"lookup"}}}`,
	} {
		req, opts, err := PrepareAntiTruncationSource(coreexecutor.Request{Payload: []byte(body)}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{coreexecutor.AntiTruncationMetadataKey: true}})
		if err != nil || string(req.Payload) != body {
			t.Fatalf("bypass changed request: %s %v", req.Payload, err)
		}
		native := []byte(`{"contents":[]}`)
		out, state, err := PrepareAntiTruncationForRequest(antiConfig(3), "gemini-test", native, "", false, opts)
		if err != nil || state != nil || !bytes.Equal(out, native) {
			t.Fatal("native phase re-enabled bypassed injection")
		}
	}
	for _, mode := range []string{"NONE", "ANY"} {
		body := []byte(`{"toolConfig":{"functionCallingConfig":{"mode":"` + mode + `"}}}`)
		out, state, err := PrepareAntiTruncation(antiConfig(3), "gemini-test", body, "", false)
		if err != nil || state != nil || !bytes.Equal(out, body) {
			t.Fatal("native tool control overridden")
		}
	}
}

func TestAntiTruncationOnlyConsumesOwnTool(t *testing.T) {
	name := antiTruncationToolPrefix + strings.Repeat("a", 24)
	foreign := antiTruncationToolPrefix + strings.Repeat("b", 24)
	answer, err := readAntiAnswer(strings.NewReader(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"`+foreign+`","args":{"content":"client tool"}}}]},"finishReason":"STOP"}]}`), false, name)
	if err != nil || answer.synthetic || !answer.realTools {
		t.Fatalf("foreign tool consumed: %+v %v", answer, err)
	}
	_, a, err := PrepareAntiTruncation(antiConfig(3), "gemini-test", []byte(`{}`), "", false)
	if err != nil {
		t.Fatal(err)
	}
	next, err := a.continuation("partial")
	if err != nil || !strings.Contains(gjson.GetBytes(next, "contents.1.parts.0.text").String(), "`"+a.toolName+"`") || gjson.GetBytes(next, "tools.0.functionDeclarations.0.name").String() != a.toolName {
		t.Fatal("continuation did not retain the tool identity")
	}
}
