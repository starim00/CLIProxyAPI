package helps

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const antiTruncationTool = "cpa_emit_answer"

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
	return PrepareAntiTruncation(cfg, model, body, root, stream)
}

const antiTruncationLimit = 16 << 20
const antiTruncationInstruction = "严格执行以下输出规则：\n\n" +
	"1. 你必须调用 `cpa_emit_answer` 工具来输出你的最终回答\n" +
	"2. 将完整的回答内容放入该工具的 `content` 参数中\n" +
	"3. 不要在普通文本中输出任何内容，所有回答必须通过 `cpa_emit_answer` 工具输出\n" +
	"4. 如果你的回答被截断，系统会要求你继续输出剩余内容\n" +
	"5. 续传时，将剩余内容继续通过 `cpa_emit_answer` 工具输出\n\n" +
	"这个规则对于确保输出完整性极其重要，请严格遵守。"

const antiTruncationContinuationInstruction = "你之前的回复被截断了。请调用 `cpa_emit_answer` 工具继续输出剩余的所有内容。\n\n" +
	"重要提醒：\n" +
	"1. 不要重复前面已经输出的内容\n" +
	"2. 直接继续输出，无需任何前言或解释\n" +
	"3. 将剩余内容放入 `cpa_emit_answer` 工具的 `content` 参数中\n\n" +
	"现在请继续输出："

// AntiTruncation retains an unconfigured request so every continuation passes
// through the executor's normal payload finalizer exactly once.
type AntiTruncation struct {
	base     []byte
	root     string
	stream   bool
	attempts int
}

// PrepareAntiTruncation runs after translation and before user payload rules.
// Disabled and unmatched requests are returned byte-for-byte unchanged.
func PrepareAntiTruncation(cfg *config.Config, model string, body []byte, root string, stream bool) ([]byte, *AntiTruncation, error) {
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
	for _, tool := range gjson.GetBytes(body, prefix+"tools").Array() {
		for _, field := range []string{"functionDeclarations", "function_declarations"} {
			for _, decl := range tool.Get(field).Array() {
				if decl.Get("name").String() == antiTruncationTool {
					return nil, nil, errors.New("anti-truncation: reserved tool name cpa_emit_answer is already in use")
				}
			}
		}
	}
	declaration := json.RawMessage(`{"functionDeclarations":[{"name":"cpa_emit_answer","description":"You MUST call this tool exactly once to output your final user-visible answer. Put the complete answer in the 'content' argument. Do NOT output any text outside this tool call.","parameters":{"type":"object","properties":{"content":{"type":"string","description":"The complete final answer to output to the user."}},"required":["content"]}}]}`)
	out, err := sjson.SetBytes(body, prefix+"tools.-1", declaration)
	if err != nil {
		return nil, nil, err
	}
	out, err = sjson.SetBytes(out, prefix+"systemInstruction.parts.-1", map[string]string{"text": antiTruncationInstruction})
	if err != nil {
		return nil, nil, err
	}
	modePath := prefix + "toolConfig.functionCallingConfig.mode"
	mode := gjson.GetBytes(out, modePath).String()
	if mode == "" || mode == "NONE" {
		out, err = sjson.SetBytes(out, modePath, "AUTO")
		if err != nil {
			return nil, nil, err
		}
	}
	allowed := prefix + "toolConfig.functionCallingConfig.allowedFunctionNames"
	if gjson.GetBytes(out, allowed).IsArray() {
		out, err = sjson.SetBytes(out, allowed+".-1", antiTruncationTool)
		if err != nil {
			return nil, nil, err
		}
	}
	attempts := c.MaxAttempts
	if attempts <= 0 {
		attempts = 3
	}
	if attempts > 10 {
		attempts = 10
	}
	return out, &AntiTruncation{base: bytes.Clone(out), root: root, stream: stream, attempts: attempts}, nil
}

type antiTruncationBody struct {
	*io.PipeReader
	cancel context.CancelFunc
}

