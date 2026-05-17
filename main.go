package main

import (
	"context"
	"fmt"
	"gateway/auth"
	"gateway/config"
	"gateway/handler"
	"gateway/logic"
	"gateway/tools"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

func main() {
	cfg := config.Load()
	serCtx := tools.NewServiceContext(*cfg)
	defer serCtx.Close()

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	router := gin.Default()
	gin.SetMode(cfg.Mode)

	router.Use(func(c *gin.Context) {
		c.Set("svc_ctx", serCtx)
	})

	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	openaiH := handler.NewOpenAIHandler()
	usageH := handler.NewUsageHandler()
	authMW := auth.APIKeyAuth(serCtx.Auth)

	v1 := router.Group("/v1")
	v1.Use(authMW)
	{
		v1.POST("/chat/completions", openaiH.ChatCompletions)
		v1.GET("/usage/summary", usageH.Summary)
	}

	// 兼容旧协议；鉴权：Authorization Bearer 优先，否则 body.api_key
	router.POST("/chat", authMW, func(c *gin.Context) {
		var req tools.Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if key, ok := c.Get("api_key"); ok {
			req.ApiKey = key.(string)
		}
		chatLogic := logic.NewChatLogic(c.Request.Context(), serCtx)
		resp, err := chatLogic.Chat(&req)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, resp)
	})

	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("listening on %s", addr)

	readTO := time.Duration(cfg.Gateway.ReadTimeoutSec) * time.Second
	writeTO := time.Duration(cfg.Gateway.WriteTimeoutSec) * time.Second

	srv := &http.Server{
		Addr:           addr,
		Handler:        router,
		ReadTimeout:    readTO,
		WriteTimeout:   writeTO,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen and serve: %v", err)
		}
	}()

	<-rootCtx.Done()
	shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shCtx); err != nil {
		log.Printf("server shutdown: %v", err)
	}
}
