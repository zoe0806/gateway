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
	"time"

	"gateway/config"
	"gateway/store"
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

type ChatLogic struct {
	ctx              context.Context
	svcCtx           *tools.ServiceContext
	lbByName         map[string]*SimpleLoadBalancer
	limiters         map[string]*rate.Limiter
	breakers         map[string]*gobreaker.CircuitBreaker
	modelMap         map[string]config.ModelRoute
	concurrency      *ConcurrencyGate
	upstreamTimeout  time.Duration
	failoverMaxTries int
}

type ChatParams struct {
	ApiKey      string
	SessionID   string
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
		modelMap:         buildModelMap(svcCtx.Config.ModelMap),
		concurrency:      NewConcurrencyGate(svcCtx.Config.Concurrency),
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

type callResult struct {
	resp        *openai.ChatCompletionResponse
	instanceURL string
}

func (l *ChatLogic) callInstance(
	ctx context.Context,
	backendName string,
	apiCfg config.Api,
	params ChatParams,
	model string,
	messages []tools.Message,
) (*callResult, error) {
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
	return &callResult{resp: result.(*openai.ChatCompletionResponse), instanceURL: apiCfg.Url}, nil
}

type streamResult struct {
	promptTokens     int
	completionTokens int
	totalTokens      int
	instanceURL      string
}

func (l *ChatLogic) streamInstance(
	ctx context.Context,
	backendName string,
	apiCfg config.Api,
	params ChatParams,
	model string,
	messages []tools.Message,
	w http.ResponseWriter,
) (*streamResult, error) {
	if strings.TrimSpace(apiCfg.Url) == "" {
		return nil, fmt.Errorf("backend %q has empty url", backendName)
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	var out streamResult
	out.instanceURL = apiCfg.Url
	promptEst := tools.EstimatePromptTokens(messages)
	var completionBuf strings.Builder

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
			if chunk.Usage != nil {
				out.promptTokens = chunk.Usage.PromptTokens
				out.completionTokens = chunk.Usage.CompletionTokens
				out.totalTokens = chunk.Usage.TotalTokens
			}
			for _, ch := range chunk.Choices {
				completionBuf.WriteString(ch.Delta.Content)
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
	if err != nil {
		return nil, err
	}
	if out.promptTokens == 0 {
		out.promptTokens = promptEst
	}
	if out.completionTokens == 0 {
		out.completionTokens = tools.EstimateCompletionTokens(completionBuf.String())
	}
	if out.totalTokens == 0 {
		out.totalTokens = out.promptTokens + out.completionTokens
	}
	return &out, nil
}

func (l *ChatLogic) callBackendWithFailover(
	ctx context.Context,
	backendName string,
	sessionKey string,
	params ChatParams,
	model string,
	messages []tools.Message,
) (*callResult, error) {
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
		apiCfg := lb.Pick(ctx, l.svcCtx.Sticky, backendName, sessionKey)
		res, err := l.callInstance(ctx, backendName, apiCfg, params, model, messages)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if sessionKey != "" {
			l.svcCtx.Sticky.Delete(ctx, backendName, sessionKey)
		}
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
	sessionKey string,
	params ChatParams,
	model string,
	messages []tools.Message,
	w http.ResponseWriter,
) (*streamResult, error) {
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
		apiCfg := lb.Pick(ctx, l.svcCtx.Sticky, backendName, sessionKey)
		res, err := l.streamInstance(ctx, backendName, apiCfg, params, model, messages, w)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if sessionKey != "" {
			l.svcCtx.Sticky.Delete(ctx, backendName, sessionKey)
		}
		if !isRetryableUpstreamErr(err) {
			break
		}
		log.Printf("stream failover: backend=%s try=%d/%d err=%v", backendName, i+1, tries, err)
	}
	return nil, lastErr
}

func (l *ChatLogic) prepareChat(params ChatParams) (routeDecision, []tools.Message, string, error) {
	route, err := l.resolveRoute(params.toRequest())
	if err != nil {
		return routeDecision{}, nil, "", err
	}
	pc := l.effectivePromptCompress()
	msgs := CompressMessages(params.Messages, pc)
	sk := sessionKeyFromParams(params)
	return route, msgs, sk, nil
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

func (l *ChatLogic) recordUsage(params ChatParams, route routeDecision, instanceURL string, prompt, completion, total int, stream bool, latency time.Duration, status string) {
	if l.svcCtx.Usage == nil {
		return
	}
	l.svcCtx.Usage.Record(context.Background(), store.UsageRecord{
		ApiKey:           params.ApiKey,
		ClientModel:      route.ClientModel,
		RoutedModel:      route.Model,
		Backend:          route.Backend,
		InstanceURL:      instanceURL,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      total,
		Economy:          route.Economy,
		Stream:           stream,
		LatencyMs:        latency.Milliseconds(),
		Status:           status,
	})
}

func (l *ChatLogic) ChatCompletion(ctx context.Context, w http.ResponseWriter, params ChatParams) error {
	release, err := l.concurrency.Acquire(params.ApiKey)
	if err != nil {
		return err
	}
	defer release()

	start := time.Now()
	route, msgs, sessionKey, err := l.prepareChat(params)
	if err != nil {
		return err
	}
	if err := l.checkRateLimit(route); err != nil {
		return err
	}

	if params.Stream {
		sr, err := l.streamBackendWithFailover(ctx, route.Backend, sessionKey, params, route.Model, msgs, w)
		if err != nil {
			l.recordUsage(params, route, "", tools.EstimatePromptTokens(msgs), 0, 0, true, time.Since(start), "error")
			log.Printf("ChatCompletion failed: %v", err)
			return fmt.Errorf("backend call failed: %w", err)
		}
		l.recordUsage(params, route, sr.instanceURL, sr.promptTokens, sr.completionTokens, sr.totalTokens, true, time.Since(start), "ok")
	} else {
		res, err := l.callBackendWithFailover(ctx, route.Backend, sessionKey, params, route.Model, msgs)
		if err != nil {
			l.recordUsage(params, route, "", tools.EstimatePromptTokens(msgs), 0, 0, false, time.Since(start), "error")
			log.Printf("ChatCompletion failed: %v", err)
			return fmt.Errorf("backend call failed: %w", err)
		}
		p, c, t := tools.UsageFromResponse(res.resp)
		l.recordUsage(params, route, res.instanceURL, p, c, t, false, time.Since(start), "ok")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(res.resp); err != nil {
			return err
		}
	}

	log.Printf("Chat: backend=%s economy=%t routed_model=%s client_model=%s stream=%t session=%t",
		route.Backend, route.Economy, route.Model, params.Model, params.Stream, sessionKey != "")
	return nil
}

func (l *ChatLogic) Chat(req *tools.Request) (*tools.Response, error) {
	params := ChatParams{
		ApiKey:      req.ApiKey,
		Model:       req.Model,
		Messages:    req.Messages,
		RoutingMode: req.RoutingMode,
		Stream:      false,
	}

	release, err := l.concurrency.Acquire(params.ApiKey)
	if err != nil {
		return nil, err
	}
	defer release()

	start := time.Now()
	route, msgs, sessionKey, err := l.prepareChat(params)
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
	res, err := l.callBackendWithFailover(ctx, route.Backend, sessionKey, params, route.Model, msgs)
	if err != nil {
		l.recordUsage(params, route, "", tools.EstimatePromptTokens(msgs), 0, 0, false, time.Since(start), "error")
		return nil, fmt.Errorf("backend call failed: %w", err)
	}

	p, c, t := tools.UsageFromResponse(res.resp)
	l.recordUsage(params, route, res.instanceURL, p, c, t, false, time.Since(start), "ok")

	out := &tools.Response{
		Id:     res.resp.ID,
		Object: res.resp.Object,
		Choices: []tools.Choice{{
			Index: 0,
			Message: tools.Message{
				Role:    res.resp.Choices[0].Message.Role,
				Content: res.resp.Choices[0].Message.Content,
			},
			FinishReason: string(res.resp.Choices[0].FinishReason),
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
