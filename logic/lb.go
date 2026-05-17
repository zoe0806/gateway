package logic

import (
	"context"
	"strings"
	"sync/atomic"

	"gateway/config"
	"gateway/store"
)

type SimpleLoadBalancer struct {
	instances []config.Api
	counter   uint64
}

func NewSimpleLoadBalancer(instances []config.Api) *SimpleLoadBalancer {
	return &SimpleLoadBalancer{instances: instances}
}

func (lb *SimpleLoadBalancer) Len() int {
	if lb == nil {
		return 0
	}
	return len(lb.instances)
}

func (lb *SimpleLoadBalancer) Next() config.Api {
	if len(lb.instances) == 0 {
		return config.Api{}
	}
	n := atomic.AddUint64(&lb.counter, 1) % uint64(len(lb.instances))
	return lb.instances[n]
}

func (lb *SimpleLoadBalancer) FindByURL(url string) (config.Api, bool) {
	for _, inst := range lb.instances {
		if inst.Url == url {
			return inst, true
		}
	}
	return config.Api{}, false
}

// Pick 粘性会话优先，否则轮询；成功选中后写入 Redis。
func (lb *SimpleLoadBalancer) Pick(ctx context.Context, sticky *store.StickySession, backend, sessionKey string) config.Api {
	if lb.Len() == 0 {
		return config.Api{}
	}
	if sessionKey != "" && sticky != nil {
		if url, ok := sticky.Get(ctx, backend, sessionKey); ok {
			if inst, found := lb.FindByURL(url); found {
				return inst
			}
			sticky.Delete(ctx, backend, sessionKey)
		}
	}
	inst := lb.Next()
	if sessionKey != "" && sticky != nil && strings.TrimSpace(inst.Url) != "" {
		sticky.Set(ctx, backend, sessionKey, inst.Url)
	}
	return inst
}
