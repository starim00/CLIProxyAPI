package helps

// Gateway-compatible injection is adapted from Xeltra233/Antigravity-gateway
// e83fc1a505e501d3ac6a13c12816df1ceb55b86b (MIT); see THIRD_PARTY_NOTICES.md.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const antiTruncationV2ToolPrefix = "agw_emit_"

// AntiTruncationV2TextModel excludes non-conversational models from v2 aliases.
func AntiTruncationV2TextModel(model string) bool {
	model = strings.ToLower(model)
	if strings.TrimSpace(model) == "" {
		return false
	}
	for _, term := range []string{"image", "vision-preview", "audio", "speech", "tts", "stt", "embed", "whisper", "video", "veo", "flux", "dall-e", "sdxl", "diffusion", "moderation"} {
		if strings.Contains(model, term) {
			return false
		}
	}
	return true
}

func antiTruncationV2Instruction(name string) string {
	return fmt.Sprintf("Always use tool `%s` to output your final reply in its `content` argument. Do not output anything outside this tool call.", name)
}

func antiTruncationV2Declaration(name string) map[string]any {
	return map[string]any{
		"name":        name,
		"description": "Use this transport tool exactly once for the final user-visible answer. Put the complete answer in content. Never wrap genuine tool calls in it.",
		"parameters":  map[string]any{"type": "object", "properties": map[string]any{"content": map[string]string{"type": "string"}}, "required": []string{"content"}},
	}
}

func prepareAntiTruncationV2Source(req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Request, coreexecutor.Options, error) {
	bodies := [][]byte{req.Payload}
	if len(opts.OriginalRequest) > 0 {
		bodies = append(bodies, opts.OriginalRequest)
	}
	existing := map[string]bool{}
	for _, body := range bodies {
		if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
			return req, opts, errors.New("anti-truncation-v2: invalid request JSON")
		}
		if tools := gjson.GetBytes(body, "tools"); tools.IsArray() {
			for _, tool := range tools.Array() {
				existing[tool.Get("function.name").String()] = true
			}
		}
	}
	name, err := randomAntiTruncationTool(existing, antiTruncationV2ToolPrefix)
	if err != nil {
		return req, opts, err
	}
	req.Payload, err = injectAntiTruncationV2Messages(req.Payload, name)
	if err != nil {
		return req, opts, err
	}
	if len(opts.OriginalRequest) > 0 {
		opts.OriginalRequest, err = injectAntiTruncationV2Messages(opts.OriginalRequest, name)
		if err != nil {
			return req, opts, err
		}
	}
	opts.Metadata = maps.Clone(opts.Metadata)
	opts.Metadata[antiTruncationPreparationKey] = antiTruncationPreparation{toolName: name}
	return req, opts, nil
}

func injectAntiTruncationV2Messages(body []byte, name string) ([]byte, error) {
	messages := []json.RawMessage{}
	lastRole := ""
	if original := gjson.GetBytes(body, "messages"); original.IsArray() {
		for _, m := range original.Array() {
			messages = append(messages, json.RawMessage(m.Raw))
			role := m.Get("role").String()
			if role != "system" && role != "developer" {
				lastRole = role
			}
		}
	}
	role := "system"
	if lastRole == "" || lastRole == "assistant" || lastRole == "model" {
		role = "user"
	}
	control, err := json.Marshal(map[string]string{"role": role, "content": antiTruncationV2Instruction(name)})
	if err != nil {
		return nil, err
	}
	out, err := sjson.SetBytes(body, "messages", append(messages, control))
	if err != nil {
		return nil, err
	}
	tools := []json.RawMessage{}
	if original := gjson.GetBytes(body, "tools"); original.IsArray() {
		for _, tool := range original.Array() {
			tools = append(tools, json.RawMessage(tool.Raw))
		}
	}
	tool, err := json.Marshal(map[string]any{"type": "function", "function": antiTruncationV2Declaration(name)})
	if err != nil {
		return nil, err
	}
	out, err = sjson.SetBytes(out, "tools", append(tools, tool))
	if err != nil {
		return nil, err
	}
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.Exists() || choice.String() == "none" {
		return sjson.SetBytes(out, "tool_choice", "auto")
	}
	return out, nil
}

