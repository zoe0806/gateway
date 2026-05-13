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
	Port int    `json:"port"`
	Host string `json:"host"`
	Mode string `json:"mode"`
	Apis []Api  `json:"apis"`
}

type Api struct {
	Name   string `json:"name"`
	Url    string `json:"url"`
	ApiKey string `json:"api_key"`
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
