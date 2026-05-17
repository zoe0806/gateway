package model

import "time"

// APIKey 平台颁发的访问密钥（与上游 provider key 无关）。
type APIKey struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement"`
	ApiKey    string    `gorm:"column:api_key;uniqueIndex;size:128;not null"`
	Name      string    `gorm:"size:64"`
	Status    int8      `gorm:"not null;default:1"` // 1=启用 0=禁用
	CreatedAt time.Time `gorm:"autoCreateTime"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"`
}

func (APIKey) TableName() string {
	return "api_keys"
}
