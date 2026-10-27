package registry

// WithGoogleEmbeddingModels supplements the default catalog, not explicit allowlists.
func WithGoogleEmbeddingModels(models []*ModelInfo) []*ModelInfo {
	for _, id := range []string{"gemini-embedding-001", "gemini-embedding-2"} {
		found := false
		for _, model := range models {
			if model != nil && model.ID == id {
				found = true
				break
			}
		}
		if !found {
			models = append(models, &ModelInfo{ID: id, Object: "model", OwnedBy: "google", Type: "gemini", DisplayName: id, SupportedGenerationMethods: []string{"embedContent"}})
		}
	}
	return models
}
