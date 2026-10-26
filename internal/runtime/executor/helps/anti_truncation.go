package helps

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// AntiTruncationModelPrefix exposes opt-in variants without changing ordinary models.
const AntiTruncationModelPrefix = "抗截断/"

// AntiTruncationMatches checks the configured upstream model allowlist.
func AntiTruncationMatches(c config.AntiTruncationConfig, model string) bool {
	if !c.Enabled {
		return false
	}
	patterns := c.Models
	if len(patterns) == 0 {
		patterns = []string{"gemini-*"}
	}
	for _, pattern := range patterns {
		if ok, _ := path.Match(pattern, model); ok {
			return true
		}
	}
	return false
}

// PrepareAntiTruncationForRequest requires explicit selection by the API handler.
func PrepareAntiTruncationForRequest(cfg *config.Config, model string, body []byte, root string, stream bool, opts coreexecutor.Options) ([]byte, *AntiTruncation, error) {
	if enabled, _ := opts.Metadata[coreexecutor.AntiTruncationMetadataKey].(bool); !enabled {
		return body, nil, nil
	}
	if cfg == nil || !AntiTruncationMatches(cfg.Streaming.AntiTruncation, model) {
		return nil, nil, errors.New("anti-truncation: resolved upstream model is not enabled")
	}
	state, _ := opts.Metadata[antiTruncationPreparationKey].(antiTruncationPreparation)
	return prepareAntiTruncationV2(cfg, model, body, root, stream, state.toolName)
}

const antiTruncationLimit = 16 << 20

// AntiTruncation retains an unconfigured request so every empty retry passes
// through the executor's normal payload finalizer exactly once.
type AntiTruncation struct {
	toolName string
	base     []byte
	stream   bool
	attempts int
}

// PrepareAntiTruncation runs after translation and before user payload rules.
// Disabled and unmatched requests are returned byte-for-byte unchanged.
func PrepareAntiTruncation(cfg *config.Config, model string, body []byte, root string, stream bool) ([]byte, *AntiTruncation, error) {
	return prepareAntiTruncationV2(cfg, model, body, root, stream, "")
}

func prepareAntiTruncation(cfg *config.Config, model string, body []byte, root string, stream bool, toolName string) ([]byte, *AntiTruncation, error) {
	if cfg == nil || !cfg.Streaming.AntiTruncation.Enabled {
		return body, nil, nil
	}
	c := cfg.Streaming.AntiTruncation
	if !AntiTruncationMatches(c, model) {
		return body, nil, nil
	}
	prefix := ""
	if root != "" {
		prefix = root + "."
	}
	if !gjson.ValidBytes(body) {
		return nil, nil, errors.New("anti-truncation: invalid request JSON")
	}
	if gjson.GetBytes(body, prefix+"generationConfig.candidateCount").Int() > 1 {
		return nil, nil, errors.New("anti-truncation requires candidateCount <= 1")
	}
	existing := map[string]bool{}
	for _, tool := range gjson.GetBytes(body, prefix+"tools").Array() {
		for _, field := range []string{"functionDeclarations", "function_declarations"} {
			for _, decl := range tool.Get(field).Array() {
				existing[decl.Get("name").String()] = true
			}
		}
	}
	out := body
	if toolName == "" || !existing[toolName] {
		return nil, nil, errors.New("anti-truncation: translated request lost the injected tool")
	}
	attempts := c.MaxAttempts
	if attempts <= 0 {
		attempts = 3
	}
	if attempts > 10 {
		attempts = 10
	}
	return out, &AntiTruncation{toolName: toolName, base: bytes.Clone(out), stream: stream, attempts: attempts}, nil
}

type antiTruncationBody struct {
	*io.PipeReader
	cancel context.CancelFunc
}

// RebuildAntiTruncationRequest preserves transport headers while replacing the
// body with an already-finalized retry. It does not mutate the template.
func RebuildAntiTruncationRequest(template *http.Request, body []byte) *http.Request {
	next := template.Clone(template.Context())
	next.Body = io.NopCloser(bytes.NewReader(body))
	next.ContentLength = int64(len(body))
	next.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return next
}

func (b *antiTruncationBody) Close() error {
	b.cancel()
	return b.PipeReader.Close()
}

// Do preserves the initial HTTP status. Successful responses are decoded before
// protocol translation. Empty replies can trigger bounded retries, while
// explicit terminal refusals and real tool calls never trigger them.
func (a *AntiTruncation) Do(client *http.Client, request *http.Request, rebuild func([]byte) (*http.Request, error)) (*http.Response, error) {
	if a == nil {
		return client.Do(request)
	}
	ctx, cancel := context.WithCancel(request.Context())
	response, err := client.Do(request.Clone(ctx))
	if err != nil {
		cancel()
		return response, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Keep the original body and release the child context when it is closed.
		response.Body = &antiTruncationHTTPBody{ReadCloser: response.Body, cancel: cancel}
		return response, nil
	}
	upstream := response.Body
	reader, writer := io.Pipe()
	response.Body = &antiTruncationBody{PipeReader: reader, cancel: cancel}
	response.ContentLength = -1
	response.Header = response.Header.Clone()
	response.Header.Del("Content-Length")
	go func() {
		defer cancel()
		errRun := a.runV2(ctx, client, upstream, rebuild, writer)
		_ = writer.CloseWithError(errRun)
	}()
	return response, nil
}

type antiTruncationHTTPBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *antiTruncationHTTPBody) Close() error { defer b.cancel(); return b.ReadCloser.Close() }

