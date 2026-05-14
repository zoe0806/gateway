package config

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

type Config struct {
	Consul struct {
		Host string `json:"host"`
		Key  string `json:"key"`
	} `json:"consul"`
	Port           int                 `json:"port"`
	Host           string              `json:"host"`
	Mode           string              `json:"mode"`
	Apis           []Api               `json:"apis"`
	Routing        RoutingConfig       `json:"routing"`
	PromptCompress PromptCompressConfig `json:"prompt_compress"`
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
	Urls   []string `json:"urls,omitempty"` // 与 url 二选一或并存：展开为多个实例（同一 name 多地址）
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
	})
}

func Load() *Config {
	return &config
}
