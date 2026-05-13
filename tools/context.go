package tools

import (
	"fmt"
	"gateway/config"

	consulapi "github.com/hashicorp/consul/api"
)

type ServiceContext struct {
	Config       config.Config
	ConsulClient *consulapi.Client
}

func NewServiceContext(c config.Config) *ServiceContext {
	// 初始化 Consul 客户端
	consulConfig := consulapi.DefaultConfig()
	consulConfig.Address = c.Consul.Host
	consulClient, _ := consulapi.NewClient(consulConfig)

	// 服务注册：向 Consul 注册网关自身
	registration := &consulapi.AgentServiceRegistration{
		ID:      c.Consul.Key,
		Name:    c.Consul.Key,
		Port:    c.Port,
		Address: c.Host,
		Check: &consulapi.AgentServiceCheck{
			HTTP:     fmt.Sprintf("http://%s:%d/health", c.Host, c.Port),
			Interval: "10s",
			Timeout:  "5s",
		},
	}
	consulClient.Agent().ServiceRegister(registration)

	return &ServiceContext{
		Config:       c,
		ConsulClient: consulClient,
	}
}