// antiAnswer is one attempt, buffered to avoid duplicating ordinary text when
// the same answer is also returned via the synthetic tool.
type antiAnswer struct {
	toolName  string
	template  map[string]any
	envelope  map[string]any
	candidate map[string]any
	parts     []any
	finish    string
	usage     map[string]any
	wrapped   bool
	synthetic bool
	realTools bool
	blocked   bool
	size      int
}

func (r *antiAnswer) observe(data []byte) error {
	r.size += len(data)
	if r.size > antiTruncationLimit {
		return errors.New("anti-truncation: response exceeds 16 MiB buffer limit")
	}
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("anti-truncation: invalid upstream JSON: %w", err)
	}
	if _, ok := envelope["error"]; ok {
		return errors.New("anti-truncation: upstream returned an error event")
	}
	inner := envelope
	r.envelope = envelope
	if v, ok := envelope["response"].(map[string]any); ok {
		inner = v
		r.wrapped = true
	}
	if _, ok := inner["error"]; ok {
		return errors.New("anti-truncation: upstream returned an error event")
	}
	r.template = inner
	if u, ok := inner["usageMetadata"].(map[string]any); ok {
		r.usage = u
	}
	if feedback, ok := inner["promptFeedback"].(map[string]any); ok && feedback["blockReason"] != nil {
		r.blocked = true
	}
	candidates, _ := inner["candidates"].([]any)
	if len(candidates) > 1 {
		return errors.New("anti-truncation: multiple response candidates are unsupported")
	}
	for _, value := range candidates {
		candidate, _ := value.(map[string]any)
		r.candidate = candidate
		if finish, ok := candidate["finishReason"].(string); ok && finish != "" {
			r.finish = finish
		}
		content, _ := candidate["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		for _, p := range parts {
			part, ok := p.(map[string]any)
			if !ok {
				return errors.New("anti-truncation: invalid response part")
			}
			if call, ok := part["functionCall"].(map[string]any); ok {
				if r.toolName != "" && call["name"] == r.toolName {
					if raw, ok := call["args"].(string); ok {
						text, err := repairAntiTruncationV2Arguments(raw)
						if err != nil {
							return err
						}
						call["args"] = map[string]any{"content": text}
					}
					args, ok := call["args"].(map[string]any)
					if !ok {
						return errors.New("anti-truncation: invalid synthetic tool arguments")
					}
					text, ok := args["content"].(string)
					if !ok {
						return errors.New("anti-truncation: synthetic answer content must be a string")
					}
					r.synthetic = true
					r.parts = append(r.parts, map[string]any{"text": text, "cpaSynthetic": true})
					continue
				}
				r.realTools = true
			}
			r.parts = append(r.parts, part)
		}
	}
	return nil
}

func (r *antiAnswer) cleanParts() ([]any, string) {
	parts := make([]any, 0, len(r.parts))
	var text strings.Builder
	for _, p := range r.parts {
		part := p.(map[string]any)
		synthetic, _ := part["cpaSynthetic"].(bool)
		thought, _ := part["thought"].(bool)
		value, isText := part["text"].(string)
		if r.synthetic && isText && !thought && !synthetic {
			continue
		}
		delete(part, "cpaSynthetic")
		parts = append(parts, part)
		if isText && !thought {
			text.WriteString(value)
		}
	}
	return parts, text.String()
}

func readAntiAnswer(body io.Reader, stream bool, toolNames ...string) (antiAnswer, error) {
	var result antiAnswer
	if len(toolNames) > 0 {
		result.toolName = toolNames[0]
	}
	return readAntiAnswerObserved(body, stream, result, nil)
}

func readAntiAnswerObserved(body io.Reader, stream bool, result antiAnswer, onEvent func(*antiAnswer, int) error) (antiAnswer, error) {
	if !stream {
		data, err := io.ReadAll(io.LimitReader(body, antiTruncationLimit+1))
		if err != nil {
			return result, err
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return result, nil
		}
		err = result.observe(data)
		return result, err
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, antiTruncationLimit)
	var event []byte
	flush := func() error {
		data := bytes.TrimSpace(event)
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			event = nil
			return nil
		}
		start := len(result.parts)
		err := result.observe(data)
		if err == nil && onEvent != nil {
			err = onEvent(&result, start)
		}
		event = nil
		return err
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			if err := flush(); err != nil {
				return result, err
			}
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		if len(event) > 0 {
			event = append(event, '\n')
		}
		event = append(event, bytes.TrimSpace(line[5:])...)
		if len(event) > antiTruncationLimit {
			return result, errors.New("anti-truncation: SSE event too large")
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	if err := flush(); err != nil {
		return result, err
	}
	return result, nil
}

func writeAntiAnswer(w io.Writer, answer antiAnswer, parts []any, finish string, usage map[string]any, stream bool) error {
	inner := answer.template
	if inner == nil {
		inner = map[string]any{}
	}
	delete(inner, "usageMetadata")
	if usage != nil {
		inner["usageMetadata"] = usage
	}
	if len(parts) > 0 || finish != "" {
		candidate := answer.candidate
		if candidate == nil {
			candidate = map[string]any{"index": 0}
		}
		candidate["content"] = map[string]any{"role": "model", "parts": parts}
		delete(candidate, "finishReason")
		if finish != "" {
			candidate["finishReason"] = finish
		}
		inner["candidates"] = []any{candidate}
	} else {
		delete(inner, "candidates")
	}
	var envelope any = inner
	if answer.wrapped {
		answer.envelope["response"] = inner
		envelope = answer.envelope
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if stream {
		_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	} else {
		_, err = w.Write(data)
	}
	return err
}
