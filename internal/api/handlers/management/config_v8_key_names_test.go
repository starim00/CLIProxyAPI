package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

func TestConfigV8PersistsIndependentKeyNames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("config-version: 8\nserver: {port: 8317}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	for _, family := range []string{"gemini", "interactions", "codex", "claude", "vertex", "meta", "xai", "openai-compatibility"} {
		body := `[{"name":"Shared group","base-url":"https://example.invalid","keys":[{"api-key":"fixture-primary","name":"Primary"},{"api-key":"fixture-backup","name":"Backup"}]}]`
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/v8/management/config/api-keys/"+family, strings.NewReader(body)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", family, recorder.Code, recorder.Body.String())
		}
	}
	// A runtime reload and unrelated configuration save must retain UI metadata.
	cfg, err = config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Port = 8318
	if err = config.SaveConfigPreserveComments(path, cfg, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Keys map[string][]struct {
			Name string `yaml:"name"`
			Keys []struct {
				Name string `yaml:"name"`
				Key  string `yaml:"api-key"`
			} `yaml:"keys"`
		} `yaml:"api-keys"`
	}
	if err = yaml.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Keys) != 8 {
		t.Fatalf("saved %d provider families, want 8", len(saved.Keys))
	}
	for family, groups := range saved.Keys {
		if len(groups) != 1 || groups[0].Name != "Shared group" || len(groups[0].Keys) != 2 {
			t.Fatalf("%s: group structure changed", family)
		}
		if groups[0].Keys[0].Name != "Primary" || groups[0].Keys[1].Name != "Backup" || groups[0].Keys[0].Key != "fixture-primary" || groups[0].Keys[1].Key != "fixture-backup" {
			t.Fatalf("%s: key metadata or identity changed", family)
		}
	}
}
