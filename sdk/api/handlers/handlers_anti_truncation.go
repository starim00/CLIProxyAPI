package handlers

import (
	"fmt"
	"maps"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (h *BaseAPIHandler) antiTruncationAvailable(model string) bool {
	if h == nil || h.Cfg == nil || !h.Cfg.Streaming.AntiTruncation.Enabled ||
		(h.AuthManager != nil && h.AuthManager.HomeEnabled()) || strings.HasPrefix(model, helps.AntiTruncationModelPrefix) {
		return false
	}
	model = thinking.ParseSuffix(model).ModelName
	r := registry.GetGlobalRegistry()
	for _, provider := range r.GetModelProviders(model) {
		if provider != "gemini" && provider != "antigravity" {
			continue
		}
		canonical := model
		if info := r.GetModelInfo(model, provider); info != nil && info.MetadataModelID != "" {
			canonical = info.MetadataModelID
		}
		if helps.AntiTruncationMatches(h.Cfg.Streaming.AntiTruncation, canonical) {
			return true
		}
	}
	return false
}

// WithAntiTruncationModels appends opt-in variants without mutating registry metadata.
func (h *BaseAPIHandler) WithAntiTruncationModels(models []map[string]any) []map[string]any {
	result := append([]map[string]any(nil), models...)
	ids := make(map[string]bool, len(models))
	modelID := func(m map[string]any) string {
		if id, _ := m["id"].(string); id != "" {
			return id
		}
		name, _ := m["name"].(string)
		return strings.TrimPrefix(name, "models/")
	}
	for _, m := range models {
		ids[modelID(m)] = true
	}
	for _, m := range models {
		id := modelID(m)
		variantID := helps.AntiTruncationModelPrefix + id
		if ids[variantID] || !h.antiTruncationAvailable(id) {
			continue
		}
		variant := maps.Clone(m)
		if _, ok := variant["id"]; ok {
			variant["id"] = variantID
		}
		if name, ok := variant["name"].(string); ok {
			variant["name"] = variantID
			if strings.HasPrefix(name, "models/") {
				variant["name"] = "models/" + variantID
			}
		}
		for _, key := range []string{"displayName", "display_name"} {
			if display, ok := variant[key].(string); ok {
				variant[key] = helps.AntiTruncationModelPrefix + display
			}
		}
		result = append(result, variant)
		ids[variantID] = true
	}
	return result
}

func antiTruncationRequestError() *interfaces.ErrorMessage {
	return &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: fmt.Errorf("anti-truncation model is unavailable for this model, provider, or endpoint")}
}

func (h *BaseAPIHandler) resolveAntiTruncationModel(model string, body []byte, protocol string, image bool) (string, []byte, bool, *interfaces.ErrorMessage) {
	if !strings.HasPrefix(model, helps.AntiTruncationModelPrefix) {
		return model, body, false, nil
	}
	// Existing registered IDs take precedence over generated variants.
	if len(registry.GetGlobalRegistry().GetModelProviders(model)) > 0 {
		return model, body, false, nil
	}
	model = strings.TrimPrefix(model, helps.AntiTruncationModelPrefix)
	if image || protocol == "interactions" || !h.antiTruncationAvailable(model) {
		return "", nil, false, antiTruncationRequestError()
	}
	if gjson.GetBytes(body, "model").Exists() {
		var err error
		body, err = sjson.SetBytes(body, "model", model)
		if err != nil {
			return "", nil, false, antiTruncationRequestError()
		}
	}
	return model, body, true, nil
}

func antiTruncationProviders(providers []string, selected bool) ([]string, *interfaces.ErrorMessage) {
	if !selected {
		return providers, nil
	}
	var supported []string
	for _, p := range providers {
		if p == "gemini" || p == "antigravity" {
			supported = append(supported, p)
		}
	}
	if len(supported) == 0 {
		return nil, antiTruncationRequestError()
	}
	return supported, nil
}
