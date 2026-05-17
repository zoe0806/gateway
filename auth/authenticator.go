package auth

import (
	"context"
	"fmt"
	"log"
	"time"

	"gateway/config"
	"gateway/model"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const redisKeyPrefix = "gateway:apikey:"

type Authenticator struct {
	db    *gorm.DB
	redis *redis.Client
	ttl   time.Duration
}

func NewAuthenticator(db *gorm.DB, rdb *redis.Client, cfg config.RedisConfig) *Authenticator {
	return &Authenticator{
		db:    db,
		redis: rdb,
		ttl:   time.Duration(cfg.APIKeyCacheTTL) * time.Second,
	}
}

// WarmCache 启动时将启用中的 key 写入 Redis，减少冷启动鉴权穿透。
func (a *Authenticator) WarmCache(ctx context.Context) error {
	var keys []model.APIKey
	if err := a.db.WithContext(ctx).Where("status = ?", 1).Find(&keys).Error; err != nil {
		return err
	}
	for _, k := range keys {
		if err := a.redis.Set(ctx, redisKey(k.ApiKey), "1", a.ttl).Err(); err != nil {
			return err
		}
	}
	log.Printf("auth: warmed %d api key(s) to redis", len(keys))
	return nil
}

func (a *Authenticator) Validate(ctx context.Context, apiKey string) bool {
	if apiKey == "" {
		return false
	}

	val, err := a.redis.Get(ctx, redisKey(apiKey)).Result()
	if err == nil && val == "1" {
		return true
	}
	if err != nil && err != redis.Nil {
		log.Printf("auth: redis get: %v", err)
	}

	var row model.APIKey
	err = a.db.WithContext(ctx).Where("api_key = ? AND status = ?", apiKey, 1).First(&row).Error
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			log.Printf("auth: mysql lookup: %v", err)
		}
		return false
	}

	if setErr := a.redis.Set(ctx, redisKey(apiKey), "1", a.ttl).Err(); setErr != nil {
		log.Printf("auth: redis set: %v", setErr)
	}
	return true
}

// Invalidate 禁用或轮换 key 时调用。
func (a *Authenticator) Invalidate(ctx context.Context, apiKey string) error {
	return a.redis.Del(ctx, redisKey(apiKey)).Err()
}

func redisKey(apiKey string) string {
	return fmt.Sprintf("%s%s", redisKeyPrefix, apiKey)
}
