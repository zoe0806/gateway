package logic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
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
	ctx              context.Context
	svcCtx           *tools.ServiceContext
	lbByName         map[string]*SimpleLoadBalancer
	limiters         map[string]*rate.Limiter
	breakers         map[string]*gobreaker.CircuitBreaker
	upstreamTimeout  time.Duration
	failoverMaxTries int
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

	maxTry := svcCtx.Config.Gateway.FailoverMaxTries
	if maxTry < 1 {
		maxTry = 1
	}

	return &ChatLogic{
		ctx:              ctx,
		svcCtx:           svcCtx,
		lbByName:         lbByName,
		limiters:         limiters,
		breakers:         breakers,
		upstreamTimeout:  time.Duration(svcCtx.Config.Gateway.UpstreamTimeoutSec) * time.Second,
		failoverMaxTries: maxTry,
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

func (l *ChatLogic) getBreaker(backendName string, apiCfg config.Api) *gobreaker.CircuitBreaker {
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
	return breaker
}

func (l *ChatLogic) newUpstreamClient(apiCfg config.Api) *openai.Client {
	cfg := openai.DefaultConfig(apiCfg.ApiKey)
	cfg.BaseURL = apiCfg.Url
	cfg.HTTPClient = &http.Client{
		Transport: DefaultTransport,
		Timeout:   l.upstreamTimeout,
	}
	return openai.NewClientWithConfig(cfg)
}

// ChatParams 网关内部统一聊天参数（OpenAI 与旧 /chat 共用）。
type ChatParams struct {
	Model       string
	Messages    []tools.Message
	RoutingMode string
	Stream      bool
	MaxTokens   int
	Temperature float32
}

func (p ChatParams) toRequest() *tools.Request {
	return &tools.Request{
		Model:       p.Model,
		Messages:    p.Messages,
		RoutingMode: p.RoutingMode,
	}
}

func (l *ChatLogic) buildOpenAIRequest(params ChatParams, model string, messages []tools.Message) openai.ChatCompletionRequest {
	maxTokens := params.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 2048
	}
	temp := params.Temperature
	if temp == 0 {
		temp = 0.7
	}
	oMsgs := make([]openai.ChatCompletionMessage, 0, len(messages))
	for _, m := range messages {
		oMsgs = append(oMsgs, openai.ChatCompletionMessage{
			Role:    m.Role,
			Content: m.Content,
		})
	}
	return openai.ChatCompletionRequest{
		Model:       model,
		Messages:    oMsgs,
		Stream:      params.Stream,
		MaxTokens:   maxTokens,
		Temperature: temp,
	}
}