func prepareAntiTruncationV2(cfg *config.Config, model string, body []byte, root string, stream bool, name string) ([]byte, *AntiTruncation, error) {
	if cfg == nil || !AntiTruncationMatches(cfg.Streaming.AntiTruncation, model) {
		return body, nil, nil
	}
	if !AntiTruncationV2TextModel(model) {
		return nil, nil, errors.New("anti-truncation-v2: non-text model is unsupported")
	}
	prefix := ""
	if root != "" {
		prefix = root + "."
	}
	if name == "" {
		existing := map[string]bool{}
		for _, tool := range gjson.GetBytes(body, prefix+"tools").Array() {
			for _, field := range []string{"functionDeclarations", "function_declarations"} {
				for _, decl := range tool.Get(field).Array() {
					existing[decl.Get("name").String()] = true
				}
			}
		}
		var err error
		name, err = randomAntiTruncationTool(existing, antiTruncationV2ToolPrefix)
		if err != nil {
			return nil, nil, err
		}
		body, err = sjson.SetBytes(body, prefix+"tools.-1", map[string]any{"functionDeclarations": []any{antiTruncationV2Declaration(name)}})
		if err != nil {
			return nil, nil, err
		}
		contents := gjson.GetBytes(body, prefix+"contents").Array()
		if len(contents) == 0 || contents[len(contents)-1].Get("role").String() == "model" {
			body, err = sjson.SetBytes(body, prefix+"contents.-1", map[string]any{"role": "user", "parts": []any{map[string]string{"text": antiTruncationV2Instruction(name)}}})
		} else {
			body, err = sjson.SetBytes(body, prefix+"systemInstruction.parts.-1", map[string]string{"text": antiTruncationV2Instruction(name)})
		}
		if err != nil {
			return nil, nil, err
		}
		mode := prefix + "toolConfig.functionCallingConfig.mode"
		if m := gjson.GetBytes(body, mode).String(); m == "" || m == "NONE" {
			body, err = sjson.SetBytes(body, mode, "AUTO")
			if err != nil {
				return nil, nil, err
			}
		}
	}
	out, a, err := prepareAntiTruncation(cfg, model, body, root, stream, name)
	return out, a, err
}

// runV2 retries only empty responses with the original payload. It never adds
// a continuation turn or repeats a partially delivered answer or real tool call.
func (a *AntiTruncation) runV2(ctx context.Context, client *http.Client, body io.ReadCloser, rebuild func([]byte) (*http.Request, error), output io.Writer) error {
	defer func() { _ = body.Close() }()
	usage := map[string]any{}
	totalSize := 0
	for attempt := 1; attempt <= a.attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		answer, err := readAntiAnswerV2(body, a.stream, a.toolName, output)
		_ = body.Close()
		if err != nil {
			return err
		}
		totalSize += answer.size
		if totalSize > antiTruncationLimit {
			return errors.New("anti-truncation-v2: total response exceeds 16 MiB buffer limit")
		}
		for key, value := range answer.usage {
			if n, ok := value.(float64); ok {
				prev, _ := usage[key].(float64)
				usage[key] = prev + n
			}
		}
		parts, text := answer.cleanParts()
		empty := strings.TrimSpace(text) == "" && !answer.synthetic && !answer.realTools && !answer.blocked && (answer.finish == "" || answer.finish == "STOP")
		for _, p := range parts {
			m := p.(map[string]any)
			if _, isText := m["text"]; !isText || m["thought"] == true {
				empty = false
			}
		}
		if empty && attempt == a.attempts {
			return fmt.Errorf("anti-truncation-v2: empty response after %d attempts", a.attempts)
		}
		if !empty || attempt == a.attempts {
			if a.stream && answer.synthetic {
				// Synthetic text has already been flushed; keep only other parts.
				kept := parts[:0]
				for _, p := range parts {
					m := p.(map[string]any)
					if _, isText := m["text"]; !isText || m["thought"] == true {
						kept = append(kept, p)
					}
				}
				parts = kept
			}
			return writeAntiAnswer(output, answer, parts, answer.finish, usage, a.stream)
		}
		next, err := rebuild(bytes.Clone(a.base))
		if err != nil {
			return err
		}
		response, err := client.Do(next.Clone(ctx))
		if err != nil {
			return err
		}
		body = response.Body
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("anti-truncation-v2: empty-response retry returned HTTP %d", response.StatusCode)
		}
	}
	return errors.New("anti-truncation-v2: exhausted attempts")
}
