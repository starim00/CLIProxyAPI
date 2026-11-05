package cliproxy

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestVertexTTSCredentialPrefix(t *testing.T) {
	for _, prefix := range []string{"vertex", "custom-google"} {
		t.Run(prefix, func(t *testing.T) {
			svc := &Service{cfg: &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: true}}}
			auth := &coreauth.Auth{ID: "tts-" + prefix, Provider: "vertex", Prefix: prefix}
			t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
			svc.registerModelsForAuth(t.Context(), auth)
			found := 0
			for _, model := range GlobalModelRegistry().GetModelsForClient(auth.ID) {
				switch model.ID {
				case prefix + "/gemini-3.8-flash-tts":
					found++
					if len(model.SupportedOutputModalities) != 1 || model.SupportedOutputModalities[0] != "AUDIO" {
						t.Fatal("missing audio output metadata")
					}
				case "gemini-3.8-flash-tts", "抗截断/" + prefix + "/gemini-3.8-flash-tts":
					t.Fatalf("unexpected alias: %s", model.ID)
				}
			}
			if found != 1 {
				t.Fatalf("TTS registrations = %d, want 1", found)
			}
		})
	}
}