func (l *ChatLogic) callInstance(
	ctx context.Context,
	backendName string,
	apiCfg config.Api,
	params ChatParams,
	model string,
	messages []tools.Message,
) (*openai.ChatCompletionResponse, error) {
	if strings.TrimSpace(apiCfg.Url) == "" {
		return nil, fmt.Errorf("backend %q has empty url", backendName)
	}

	chars := 0
	for _, m := range messages {
		chars += len(m.Content)
	}
	log.Printf("callBackend: backend=%s url=%s model=%s stream=%t messages=%d approxChars=%d",
		backendName, apiCfg.Url, model, params.Stream, len(messages), chars)

	breaker := l.getBreaker(backendName, apiCfg)
	result, err := breaker.Execute(func() (interface{}, error) {
		client := l.newUpstreamClient(apiCfg)
		req := l.buildOpenAIRequest(params, model, messages)
		req.Stream = false
		resp, err := client.CreateChatCompletion(ctx, req)
		if err != nil {
			return nil, err
		}
		if len(resp.Choices) == 0 {
			return nil, fmt.Errorf("empty choices from upstream")
		}
		return &resp, nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*openai.ChatCompletionResponse), nil
}

func (l *ChatLogic) streamInstance(
	ctx context.Context,
	backendName string,
	apiCfg config.Api,
	params ChatParams,
	model string,
	messages []tools.Message,
	w http.ResponseWriter,
) error {
	if strings.TrimSpace(apiCfg.Url) == "" {
		return fmt.Errorf("backend %q has empty url", backendName)
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	breaker := l.getBreaker(backendName, apiCfg)
	_, err := breaker.Execute(func() (interface{}, error) {
		client := l.newUpstreamClient(apiCfg)
		req := l.buildOpenAIRequest(params, model, messages)
		req.Stream = true

		stream, err := client.CreateChatCompletionStream(ctx, req)
		if err != nil {
			return nil, err
		}
		defer stream.Close()

		for {
			chunk, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, err
			}
			data, err := json.Marshal(chunk)
			if err != nil {
				return nil, err
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return nil, err
			}
			flusher.Flush()
		}
		if _, err := fmt.Fprintf(w, "data: [DONE]\n\n"); err != nil {
			return nil, err
		}
		flusher.Flush()
		return nil, nil
	})
	return err
}

func (l *ChatLogic) callBackendWithFailover(
	ctx context.Context,
	backendName string,
	params ChatParams,
	model string,
	messages []tools.Message,
) (*openai.ChatCompletionResponse, error) {
	lb, ok := l.lbByName[backendName]
	if !ok || lb.Len() == 0 {
		return nil, fmt.Errorf("backend %q not configured", backendName)
	}

	tries := l.failoverMaxTries
	if tries > lb.Len() {
		tries = lb.Len()
	}

	var lastErr error
	for i := 0; i < tries; i++ {
		apiCfg := lb.Next()
		resp, err := l.callInstance(ctx, backendName, apiCfg, params, model, messages)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryableUpstreamErr(err) {
			break
		}
		log.Printf("failover: backend=%s try=%d/%d err=%v", backendName, i+1, tries, err)
	}
	return nil, lastErr
}

func (l *ChatLogic) streamBackendWithFailover(
	ctx context.Context,
	backendName string,
	params ChatParams,
	model string,
	messages []tools.Message,
	w http.ResponseWriter,
) error {
	lb, ok := l.lbByName[backendName]
	if !ok || lb.Len() == 0 {
		return fmt.Errorf("backend %q not configured", backendName)
	}

	tries := l.failoverMaxTries
	if tries > lb.Len() {
		tries = lb.Len()
	}

	var lastErr error
	for i := 0; i < tries; i++ {
		apiCfg := lb.Next()
		err := l.streamInstance(ctx, backendName, apiCfg, params, model, messages, w)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRetryableUpstreamErr(err) {
			break
		}
		log.Printf("stream failover: backend=%s try=%d/%d err=%v", backendName, i+1, tries, err)
	}
	return lastErr
}

func (l *ChatLogic) prepareChat(params ChatParams) (routeDecision, []tools.Message, error) {
	route, err := l.resolveRoute(params.toRequest())
	if err != nil {
		return routeDecision{}, nil, err
	}
	pc := l.effectivePromptCompress()
	msgs := CompressMessages(params.Messages, pc)
	return route, msgs, nil
}

func (l *ChatLogic) checkRateLimit(route routeDecision) error {
	limiter, ok := l.limiters[route.Backend]
	if !ok {
		limiter = rate.NewLimiter(5, 10)
	}
	if !limiter.Allow() {
		log.Printf("rate_limit: %s", route.Backend)
		return fmt.Errorf("rate limit exceeded on backend %s", route.Backend)
	}
	return nil
}

// ChatCompletion OpenAI 兼容入口：支持流式与非流式。
func (l *ChatLogic) ChatCompletion(ctx context.Context, w http.ResponseWriter, params ChatParams) error {
	route, msgs, err := l.prepareChat(params)
	if err != nil {
		return err
	}
	if err := l.checkRateLimit(route); err != nil {
		return err
	}

	if params.Stream {
		err = l.streamBackendWithFailover(ctx, route.Backend, params, route.Model, msgs, w)
	} else {
		var resp *openai.ChatCompletionResponse
		resp, err = l.callBackendWithFailover(ctx, route.Backend, params, route.Model, msgs)
		if err == nil {
			w.Header().Set("Content-Type", "application/json")
			err = json.NewEncoder(w).Encode(resp)
		}
	}
	if err != nil {
		log.Printf("ChatCompletion failed: %v", err)
		return fmt.Errorf("backend call failed: %w", err)
	}

	log.Printf("Chat: backend=%s economy=%t routed_model=%s client_model=%s stream=%t",
		route.Backend, route.Economy, route.Model, params.Model, params.Stream)
	return nil
}

// Chat 保留旧 JSON 协议（非流式）。
func (l *ChatLogic) Chat(req *tools.Request) (*tools.Response, error) {
	params := ChatParams{
		Model:       req.Model,
		Messages:    req.Messages,
		RoutingMode: req.RoutingMode,
		Stream:      false,
	}
	route, msgs, err := l.prepareChat(params)
	if err != nil {
		return nil, err
	}
	if err := l.checkRateLimit(route); err != nil {
		return &tools.Response{
			Id:     "fallback",
			Object: "error",
			Choices: []tools.Choice{{
				Index:        0,
				Message:      tools.Message{Role: "assistant", Content: "当前请求过多，请稍后再试。"},
				FinishReason: "rate_limit",
			}},
		}, nil
	}

	ctx := l.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := l.callBackendWithFailover(ctx, route.Backend, params, route.Model, msgs)
	if err != nil {
		return nil, fmt.Errorf("backend call failed: %w", err)
	}

	out := &tools.Response{
		Id:     resp.ID,
		Object: resp.Object,
		Choices: []tools.Choice{{
			Index: 0,
			Message: tools.Message{
				Role:    resp.Choices[0].Message.Role,
				Content: resp.Choices[0].Message.Content,
			},
			FinishReason: string(resp.Choices[0].FinishReason),
		}},
	}
	log.Printf("Chat: backend=%s economy=%t routed_model=%s client_model=%s resp_id=%s",
		route.Backend, route.Economy, route.Model, req.Model, out.Id)
	return out, nil
}

func isRetryableUpstreamErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "429") ||
		strings.Contains(msg, "500") ||
		strings.Contains(msg, "502") ||
		strings.Contains(msg, "503") ||
		strings.Contains(msg, "504") {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
