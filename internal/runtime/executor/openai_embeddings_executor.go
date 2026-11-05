package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func (e *CodexExecutor) SupportsEmbeddings() bool { return true }

func (e *OpenAICompatExecutor) executeEmbeddings(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (result coreexecutor.Response, err error) {
	reporter := helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)
	if _, err = helps.ParseOpenAIEmbeddingRequest(req.Payload); err != nil {
		return result, statusErr{code: 400, msg: err.Error()}
	}
	base, _ := e.resolveCredentials(auth)
	if base == "" {
		return result, statusErr{code: 401, msg: "missing provider baseURL"}
	}
	doc := ojson.NewDoc(req.Payload)
	if err = doc.Set("model", req.Model); err != nil {
		return result, err
	}
	original := doc.Bytes()
	helps.ApplyPayloadConfigWithRequestForExecutorDoc(e.cfg, e.Identifier(), req.Model, "openai", opts.SourceFormat.String(), "", doc, original, helps.PayloadRequestedModel(opts, req.Model), "/v1/embeddings", opts.Headers)
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/embeddings", bytes.NewReader(doc.Bytes()))
	if err != nil {
		return result, err
	}
	r.Header.Set("Content-Type", "application/json")
	if err = e.PrepareRequest(r, auth); err != nil {
		return result, err
	}
	ensureOpenAICompatUserAgent(r, opts.Headers)
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(r, auth.Attributes, opts.Headers)
	}
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	response, err := client.Do(r)
	if err != nil {
		return result, err
	}
	defer func() { _ = response.Body.Close() }()
	result.Payload, err = io.ReadAll(response.Body)
	if err != nil {
		return result, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result, newOpenAICompatStatusError(response.StatusCode, response.Header, result.Payload)
	}
	if !ojson.ValidBytes(result.Payload) || !ojson.GetBytes(result.Payload, "data").IsArray() {
		return result, statusErr{code: 502, msg: "upstream returned an invalid embedding response"}
	}
	result.Headers = response.Header.Clone()
	reporter.Publish(ctx, helps.ParseOpenAIUsage(result.Payload))
	return result, nil
}
