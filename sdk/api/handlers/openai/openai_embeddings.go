package openai

import (
	"context"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

// Embeddings serves native OpenAI-compatible and translated Google embeddings.
func (h *OpenAIAPIHandler) Embeddings(c *gin.Context) {
	body, err := handlers.ReadRequestBody(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: err.Error(), Type: "invalid_request_error"}})
		return
	}
	input, err := helps.ParseOpenAIEmbeddingRequest(body)
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: err.Error(), Type: "invalid_request_error"}})
		return
	}
	h.executeNativeJSON(c, input.Model, body, "embeddings")
}

// Decisions forwards the native Decisions JSON schema to the selected provider.
func (h *OpenAIAPIHandler) Decisions(c *gin.Context) {
	body, err := handlers.ReadRequestBody(c)
	if err != nil || !ojson.ValidBytes(body) {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: "invalid JSON request body", Type: "invalid_request_error"}})
		return
	}
	model := ojson.GetBytes(body, "model")
	if model.Type != ojson.String || strings.TrimSpace(model.String()) == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: "model must be a non-empty string", Type: "invalid_request_error"}})
		return
	}
	h.executeNativeJSON(c, model.String(), body, "decisions")
}

func (h *OpenAIAPIHandler) executeNativeJSON(c *gin.Context, model string, body []byte, operation string) {
	ctx, cancel := h.GetContextWithCancel(h, c, context.Background())
	defer cancel()
	response, headers, failure := h.ExecuteWithAuthManager(ctx, h.HandlerType(), model, body, operation)
	if failure != nil {
		h.WriteErrorResponse(c, failure)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), headers)
	c.Data(http.StatusOK, "application/json", response)
}
