package helps

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"strings"
	"unicode/utf8"
)

// Native Gemini SSE carries decoded argument objects rather than OpenAI JSON
// string deltas. Flush each own-tool content event immediately, holding ordinary
// text until EOF so it can never be concatenated with a later synthetic answer.
func readAntiAnswerV2(body io.Reader, stream bool, name string, output io.Writer) (antiAnswer, error) {
	return readAntiAnswerObserved(body, stream, antiAnswer{toolName: name, v2: true}, func(answer *antiAnswer, start int) error {
		var parts []any
		for _, part := range answer.parts[start:] {
			m := part.(map[string]any)
			if m["cpaSynthetic"] == true && m["text"] != "" {
				parts = append(parts, map[string]any{"text": m["text"]})
			}
		}
		if len(parts) == 0 {
			return nil
		}
		snapshot := *answer
		snapshot.template = maps.Clone(answer.template)
		snapshot.candidate = maps.Clone(answer.candidate)
		snapshot.envelope = maps.Clone(answer.envelope)
		return writeAntiAnswer(output, snapshot, parts, "", nil, true)
	})
}

// Repair only an own-tool argument string, never an upstream envelope or a real
// tool. The accepted schema remains an object containing a string content field.
func repairAntiTruncationV2Arguments(raw string) (string, error) {
	if len(raw) > antiTruncationLimit || !utf8.ValidString(raw) {
		return "", errors.New("anti-truncation-v2: invalid or oversized arguments")
	}
	decode := func(s string) (string, bool) {
		var args map[string]json.RawMessage
		if json.Unmarshal([]byte(s), &args) != nil {
			return "", false
		}
		field, ok := args["content"]
		if !ok || len(field) == 0 || field[0] != '"' {
			return "", false
		}
		var text string
		if json.Unmarshal(field, &text) != nil {
			return "", false
		}
		return text, true
	}
	if text, ok := decode(raw); ok {
		return text, nil
	}
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```") {
		if newline := strings.IndexByte(s, '\n'); newline >= 0 {
			s = s[newline+1:]
		}
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
	}
	if text, ok := decode(s); ok {
		return text, nil
	}
	var out strings.Builder
	var stack []byte
	inString, escaped := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				out.WriteByte(c)
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
				out.WriteByte(c)
			case '"':
				inString = false
				out.WriteByte(c)
			case '\n':
				out.WriteString(`\n`)
			case '\r':
				out.WriteString(`\r`)
			case '\t':
				out.WriteString(`\t`)
			default:
				out.WriteByte(c)
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) == 0 || stack[len(stack)-1] != c {
				return "", errors.New("anti-truncation-v2: mismatched argument delimiters")
			}
			stack = stack[:len(stack)-1]
		case ',':
			tail := strings.TrimSpace(s[i+1:])
			if tail == "" || strings.HasPrefix(tail, "}") || strings.HasPrefix(tail, "]") {
				continue
			}
		}
		out.WriteByte(c)
	}
	// A dangling escape cannot safely be interpreted as generated text.
	if escaped {
		return "", errors.New("anti-truncation-v2: incomplete argument escape")
	}
	if inString {
		out.WriteByte('"')
	}
	for i := len(stack) - 1; i >= 0; i-- {
		out.WriteByte(stack[i])
	}
	if text, ok := decode(out.String()); ok {
		return text, nil
	}
	return "", errors.New("anti-truncation-v2: cannot decode synthetic content")
}
