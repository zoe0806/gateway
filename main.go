package main

import (
	"fmt"
	"gateway/tools"
	"net/http"

	"gateway/config"
	"gateway/logic"

	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/context"
)

func main() {

	cfg := config.Load()
	serCtx := tools.NewServiceContext(*cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	router := gin.Default()
	gin.SetMode(cfg.Mode)

	router.POST("/chat", func(c *gin.Context) {
		var req tools.Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		chatLogic := logic.NewChatLogic(c.Request.Context(), serCtx)
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
	srv := &http.Server{
		Addr:           addr,
		Handler:        router,
		ReadTimeout:    5 * time.Second,
		WriteTimeout:   60 * time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1MB
	}
	for {
		select {
		case <-ctx.Done():
			srv.Shutdown(ctx)
			return
		default:
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				panic(fmt.Errorf("listen and serve: %v", err))
			}
		}
	}
}
