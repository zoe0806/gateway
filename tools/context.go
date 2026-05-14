package tools

import (
	"fmt"
	"log"
	"os"
	"strings"

	"gateway/config"

	consulapi "github.com/hashicorp/consul/api"
)

type ServiceContext struct {
	Config       config.Config
	ConsulClient *consulapi.Client
	consulSvcID  string // 非空表示已成功向本机 Agent 注册，退出时应注销
}

func sanitizeConsulIDPart(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			return r
		default:
			return '-'
		}
	}, s)
}

func NewServiceContext(c config.Config) *ServiceContext {
	consulConfig := consulapi.DefaultConfig()
	consulConfig.Address = c.Consul.Host

	consulClient, err := consulapi.NewClient(consulConfig)
	if err != nil {
		log.Printf("consul: new client: %v (registration disabled)", err)
		return &ServiceContext{Config: c, ConsulClient: nil}
	}

	host, _ := os.Hostname()
	svcID := fmt.Sprintf("%s-%s-%d", sanitizeConsulIDPart(c.Consul.Key), sanitizeConsulIDPart(host), c.Port)

	registration := &consulapi.AgentServiceRegistration{
		ID:      svcID,
		Name:    c.Consul.Key,
		Port:    c.Port,
		Address: c.Host,
		Check: &consulapi.AgentServiceCheck{
			HTTP:     fmt.Sprintf("http://%s:%d/health", c.Host, c.Port),
			Interval: "10s",
			Timeout:  "5s",
		},
	}
	if err := consulClient.Agent().ServiceRegister(registration); err != nil {
		log.Printf("consul: service register %q: %v", svcID, err)
		svcID = ""
	} else {
		log.Printf("consul: registered service id=%q name=%q", svcID, c.Consul.Key)
	}

	return &ServiceContext{
		Config:       c,
		ConsulClient: consulClient,
		consulSvcID:  svcID,
	}
}

// DeregisterFromConsul 在进程退出前注销，避免 Consul 中长期残留不健康实例。
func (s *ServiceContext) DeregisterFromConsul() {
	if s == nil || s.ConsulClient == nil || s.consulSvcID == "" {
		return
	}
	if err := s.ConsulClient.Agent().ServiceDeregister(s.consulSvcID); err != nil {
		log.Printf("consul: service deregister %q: %v", s.consulSvcID, err)
		return
	}
	log.Printf("consul: deregistered service id=%q", s.consulSvcID)
}
