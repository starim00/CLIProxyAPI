package cliproxy

import (
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"testing"
)

func TestOpenAIAPIKeyEmbeddingCatalog(t *testing.T) {
	for _, base := range []string{"https://api.openai.com/v1", "https://api.openai.com/v1/", "https://chatgpt.com/backend-api/codex", "https://other.example/v1"} {
		models := buildCodexConfigModels(&config.CodexKey{BaseURL: base})
		found := map[string]bool{}
		for _, model := range models {
			found[model.ID] = true
		}
		want := base == "https://api.openai.com/v1" || base == "https://api.openai.com/v1/"
		for _, name := range []string{"text-embedding-3-small", "text-embedding-3-large", "text-embedding-ada-002"} {
			if found[name] != want {
				t.Errorf("%s: model %s present=%v want=%v", base, name, found[name], want)
			}
		}
	}
}
