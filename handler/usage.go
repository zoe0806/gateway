package handler

import (
	"net/http"
	"strconv"
	"time"

	"gateway/store"
	"gateway/tools"

	"github.com/gin-gonic/gin"
)

type UsageHandler struct{}

func NewUsageHandler() *UsageHandler {
	return &UsageHandler{}
}

// Summary GET /usage/summary?days=7  返回当前 API Key 的用量汇总（供 HTML/JS 调用）。
func (h *UsageHandler) Summary(c *gin.Context) {
	serCtx := c.MustGet("svc_ctx").(*tools.ServiceContext)
	apiKey, _ := c.Get("api_key")
	keyStr, _ := apiKey.(string)

	days := 7
	if d := c.Query("days"); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n > 0 && n <= 90 {
			days = n
		}
	}
	since := time.Now().AddDate(0, 0, -days)
	hash := store.HashAPIKey(keyStr)

	summary, err := serCtx.Usage.SummarySince(c.Request.Context(), hash, since)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"days":    days,
		"since":   since.Format(time.RFC3339),
		"summary": summary,
	})
}
