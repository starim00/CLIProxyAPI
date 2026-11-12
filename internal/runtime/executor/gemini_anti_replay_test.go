package executor

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestGeminiAntiTruncationDoesNotReserveNativeReplay(t *testing.T) {
	for _, reserve := range []bool{false, true} {
		const payload = `{"contents":[{"role":"user","parts":[{"text":"Hello"}]}]}`
		doc := ojson.NewDocString(payload)
		opts := coreexecutor.Options{Metadata: map[string]any{coreexecutor.AntiTruncationMetadataKey: true}}
		scope, body, changed, err := prepareGeminiTopLevelReasoningReplayPayloadDocWithMode(t.Context(), "gemini-3.8-flash", coreexecutor.Request{}, opts, doc, reserve)
		if err != nil || scope.valid() || body != nil || changed || doc.String() != payload {
			t.Fatalf("anti-truncation must not reserve or mutate native replay: reserve=%v scope=%v changed=%v err=%v", reserve, scope.valid(), changed, err)
		}
	}
}
