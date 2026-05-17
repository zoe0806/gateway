package tools

import (
	"context"
	"log"

	"gateway/auth"
	"gateway/config"
	"gateway/store"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type ServiceContext struct {
	Config config.Config
	DB     *gorm.DB
	Redis  *redis.Client
	Auth   *auth.Authenticator
	Usage  *store.UsageRecorder
	Sticky *store.StickySession
}

func NewServiceContext(c config.Config) *ServiceContext {
	db, err := store.NewMySQL(c.MySQL)
	if err != nil {
		log.Fatalf("mysql: %v", err)
	}

	rdb, err := store.NewRedis(c.Redis)
	if err != nil {
		log.Fatalf("redis: %v", err)
	}

	authenticator := auth.NewAuthenticator(db, rdb, c.Redis)
	if err := authenticator.WarmCache(context.Background()); err != nil {
		log.Fatalf("auth warm cache: %v", err)
	}

	return &ServiceContext{
		Config: c,
		DB:     db,
		Redis:  rdb,
		Auth:   authenticator,
		Usage:  store.NewUsageRecorder(db),
		Sticky: store.NewStickySession(rdb, c.StickySession),
	}
}

func (s *ServiceContext) Close() {
	if s.Redis != nil {
		_ = s.Redis.Close()
	}
}
