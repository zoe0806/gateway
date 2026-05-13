package logic

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"gateway/tools"

	"github.com/sashabaranov/go-openai"
	"github.com/sony/gobreaker"
	"golang.org/x/time/rate"
)

// SimpleLoadBalancer 轮询负载均衡
type SimpleLoadBalancer struct {
	targets []string
	counter uint64
}

func NewSimpleLoadBalancer(targets []string) *SimpleLoadBalancer {
	return &SimpleLoadBalancer{targets: targets}
}

func (lb *SimpleLoadBalancer) Next() string {
	n := atomic.AddUint64(&lb.counter, 1) % uint64(len(lb.targets))
	return lb.targets[n]
}

type ChatLogic struct {
	ctx        context.Context
	svcCtx     *tools.ServiceContext
	lb         *SimpleLoadBalancer
	limiters   map[string]*rate.Limiter
	breakers   map[string]*gobreaker.CircuitBreaker
	apiKeys    map[string]string
	httpClient *http.Client // 复用 HTTP 客户端
}

func NewChatLogic(ctx context.Context, svcCtx *tools.ServiceContext) *ChatLogic {

	var targets []string
	limiters := make(map[string]*rate.Limiter)
	breakers := make(map[string]*gobreaker.CircuitBreaker)
	apiKeys := make(map[string]string)

	for _, api := range svcCtx.Config.Apis {
		targets = append(targets, api.Url)
		limiters[api.Name] = rate.NewLimiter(10, 20)
		breakers[api.Name] = gobreaker.NewCircuitBreaker(gobreaker.Settings{
			Name:        api.Name,
			MaxRequests: 3,
			Interval:    0,
			Timeout:     10,
		})
		apiKeys[api.Name] = api.ApiKey
	}
	lb := NewSimpleLoadBalancer(targets)
	return &ChatLogic{
		ctx:        ctx,
		svcCtx:     svcCtx,
		lb:         lb,
		limiters:   limiters,
		breakers:   breakers,
		apiKeys:    apiKeys,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (l *ChatLogic) selectBackend(model string) (string, error) {
	switch {
	case strings.Contains(model, "gpt"):
		return "openai", nil
	case strings.Contains(model, "deepseek"):
		return "deepseek", nil
	case strings.Contains(model, "gemini"):
		return "gemini", nil
	default:
		return "", fmt.Errorf("unsupported model: %s", model)
	}
}

// callBackend 发送真实 HTTP 请求，并解析响应为 tools.Response
func (l *ChatLogic) callBackend(backendName, url string, model string, messages []tools.Message) (*tools.Response, error) {
	log.Printf("callBackend: %s, baseURL: %s, model: %s, messages: %+v", backendName, url, model, messages)
	// 获取或创建熔断器
	breaker, ok := l.breakers[backendName]
	if !ok {
		breaker = gobreaker.NewCircuitBreaker(gobreaker.Settings{
			Name:        backendName,
			MaxRequests: 3,
			Interval:    0,
			Timeout:     10,
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
				return counts.Requests >= 3 && failureRatio >= 0.6
			},
		})
		l.breakers[backendName] = breaker
	}
	log.Printf("callBackend breaker: %+v, %t", breaker, ok)
	// 熔断器内执行请求
	result, err := breaker.Execute(func() (interface{}, error) {
		// 创建客户端配置，指向DeepSeek的端点
		config := openai.DefaultConfig(l.apiKeys[backendName])
		config.BaseURL = url

		client := openai.NewClientWithConfig(config)

		// 构建请求参数，与OpenAI完全一致
		req := openai.ChatCompletionRequest{
			Model: model,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    messages[0].Role,
					Content: messages[0].Content,
				},
			},
			Stream:      false,
			MaxTokens:   2048,
			Temperature: 0.7,
		}

		// 发起调用
		ctx := context.Background()
		resp, err := client.CreateChatCompletion(ctx, req)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return nil, err
		}

		// 打印回复内容
		if len(resp.Choices) > 0 {
			fmt.Printf("回复: %s\n", resp.Choices[0].Message.Content)
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
	// 1. 选择后端
	backend, err := l.selectBackend(req.Model)
	if err != nil {
		return nil, err
	}
	log.Printf("selectBackend: %s, req: %+v", backend, req)

	// 2. 限流
	limiter, ok := l.limiters[backend]
	if !ok {
		limiter = rate.NewLimiter(5, 10)
	}
	if !limiter.Allow() {
		// 降级响应
		log.Printf("rate_limit: %s", backend)
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

	// 3. 负载均衡：选取一个实例（简化：同一个 backend 只有一个 baseURL）
	baseURL := l.lb.Next()
	log.Printf("loadBalancer: %s", baseURL)

	// 5. 熔断调用
	resp, err := l.callBackend(backend, baseURL, req.Model, req.Messages)
	if err != nil {
		log.Printf("callBackend failed: %s", err)
		// 可以在这里做降级或记录错误
		return nil, fmt.Errorf("backend call failed: %w", err)
	}

	// 6. 记录审计（可改用结构化日志）
	// 注意：因为 resp 是指针，这里打印可能导致数据较大，生产环境请谨慎
	log.Printf("Chat: backend=%s, baseURL=%s, req_model=%s, resp_id=%s", backend, baseURL, req.Model, resp.Id)

	return resp, nil
}
