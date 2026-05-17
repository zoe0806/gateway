package logic

import (
	"fmt"
	"sync"

	"gateway/config"
)

type ConcurrencyGate struct {
	enabled   bool
	global    chan struct{}
	perKeyMax int
	perKey    sync.Map // apiKey -> *keySlot
}

type keySlot struct {
	ch chan struct{}
}

func NewConcurrencyGate(cfg config.ConcurrencyConfig) *ConcurrencyGate {
	g := &ConcurrencyGate{
		enabled:   cfg.Enabled,
		perKeyMax: cfg.PerAPIKeyMax,
	}
	if cfg.GlobalMax > 0 {
		g.global = make(chan struct{}, cfg.GlobalMax)
	}
	return g
}

func (g *ConcurrencyGate) Acquire(apiKey string) (release func(), err error) {
	if g == nil || !g.enabled {
		return func() {}, nil
	}
	if g.global != nil {
		select {
		case g.global <- struct{}{}:
		default:
			return nil, fmt.Errorf("gateway global concurrency limit exceeded")
		}
	}

	var keyRelease func()
	if apiKey != "" && g.perKeyMax > 0 {
		slot := g.slotFor(apiKey)
		select {
		case slot.ch <- struct{}{}:
		default:
			if g.global != nil {
				<-g.global
			}
			return nil, fmt.Errorf("api key concurrency limit exceeded")
		}
		keyRelease = func() { <-slot.ch }
	}

	return func() {
		if keyRelease != nil {
			keyRelease()
		}
		if g.global != nil {
			<-g.global
		}
	}, nil
}

func (g *ConcurrencyGate) slotFor(apiKey string) *keySlot {
	if v, ok := g.perKey.Load(apiKey); ok {
		return v.(*keySlot)
	}
	slot := &keySlot{ch: make(chan struct{}, g.perKeyMax)}
	actual, _ := g.perKey.LoadOrStore(apiKey, slot)
	return actual.(*keySlot)
}
