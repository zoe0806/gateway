package model

import "time"

// UsageLog Token 级用量审计（异步写入）。
type UsageLog struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement"`
	ApiKeyHash       string    `gorm:"index;size:64;not null"`
	ClientModel      string    `gorm:"size:128"`
	RoutedModel      string    `gorm:"size:128"`
	Backend          string    `gorm:"size:64;index"`
	InstanceURL      string    `gorm:"size:512"`
	PromptTokens     int       `gorm:"not null;default:0"`
	CompletionTokens int       `gorm:"not null;default:0"`
	TotalTokens      int       `gorm:"not null;default:0"`
	Economy          bool      `gorm:"not null;default:0"`
	Stream           bool      `gorm:"not null;default:0"`
	LatencyMs        int64     `gorm:"not null;default:0"`
	Status           string    `gorm:"size:32;index"` // ok | error
	CreatedAt        time.Time `gorm:"autoCreateTime;index"`
}

func (UsageLog) TableName() string {
	return "usage_logs"
}
