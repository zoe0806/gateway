package store

import (
	"fmt"
	"log"

	"gateway/config"
	"gateway/model"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func NewMySQL(cfg config.MySQLConfig) (*gorm.DB, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database)
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&model.APIKey{}); err != nil {
		return nil, err
	}
	if err := seedAPIKeys(db); err != nil {
		return nil, err
	}
	return db, nil
}

func seedAPIKeys(db *gorm.DB) error {
	var n int64
	if err := db.Model(&model.APIKey{}).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	keys := []model.APIKey{
		{ApiKey: "sk-proj-1234567890", Name: "default", Status: 1},
	}
	if err := db.Create(&keys).Error; err != nil {
		return err
	}
	log.Printf("mysql: seeded %d api key(s)", len(keys))
	return nil
}
