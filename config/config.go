package config

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

type Config struct {
	Port           int                  `json:"port"`
	Mode           string               `json:"mode"`
	Apis           []Api                `json:"apis"`
	Routing        RoutingConfig        `json:"routing"`
	PromptCompress PromptCompressConfig `json:"prompt_compress"`
	MySQL          MySQLConfig          `json:"mysql"`
	Redis          RedisConfig          `json:"redis"`
	Gateway        GatewayConfig        `json:"gateway"`
}

type MySQLConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Database string `json:"database"`
}

type RedisConfig struct {
	Addr             string `json:"addr"`
	Password         string `json:"password"`
	DB               int    `json:"db"`
	APIKeyCacheTTL   int    `json:"api_key_cache_ttl_sec"`
}

type GatewayConfig struct {
	ReadTimeoutSec     int `json:"read_timeout_sec"`
	WriteTimeoutSec    int `json:"write_timeout_sec"`
	UpstreamTimeoutSec int `json:"upstream_timeout_sec"`
	MaxBodyBytes       int `json:"max_body_bytes"`
	FailoverMaxTries   int `json:"failover_max_tries"`
}

// RoutingConfig 智能模型路由：简单对话走经济模型，复杂任务保留客户端指定模型。
type RoutingConfig struct {
	Enabled           bool     `json:"enabled"`
	EconomyModel      string   `json:"economy_model"`
	EconomyBackend    string   `json:"economy_backend"`
	MaxCharsSimple    int      `json:"max_chars_simple"`
	MaxMessagesSimple int      `json:"max_messages_simple"`
	ComplexKeywords   []string `json:"complex_keywords"`
}

// PromptCompressConfig 在发往上游前压缩上下文，降低 token 成本。
type PromptCompressConfig struct {
	Enabled            bool `json:"enabled"`
	MaxTotalChars      int  `json:"max_total_chars"`
	MaxMessageChars    int  `json:"max_message_chars"`
	CollapseBlankLines bool `json:"collapse_blank_lines"`
}

type Api struct {
	Name   string   `json:"name"`
	Url    string   `json:"url"`
	Urls   []string `json:"urls,omitempty"`
	ApiKey string   `json:"api_key"`
}

var config Config
var once sync.Once

func init() {
	once.Do(func() {
		dir, err := os.Getwd()
		if err != nil {
			log.Fatalf("get current directory: %v", err)
		}
		cfgPath := filepath.Join(dir, "config.json")
		log.Printf("config file path: %s", cfgPath)
		cfg, err := os.ReadFile(cfgPath)
		if err != nil {
			log.Fatalf("read config file: %v", err)
		}
		err = json.Unmarshal(cfg, &config)
		if err != nil {
			log.Fatalf("unmarshal config: %v", err)
		}
		applyDefaults(&config)
	})
}

func applyDefaults(c *Config) {
	if c.MySQL.Host == "" {
		c.MySQL.Host = "127.0.0.1"
	}
	if c.MySQL.Port == 0 {
		c.MySQL.Port = 3306
	}
	if c.MySQL.User == "" {
		c.MySQL.User = "root"
	}
	if c.MySQL.Database == "" {
		c.MySQL.Database = "test"
	}
	if c.Redis.Addr == "" {
		c.Redis.Addr = "127.0.0.1:6379"
	}
	if c.Redis.APIKeyCacheTTL == 0 {
		c.Redis.APIKeyCacheTTL = 300
	}
	if c.Gateway.ReadTimeoutSec == 0 {
		c.Gateway.ReadTimeoutSec = 30
	}
	if c.Gateway.WriteTimeoutSec == 0 {
		c.Gateway.WriteTimeoutSec = 300
	}
	if c.Gateway.UpstreamTimeoutSec == 0 {
		c.Gateway.UpstreamTimeoutSec = 180
	}
	if c.Gateway.MaxBodyBytes == 0 {
		c.Gateway.MaxBodyBytes = 10 << 20
	}
	if c.Gateway.FailoverMaxTries == 0 {
		c.Gateway.FailoverMaxTries = 3
	}
}

func Load() *Config {
	return &config
}
