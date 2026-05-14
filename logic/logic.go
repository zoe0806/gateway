package logic

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"gateway/config"
	"gateway/tools"

	"github.com/sashabaranov/go-openai"
	"github.com/sony/gobreaker"
	"golang.org/x/time/rate"
)

var DefaultTransport = &http.Transport{
	MaxIdleConns:        100,
	MaxIdleConnsPerHost: 10,
	IdleConnTimeout:     90 * time.Second,
	DisableKeepAlives:   false,
}

// SimpleLoadBalancer 同一后端多实例时按轮询选取。
type SimpleLoadBalancer struct {
	instances []config.Api
	counter   uint64
}

func NewSimpleLoadBalancer(instances []config.Api) *SimpleLoadBalancer {
	return &SimpleLoadBalancer{instances: instances}
}

func (lb *SimpleLoadBalancer) Next() config.Api {
	if len(lb.instances) == 0 {
		return config.Api{}
	}
	n := atomic.AddUint64(&lb.counter, 1) % uint64(len(lb.instances))
	return lb.instances[n]
}

func (lb *SimpleLoadBalancer) Len() int {
	if lb == nil {
		return 0
	}
	return len(lb.instances)
}

type ChatLogic struct {
	ctx        context.Context
	svcCtx     *tools.ServiceContext
	lbByName   map[string]*SimpleLoadBalancer
	limiters   map[string]*rate.Limiter
	breakers   map[string]*gobreaker.CircuitBreaker // key: backend|url
	httpClient *http.Client
}

func expandAPISpecs(apis []config.Api) []config.Api {
	var out []config.Api
	for _, a := range apis {
		if len(a.Urls) > 0 {
			for _, u := range a.Urls {
				u = strings.TrimSpace(u)
				if u == "" {
					continue
				}
				one := a
				one.Url = u
				one.Urls = nil
				out = append(out, one)
			}
			continue
		}
		if strings.TrimSpace(a.Url) != "" {
			one := a
			one.Urls = nil
			out = append(out, one)
		}
	}
	return out
}

func groupApisByName(instances []config.Api) map[string][]config.Api {
	m := make(map[string][]config.Api)
	for _, a := range instances {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			continue
		}
		m[name] = append(m[name], a)
	}
	return m
}

func circuitKey(backendName, url string) string {
	return backendName + "|" + url
}

