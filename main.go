package main

import (
	"fmt"
	"gateway/tools"
	"log"
	"net/http"

	"gateway/config"
	"gateway/logic"

	"github.com/gin-gonic/gin"
)

func main() {

	cfg := config.Load()
	ctx := tools.NewServiceContext(*cfg)

	router := gin.Default()
	gin.SetMode(cfg.Mode)

	router.POST("/chat", func(c *gin.Context) {
		var req tools.Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		chatLogic := logic.NewChatLogic(c.Request.Context(), ctx)
		resp, err := chatLogic.Chat(&req)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, resp)
	})

	// 假设配置的端口为 8080
	addr := fmt.Sprintf(":%d", cfg.Port)
	fmt.Printf("Starting server on %s\n", addr)
	if err := router.Run(addr); err != nil {
		log.Fatal(err)
	}
}
