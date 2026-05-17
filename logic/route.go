package logic

import (
	"fmt"
	"strings"

	"gateway/config"
	"gateway/tools"
)

type routeDecision struct {
	Backend     string
	Model       string // 发往上游的 model
	ClientModel string
	Economy     bool
}

func (l *ChatLogic) effectiveRouting() config.RoutingConfig {
	r := l.svcCtx.Config.Routing
	if !r.Enabled {
		return r
	}
	if r.EconomyModel == "" {
		r.EconomyModel = "deepseek-chat"
	}
	if r.EconomyBackend == "" {
		r.EconomyBackend = "deepseek"
	}
	if r.MaxCharsSimple == 0 {
		r.MaxCharsSimple = 3000
	}
	if r.MaxMessagesSimple == 0 {
		r.MaxMessagesSimple = 8
	}
	if len(r.ComplexKeywords) == 0 {
		r.ComplexKeywords = []string{
			"refactor", "security audit", "cryptography", "formal proof",
			"exploit", "architecture review", "penetration", "漏洞",
		}
	}
	return r
}

func (l *ChatLogic) effectivePromptCompress() config.PromptCompressConfig {
	p := l.svcCtx.Config.PromptCompress
	if !p.Enabled {
		return p
	}
	if p.MaxTotalChars == 0 {
		p.MaxTotalChars = 16000
	}
	if p.MaxMessageChars == 0 {
		p.MaxMessageChars = 12000
	}
	return p
}

func (l *ChatLogic) finishRoute(clientModel, backend string, economy bool) (routeDecision, error) {
	routed := clientModel
	if economy {
		r := l.effectiveRouting()
		backend = r.EconomyBackend
		routed = r.EconomyModel
	}
	upstream := l.upstreamModel(clientModel, routed)
	return routeDecision{
		Backend:     backend,
		Model:       upstream,
		ClientModel: clientModel,
		Economy:     economy,
	}, nil
}

func (l *ChatLogic) resolveRoute(req *tools.Request) (routeDecision, error) {
	clientModel := req.Model
	mode := strings.ToLower(strings.TrimSpace(req.RoutingMode))
	if mode == "" {
		mode = "auto"
	}

	r := l.effectiveRouting()

	if mode == "economy" {
		if !l.hasBackend(r.EconomyBackend) {
			return routeDecision{}, fmt.Errorf("economy backend %q not in apis config", r.EconomyBackend)
		}
		return l.finishRoute(clientModel, r.EconomyBackend, true)
	}

	if !r.Enabled || mode == "premium" {
		b, err := l.backendForModel(clientModel)
		if err != nil {
			return routeDecision{}, err
		}
		return l.finishRoute(clientModel, b, false)
	}

	// auto
	if isComplexTask(req.Messages, r) {
		b, err := l.backendForModel(clientModel)
		if err != nil {
			return routeDecision{}, err
		}
		return l.finishRoute(clientModel, b, false)
	}

	if !l.hasBackend(r.EconomyBackend) {
		b, err := l.backendForModel(clientModel)
		if err != nil {
			return routeDecision{}, err
		}
		return l.finishRoute(clientModel, b, false)
	}
	return l.finishRoute(clientModel, r.EconomyBackend, true)
}

func isComplexTask(messages []tools.Message, r config.RoutingConfig) bool {
	chars := 0
	for _, m := range messages {
		chars += len(m.Content)
	}
	if chars > r.MaxCharsSimple {
		return true
	}
	if len(messages) > r.MaxMessagesSimple {
		return true
	}
	var b strings.Builder
	for _, m := range messages {
		b.WriteString(strings.ToLower(m.Content))
		b.WriteByte('\n')
	}
	combined := b.String()
	for _, kw := range r.ComplexKeywords {
		k := strings.ToLower(strings.TrimSpace(kw))
		if k != "" && strings.Contains(combined, k) {
			return true
		}
	}
	return false
}

func sessionKeyFromParams(params ChatParams) string {
	if strings.TrimSpace(params.SessionID) != "" {
		return strings.TrimSpace(params.SessionID)
	}
	for _, m := range params.Messages {
		if strings.EqualFold(m.Role, "user") && m.Content != "" {
			return tools.HashSession(m.Content)
		}
	}
	return ""
}