func NewChatLogic(ctx context.Context, svcCtx *tools.ServiceContext) *ChatLogic {
	flat := expandAPISpecs(svcCtx.Config.Apis)
	grouped := groupApisByName(flat)

	limiters := make(map[string]*rate.Limiter)
	breakers := make(map[string]*gobreaker.CircuitBreaker)
	lbByName := make(map[string]*SimpleLoadBalancer)

	for name, insts := range grouped {
		lbByName[name] = NewSimpleLoadBalancer(insts)
		limiters[name] = rate.NewLimiter(10, 20)
		for _, inst := range insts {
			key := circuitKey(name, inst.Url)
			if _, ok := breakers[key]; ok {
				continue
			}
			breakers[key] = gobreaker.NewCircuitBreaker(gobreaker.Settings{
				Name:        key,
				MaxRequests: 5,
				Interval:    0,
				Timeout:     10 * time.Second,
			})
		}
	}

	return &ChatLogic{
		ctx:        ctx,
		svcCtx:     svcCtx,
		lbByName:   lbByName,
		limiters:   limiters,
		breakers:   breakers,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (l *ChatLogic) hasBackend(name string) bool {
	lb, ok := l.lbByName[name]
	return ok && lb != nil && lb.Len() > 0
}

func (l *ChatLogic) selectBackend(model string) (string, error) {
	switch {
	case strings.Contains(strings.ToLower(model), "gpt"):
		return "openai", nil
	case strings.Contains(strings.ToLower(model), "deepseek"):
		return "deepseek", nil
	case strings.Contains(strings.ToLower(model), "gemini"):
		return "gemini", nil
	default:
		return "", fmt.Errorf("unsupported model: %s", model)
	}
}

func (l *ChatLogic) callBackend(backendName, model string, messages []tools.Message) (*tools.Response, error) {
	lb, ok := l.lbByName[backendName]
	if !ok || lb.Len() == 0 {
		return nil, fmt.Errorf("backend %q not configured", backendName)
	}

	apiCfg := lb.Next()
	if strings.TrimSpace(apiCfg.Url) == "" {
		return nil, fmt.Errorf("backend %q has no valid url", backendName)
	}

	chars := 0
	for _, m := range messages {
		chars += len(m.Content)
	}
	log.Printf("callBackend: backend=%s url=%s model=%s messages=%d approxChars=%d", backendName, apiCfg.Url, model, len(messages), chars)

	bKey := circuitKey(backendName, apiCfg.Url)
	breaker, ok := l.breakers[bKey]
	if !ok {
		breaker = gobreaker.NewCircuitBreaker(gobreaker.Settings{
			Name:        bKey,
			MaxRequests: 5,
			Interval:    0,
			Timeout:     10 * time.Second,
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				if counts.Requests == 0 {
					return false
				}
				failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
				return counts.Requests >= 5 && failureRatio >= 0.6
			},
		})
		l.breakers[bKey] = breaker
	}

	result, err := breaker.Execute(func() (interface{}, error) {
		cfg := openai.DefaultConfig(apiCfg.ApiKey)
		cfg.BaseURL = apiCfg.Url
		cfg.HTTPClient = &http.Client{
			Transport: DefaultTransport,
			Timeout:   60 * time.Second,
		}
		client := openai.NewClientWithConfig(cfg)

		oMsgs := make([]openai.ChatCompletionMessage, 0, len(messages))
		for _, m := range messages {
			oMsgs = append(oMsgs, openai.ChatCompletionMessage{
				Role:    m.Role,
				Content: m.Content,
			})
		}

		req := openai.ChatCompletionRequest{
			Model:       model,
			Messages:    oMsgs,
			Stream:      false,
			MaxTokens:   2048,
			Temperature: 0.7,
		}

		ctx := context.Background()
		resp, err := client.CreateChatCompletion(ctx, req)
		if err != nil {
			return nil, err
		}

		if len(resp.Choices) == 0 {
			return nil, fmt.Errorf("empty choices from upstream")
		}

		return &tools.Response{
			Id:     resp.ID,
			Object: resp.Object,
			Choices: []tools.Choice{
				{
					Index: 0,
					Message: tools.Message{
						Role:    resp.Choices[0].Message.Role,
						Content: resp.Choices[0].Message.Content,
					},
					FinishReason: "done",
				},
			},
		}, nil
	})

	if err != nil {
		return nil, err
	}
	return result.(*tools.Response), nil
}

func (l *ChatLogic) Chat(req *tools.Request) (*tools.Response, error) {
	route, err := l.resolveRoute(req)
	if err != nil {
		return nil, err
	}

	pc := l.effectivePromptCompress()
	msgs := CompressMessages(req.Messages, pc)

	limiter, ok := l.limiters[route.Backend]
	if !ok {
		limiter = rate.NewLimiter(5, 10)
	}
	if !limiter.Allow() {
		log.Printf("rate_limit: %s", route.Backend)
		return &tools.Response{
			Id:     "fallback",
			Object: "error",
			Choices: []tools.Choice{
				{
					Index: 0,
					Message: tools.Message{
						Role:    "assistant",
						Content: "当前请求过多，请稍后再试。",
					},
					FinishReason: "rate_limit",
				},
			},
		}, nil
	}

	resp, err := l.callBackend(route.Backend, route.Model, msgs)
	if err != nil {
		log.Printf("callBackend failed: %s", err)
		return nil, fmt.Errorf("backend call failed: %w", err)
	}

	log.Printf("Chat: backend=%s economy=%t routed_model=%s client_model=%s resp_id=%s", route.Backend, route.Economy, route.Model, req.Model, resp.Id)

	return resp, nil
}
