package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"time"

	"gateway/model"

	"gorm.io/gorm"
)

type UsageRecord struct {
	ApiKey           string
	ClientModel      string
	RoutedModel      string
	Backend          string
	InstanceURL      string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Economy          bool
	Stream           bool
	LatencyMs        int64
	Status           string
}

type UsageRecorder struct {
	db *gorm.DB
}

func NewUsageRecorder(db *gorm.DB) *UsageRecorder {
	return &UsageRecorder{db: db}
}

func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:16])
}

func (r *UsageRecorder) Record(ctx context.Context, rec UsageRecord) {
	if r == nil || r.db == nil {
		return
	}
	go func() {
		row := model.UsageLog{
			ApiKeyHash:       HashAPIKey(rec.ApiKey),
			ClientModel:      rec.ClientModel,
			RoutedModel:      rec.RoutedModel,
			Backend:          rec.Backend,
			InstanceURL:      rec.InstanceURL,
			PromptTokens:     rec.PromptTokens,
			CompletionTokens: rec.CompletionTokens,
			TotalTokens:      rec.TotalTokens,
			Economy:          rec.Economy,
			Stream:           rec.Stream,
			LatencyMs:        rec.LatencyMs,
			Status:           rec.Status,
		}
		if row.Status == "" {
			row.Status = "ok"
		}
		if row.TotalTokens == 0 {
			row.TotalTokens = row.PromptTokens + row.CompletionTokens
		}
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.db.WithContext(c).Create(&row).Error; err != nil {
			log.Printf("usage: insert failed: %v", err)
		}
	}()
}

type UsageSummary struct {
	TotalRequests      int64 `json:"total_requests"`
	TotalTokens        int64 `json:"total_tokens"`
	PromptTokens       int64 `json:"prompt_tokens"`
	CompletionTokens   int64 `json:"completion_tokens"`
	EconomyRequests    int64 `json:"economy_requests"`
}

func (r *UsageRecorder) SummarySince(ctx context.Context, apiKeyHash string, since time.Time) (UsageSummary, error) {
	var s UsageSummary
	base := r.db.WithContext(ctx).Model(&model.UsageLog{}).Where("created_at >= ?", since)
	if apiKeyHash != "" {
		base = base.Where("api_key_hash = ?", apiKeyHash)
	}
	if err := base.Count(&s.TotalRequests).Error; err != nil {
		return s, err
	}
	type agg struct {
		Prompt   int64
		Complete int64
		Total    int64
		Economy  int64
	}
	var a agg
	sumQ := r.db.WithContext(ctx).Model(&model.UsageLog{}).Where("created_at >= ?", since)
	if apiKeyHash != "" {
		sumQ = sumQ.Where("api_key_hash = ?", apiKeyHash)
	}
	if err := sumQ.Select(
		"COALESCE(SUM(prompt_tokens),0) as prompt",
		"COALESCE(SUM(completion_tokens),0) as complete",
		"COALESCE(SUM(total_tokens),0) as total",
		"COALESCE(SUM(CASE WHEN economy = 1 THEN 1 ELSE 0 END),0) as economy",
	).Scan(&a).Error; err != nil {
		return s, err
	}
	s.PromptTokens = a.Prompt
	s.CompletionTokens = a.Complete
	s.TotalTokens = a.Total
	s.EconomyRequests = a.Economy
	return s, nil
}
