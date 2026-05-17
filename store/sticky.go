package store

import (
	"context"
	"fmt"
	"time"

	"gateway/config"

	"github.com/redis/go-redis/v9"
)

const stickyKeyPrefix = "gateway:sticky:"

type StickySession struct {
	rdb *redis.Client
	ttl time.Duration
	on  bool
}

func NewStickySession(rdb *redis.Client, cfg config.StickySessionConfig) *StickySession {
	return &StickySession{
		rdb: rdb,
		ttl: time.Duration(cfg.TTLSec) * time.Second,
		on:  cfg.Enabled,
	}
}

func (s *StickySession) key(backend, sessionKey string) string {
	return fmt.Sprintf("%s%s:%s", stickyKeyPrefix, backend, sessionKey)
}

func (s *StickySession) Get(ctx context.Context, backend, sessionKey string) (string, bool) {
	if !s.on || sessionKey == "" {
		return "", false
	}
	url, err := s.rdb.Get(ctx, s.key(backend, sessionKey)).Result()
	if err == redis.Nil {
		return "", false
	}
	if err != nil {
		return "", false
	}
	return url, true
}

func (s *StickySession) Set(ctx context.Context, backend, sessionKey, instanceURL string) {
	if !s.on || sessionKey == "" || instanceURL == "" {
		return
	}
	_ = s.rdb.Set(ctx, s.key(backend, sessionKey), instanceURL, s.ttl).Err()
}

func (s *StickySession) Delete(ctx context.Context, backend, sessionKey string) {
	if !s.on || sessionKey == "" {
		return
	}
	_ = s.rdb.Del(ctx, s.key(backend, sessionKey)).Err()
}
