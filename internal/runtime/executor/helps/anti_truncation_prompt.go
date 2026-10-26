package helps

import (
	"crypto/rand"
	"encoding/hex"
	"errors"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const antiTruncationPreparationKey = "anti_truncation_preparation"

type antiTruncationPreparation struct{ toolName string }

func randomAntiTruncationTool(existing map[string]bool, prefix string) (string, error) {
	for attempt := 0; attempt < 100; attempt++ {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		name := prefix + hex.EncodeToString(random[:])
		if !existing[name] {
			return name, nil
		}
	}
	return "", errors.New("anti-truncation: cannot allocate a unique tool name")
}

// PrepareAntiTruncationSource injects the gateway control message before translation.
func PrepareAntiTruncationSource(req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Request, coreexecutor.Options, error) {
	enabled, _ := opts.Metadata[coreexecutor.AntiTruncationMetadataKey].(bool)
	if !enabled || opts.SourceFormat != sdktranslator.FormatOpenAI || opts.Metadata[antiTruncationPreparationKey] != nil {
		return req, opts, nil
	}
	return prepareAntiTruncationV2Source(req, opts)
}