// RebuildAntiTruncationRequest preserves transport headers while replacing the
// body with an already-finalized continuation. It does not mutate the template.
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
// protocol translation; a missing terminator or MAX_TOKENS can trigger bounded
// continuation. Explicit terminal refusals and real tool calls never trigger it.
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
		errRun := a.run(ctx, client, upstream, rebuild, writer)
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
				if call["name"] == antiTruncationTool {
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

func readAntiAnswer(body io.Reader, stream bool) (antiAnswer, error) {
	var result antiAnswer
	if !stream {
		data, err := io.ReadAll(io.LimitReader(body, antiTruncationLimit+1))
		if err != nil {
			return result, err
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
		err := result.observe(data)
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

func (a *AntiTruncation) continuation(text string) ([]byte, error) {
	prefix := ""
	if a.root != "" {
		prefix = a.root + "."
	}
	body := bytes.Clone(a.base)
	if text != "" {
		var err error
		body, err = sjson.SetBytes(body, prefix+"contents.-1", map[string]any{"role": "model", "parts": []any{map[string]string{"text": text}}})
		if err != nil {
			return nil, err
		}
	}
	return sjson.SetBytes(body, prefix+"contents.-1", map[string]any{"role": "user", "parts": []any{map[string]string{"text": antiTruncationContinuationInstruction}}})
}

func (a *AntiTruncation) run(ctx context.Context, client *http.Client, body io.ReadCloser, rebuild func([]byte) (*http.Request, error), output io.Writer) error {
	defer func() { _ = body.Close() }()
	var history strings.Builder
	var allParts []any
	usage := map[string]any{}
	totalSize := 0
	for attempt := 1; attempt <= a.attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		answer, readErr := readAntiAnswer(body, a.stream)
		_ = body.Close()
		if err := ctx.Err(); err != nil {
			return err
		}
		totalSize += answer.size
		if totalSize > antiTruncationLimit {
			return errors.New("anti-truncation: total response exceeds 16 MiB buffer limit")
		}
		parts, text := answer.cleanParts()
		history.WriteString(text)
		for key, value := range answer.usage {
			if n, ok := value.(float64); ok {
				prev, _ := usage[key].(float64)
				usage[key] = prev + n
			}
		}
		// Only transport truncation, missing terminal markers and token limits are
		// continuable. Malformed JSON/error events must remain explicit failures.
		var networkErr net.Error
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.As(readErr, &networkErr) {
			return readErr
		}
		if answer.realTools && readErr != nil {
			return readErr
		}
		complete := answer.blocked || answer.realTools || (answer.finish != "" && answer.finish != "MAX_TOKENS") || (answer.synthetic && answer.finish == "" && readErr == nil)
		if complete && readErr != nil && !answer.realTools {
			complete = false
		}
		if complete && answer.finish == "" && !answer.blocked {
			answer.finish = "STOP"
		}
		if !complete && attempt == a.attempts {
			if a.stream && len(parts) > 0 {
				if err := writeAntiAnswer(output, answer, parts, "", nil, true); err != nil {
					return err
				}
			}
			return fmt.Errorf("anti-truncation: answer incomplete after %d attempts", a.attempts)
		}
		if a.stream {
			finish := ""
			var finalUsage map[string]any
			if complete {
				finish = answer.finish
				if finish == "" && answer.realTools {
					finish = "STOP"
				}
				finalUsage = usage
			}
			if err := writeAntiAnswer(output, answer, parts, finish, finalUsage, true); err != nil {
				return err
			}
		} else {
			allParts = append(allParts, parts...)
		}
		if complete {
			if !a.stream {
				return writeAntiAnswer(output, answer, allParts, answer.finish, usage, false)
			}
			return nil
		}
		payload, err := a.continuation(history.String())
		if err != nil {
			return err
		}
		next, err := rebuild(payload)
		if err != nil {
			return err
		}
		response, err := client.Do(next.Clone(ctx))
		if err != nil {
			return err
		}
		body = response.Body
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("anti-truncation: continuation returned HTTP %d", response.StatusCode)
		}
	}
	return errors.New("anti-truncation: exhausted attempts")
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
