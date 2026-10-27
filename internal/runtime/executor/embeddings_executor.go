package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func (e *GeminiExecutor) executeEmbeddings(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	url := fmt.Sprintf("%s/%s/models/%s:embedContent", resolveGeminiBaseURL(auth), glAPIVersion, req.Model)
	return helps.ExecuteGoogleEmbeddings(ctx, e.cfg, auth, req, opts, url, false, helps.NewExecutorUsageReporter(ctx, e, req.Model, auth), func(r *http.Request) (*http.Response, error) {
		applyGeminiHeaders(r, auth, opts.Headers)
		return e.HttpRequest(ctx, auth, r)
	})
}

func (e *GeminiVertexExecutor) executeEmbeddings(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	// Embedding 2 uses embedContent; the earlier text embedding models use predict.
	predict := !strings.HasPrefix(req.Model, "gemini-embedding-2")
	action := "embedContent"
	if predict {
		action = "predict"
	}
	key, base := vertexAPICreds(auth)
	url := vertexAPIKeyModelsURL(base, req.Model, action)
	if key == "" {
		project, location, _, err := vertexCreds(auth)
		if err != nil {
			return coreexecutor.Response{}, err
		}
		url = fmt.Sprintf("%s/%s/projects/%s/locations/%s/publishers/google/models/%s:%s", vertexBaseURL(location), vertexAPIVersion, project, location, req.Model, action)
	}
	return helps.ExecuteGoogleEmbeddings(ctx, e.cfg, auth, req, opts, url, predict, helps.NewExecutorUsageReporter(ctx, e, req.Model, auth), func(r *http.Request) (*http.Response, error) {
		return e.HttpRequest(ctx, auth, r)
	})
}
