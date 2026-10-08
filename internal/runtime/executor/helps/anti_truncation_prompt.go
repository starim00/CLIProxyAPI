package helps

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const antiTruncationToolPrefix = "emit_complete_response_"
const antiTruncationPreparationKey = "anti_truncation_preparation"

type antiTruncationPreparation struct {
	toolName string
	bypass   bool
}

func antiTruncationInstruction(name string) string {
	return fmt.Sprintf("Call the `%s` function exactly once and put your complete final reply in its `content` argument. Do not write any of the final reply outside that call. Use any other available tools normally when they are needed.", name)
}

func antiTruncationDeclaration(name string) map[string]any {
	return map[string]any{
		"name":        name,
		"description": "Emit the complete final user-visible reply exactly once. Put the entire reply in content and write no reply text outside this call.",
		"parameters": map[string]any{
			"type":       "object",
			"properties": map[string]any{"content": map[string]string{"type": "string", "description": "The complete final reply shown to the user."}},
			"required":   []string{"content"},
		},
	}
}

func randomAntiTruncationTool(existing map[string]bool) (string, error) {
	for attempt := 0; attempt < 100; attempt++ {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		name := antiTruncationToolPrefix + hex.EncodeToString(random[:])
		if !existing[name] {
			return name, nil
		}
	}
	return "", errors.New("anti-truncation: cannot allocate a unique tool name")
}

func skipAntiTruncationMessages(body []byte) bool {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return true
	}
	messages := gjson.GetBytes(body, "messages")
	choice := gjson.GetBytes(body, "tool_choice")
	return !messages.IsArray() || len(messages.Array()) == 0 || choice.String() == "none" || choice.String() == "required" || choice.IsObject() || choice.IsArray()
}

// PrepareAntiTruncationSource mirrors the preset's OpenAI injection before
// translation can collapse system messages or convert assistant roles.
// Metadata is copied because SDK callers may reuse options across requests.
func PrepareAntiTruncationSource(req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Request, coreexecutor.Options, error) {
	enabled, _ := opts.Metadata[coreexecutor.AntiTruncationMetadataKey].(bool)
	if !enabled || opts.SourceFormat != sdktranslator.FormatOpenAI || opts.Metadata[antiTruncationPreparationKey] != nil {
		return req, opts, nil
	}
	state := antiTruncationPreparation{}
	bodies := [][]byte{req.Payload}
	if len(opts.OriginalRequest) > 0 {
		bodies = append(bodies, opts.OriginalRequest)
	}
	existing := map[string]bool{}
	for _, body := range bodies {
		if skipAntiTruncationMessages(body) {
			state.bypass = true
		}
		if tools := gjson.GetBytes(body, "tools"); tools.IsArray() {
			for _, tool := range tools.Array() {
				existing[tool.Get("function.name").String()] = true
			}
		}
	}
	if !state.bypass {
		var err error
		state.toolName, err = randomAntiTruncationTool(existing)
		if err != nil {
			return req, opts, err
		}
		req.Payload, err = injectAntiTruncationMessages(req.Payload, state.toolName)
		if err != nil {
			return req, opts, err
		}
		if len(opts.OriginalRequest) > 0 {
			opts.OriginalRequest, err = injectAntiTruncationMessages(opts.OriginalRequest, state.toolName)
			if err != nil {
				return req, opts, err
			}
		}
	}
	opts.Metadata = maps.Clone(opts.Metadata)
	opts.Metadata[antiTruncationPreparationKey] = state
	return req, opts, nil
}

func injectAntiTruncationMessages(body []byte, name string) ([]byte, error) {
	messages := gjson.GetBytes(body, "messages").Array()
	index, role := len(messages), "system"
	for i, message := range messages {
		content := message.Get("content")
		if content.Type == gjson.String && strings.Contains(content.String(), "<format>") {
			index = i
			break
		}
	}
	if index == len(messages) && messages[len(messages)-1].Get("role").String() == "assistant" {
		role = "user"
	}
	control, err := json.Marshal(map[string]string{"role": role, "content": antiTruncationInstruction(name)})
	if err != nil {
		return nil, err
	}
	result := make([]json.RawMessage, 0, len(messages)+1)
	for i, message := range messages {
		if i == index {
			result = append(result, control)
		}
		result = append(result, json.RawMessage(message.Raw))
	}
	if index == len(messages) {
		result = append(result, control)
	}
	out, err := sjson.SetBytes(body, "messages", result)
	if err != nil {
		return nil, err
	}
	tools := make([]json.RawMessage, 0)
	if original := gjson.GetBytes(body, "tools"); original.IsArray() {
		for _, tool := range original.Array() {
			tools = append(tools, json.RawMessage(tool.Raw))
		}
	}
	tool, err := json.Marshal(map[string]any{"type": "function", "function": antiTruncationDeclaration(name)})
	if err != nil {
		return nil, err
	}
	out, err = sjson.SetBytes(out, "tools", append(tools, tool))
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(out, "tool_choice", "auto")
}
