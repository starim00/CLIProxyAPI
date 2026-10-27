package helps

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// EmbeddingRequest accepts text, never provider-specific token IDs.
type EmbeddingRequest struct {
	Model      string          `json:"model"`
	Input      json.RawMessage `json:"input"`
	Dimensions *int            `json:"dimensions"`
	Encoding   string          `json:"encoding_format"`
	TaskType   string          `json:"task_type"`
	Title      string          `json:"title"`
	Texts      []string        `json:"-"`
}

func ParseEmbeddingRequest(body []byte) (EmbeddingRequest, error) {
	var r EmbeddingRequest
	if err := json.Unmarshal(body, &r); err != nil {
		return r, fmt.Errorf("invalid embeddings JSON: %w", err)
	}
	if strings.TrimSpace(r.Model) == "" {
		return r, fmt.Errorf("model must be a non-empty string")
	}
	var text string
	if json.Unmarshal(r.Input, &text) == nil && string(r.Input) != "null" {
		r.Texts = []string{text}
	} else if err := json.Unmarshal(r.Input, &r.Texts); err != nil {
		return r, fmt.Errorf("input must be text or an array of strings; token ID inputs are not supported by Google embeddings")
	}
	if len(r.Texts) == 0 || len(r.Texts) > 2048 {
		return r, fmt.Errorf("input must contain between 1 and 2048 texts")
	}
	for _, text := range r.Texts {
		if text == "" {
			return r, fmt.Errorf("input texts must not be empty")
		}
	}
	if r.Dimensions != nil && *r.Dimensions <= 0 {
		return r, fmt.Errorf("dimensions must be a positive integer")
	}
	if r.Encoding == "" {
		r.Encoding = "float"
	}
	if r.Encoding != "float" && r.Encoding != "base64" {
		return r, fmt.Errorf("encoding_format must be float or base64")
	}
	return r, nil
}

// EmbeddingPayload creates one request per input to honor Vertex predict limits.
func EmbeddingPayload(r EmbeddingRequest, text string, predict bool) []byte {
	var body map[string]any
	if predict {
		instance := map[string]any{"content": text}
		if r.TaskType != "" {
			instance["task_type"] = r.TaskType
		}
		if r.Title != "" {
			instance["title"] = r.Title
		}
		params := map[string]any{"autoTruncate": false}
		if r.Dimensions != nil {
			params["outputDimensionality"] = *r.Dimensions
		}
		body = map[string]any{"instances": []any{instance}, "parameters": params}
	} else {
		cfg := map[string]any{"autoTruncate": false}
		if r.Dimensions != nil {
			cfg["outputDimensionality"] = *r.Dimensions
		}
		if r.TaskType != "" {
			cfg["taskType"] = r.TaskType
		}
		if r.Title != "" {
			cfg["title"] = r.Title
		}
		body = map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": text}}}, "embedContentConfig": cfg}
	}
	encoded, _ := json.Marshal(body)
	return encoded
}

// DecodeEmbedding rejects empty, malformed and silently truncated upstream results.
func DecodeEmbedding(body []byte, predict bool) ([]float32, int64, error) {
	type vector struct {
		Values     []float32 `json:"values"`
		Statistics struct {
			TokenCount float64 `json:"token_count"`
			Truncated  bool    `json:"truncated"`
		} `json:"statistics"`
	}
	var response struct {
		Embedding   vector `json:"embedding"`
		Predictions []struct {
			Embeddings vector `json:"embeddings"`
		} `json:"predictions"`
		Usage struct {
			Prompt int64 `json:"promptTokenCount"`
			Total  int64 `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, 0, fmt.Errorf("invalid embedding response: %w", err)
	}
	v := response.Embedding
	if predict {
		if len(response.Predictions) != 1 {
			return nil, 0, fmt.Errorf("expected exactly one embedding prediction")
		}
		v = response.Predictions[0].Embeddings
	}
	if len(v.Values) == 0 || v.Statistics.Truncated {
		return nil, 0, fmt.Errorf("upstream returned an empty or truncated embedding")
	}
	// A JSON null would otherwise silently decode to float32(0).
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(body, &raw)
	vectorJSON := raw["embedding"]
	if predict {
		var predictions []map[string]json.RawMessage
		_ = json.Unmarshal(raw["predictions"], &predictions)
		vectorJSON = predictions[0]["embeddings"]
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(vectorJSON, &fields)
	var elements []json.RawMessage
	_ = json.Unmarshal(fields["values"], &elements)
	for _, element := range elements {
		if strings.TrimSpace(string(element)) == "null" {
			return nil, 0, fmt.Errorf("upstream returned a null embedding component")
		}
	}
	tokens := int64(v.Statistics.TokenCount)
	if response.Usage.Prompt > 0 {
		tokens = response.Usage.Prompt
	} else if response.Usage.Total > 0 {
		tokens = response.Usage.Total
	}
	return v.Values, tokens, nil
}

func EncodeEmbeddings(r EmbeddingRequest, vectors [][]float32, tokens int64) ([]byte, error) {
	data := make([]map[string]any, len(vectors))
	for i, vector := range vectors {
		var embedding any = vector
		if r.Encoding == "base64" {
			b := make([]byte, len(vector)*4)
			for j, v := range vector {
				binary.LittleEndian.PutUint32(b[j*4:], math.Float32bits(v))
			}
			embedding = base64.StdEncoding.EncodeToString(b)
		}
		data[i] = map[string]any{"object": "embedding", "index": i, "embedding": embedding}
	}
	return json.Marshal(map[string]any{"object": "list", "model": r.Model, "data": data, "usage": map[string]int64{"prompt_tokens": tokens, "total_tokens": tokens}})
}
