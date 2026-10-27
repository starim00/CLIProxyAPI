package openai

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

// Embeddings serves the OpenAI text embeddings contract through Google credentials.
func (h *OpenAIAPIHandler) Embeddings(c *gin.Context) {
	body, err := handlers.ReadRequestBody(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: err.Error(), Type: "invalid_request_error"}})
		return
	}
	input, err := helps.ParseEmbeddingRequest(body)
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: err.Error(), Type: "invalid_request_error"}})
		return
	}
	ctx, cancel := h.GetContextWithCancel(h, c, context.Background())
	defer cancel()
	response, headers, failure := h.ExecuteWithAuthManager(ctx, h.HandlerType(), input.Model, body, "embeddings")
	if failure != nil {
		h.WriteErrorResponse(c, failure)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), headers)
	c.Data(http.StatusOK, "application/json", response)
}
