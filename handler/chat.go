package handler

import (
	"net/http"
	"strings"

	"gateway/logic"
	"gateway/tools"

	"github.com/gin-gonic/gin"
	"github.com/sashabaranov/go-openai"
)

type ChatHandler struct{}

func NewChatHandler() *ChatHandler {
	return &ChatHandler{}
}

// Chat POST /chat — OpenAI Chat Completions 兼容（支持 stream）。
func (h *ChatHandler) Chat(c *gin.Context) {
	var req openai.ChatCompletionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"message": err.Error(),
				"type":    "invalid_request_error",
			},
		})
		return
	}
	if req.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"message": "model is required",
				"type":    "invalid_request_error",
			},
		})
		return
	}

	messages, bad := convertMessages(req.Messages)
	if bad {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"message": "only string message content is supported",
				"type":    "invalid_request_error",
			},
		})
		return
	}

	serCtx := c.MustGet("svc_ctx").(*tools.ServiceContext)
	chatLogic := logic.NewChatLogic(c.Request.Context(), serCtx)

	apiKey, _ := c.Get("api_key")
	keyStr, _ := apiKey.(string)

	params := logic.ChatParams{
		ApiKey:      keyStr,
		SessionID:   c.GetHeader("X-Session-Id"),
		Model:       req.Model,
		Messages:    messages,
		RoutingMode: c.GetHeader("X-Routing-Mode"),
		Stream:      req.Stream,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	}

	if err := chatLogic.Chat(c.Request.Context(), c.Writer, params); err != nil {
		if strings.Contains(err.Error(), "concurrency") || strings.Contains(err.Error(), "rate limit") {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": gin.H{
					"message": err.Error(),
					"type":    "rate_limit_error",
				},
			})
			return
		}
		if params.Stream {
			if c.Writer.Size() <= 0 {
				c.JSON(http.StatusBadGateway, gin.H{
					"error": gin.H{
						"message": err.Error(),
						"type":    "server_error",
					},
				})
			}
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"message": err.Error(),
				"type":    "server_error",
			},
		})
	}
}

func convertMessages(msgs []openai.ChatCompletionMessage) ([]tools.Message, bool) {
	out := make([]tools.Message, 0, len(msgs))
	for _, m := range msgs {
		content := m.Content
		if content == "" && len(m.MultiContent) > 0 {
			return nil, true
		}
		out = append(out, tools.Message{
			Role:    m.Role,
			Content: content,
		})
	}
	return out, false
}
