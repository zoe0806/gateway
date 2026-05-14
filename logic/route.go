package logic

import (
	"fmt"
	"strings"

	"gateway/config"
	"gateway/tools"
)

type routeDecision struct {
	Backend string
	Model   string
	Economy bool
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

func (l *ChatLogic) resolveRoute(req *tools.Request) (routeDecision, error) {
	mode := strings.ToLower(strings.TrimSpace(req.RoutingMode))
	if mode == "" {
		mode = "auto"
	}

	r := l.effectiveRouting()
	if !r.Enabled || mode == "premium" {
		b, err := l.selectBackend(req.Model)
		if err != nil {
			return routeDecision{}, err
		}
		return routeDecision{Backend: b, Model: req.Model, Economy: false}, nil
	}

	if mode == "economy" {
		if !l.hasBackend(r.EconomyBackend) {
			return routeDecision{}, fmt.Errorf("economy backend %q not in apis config", r.EconomyBackend)
		}
		return routeDecision{Backend: r.EconomyBackend, Model: r.EconomyModel, Economy: true}, nil
	}

	// auto
	if isComplexTask(req.Messages, r) {
		b, err := l.selectBackend(req.Model)
		if err != nil {
			return routeDecision{}, err
		}
		return routeDecision{Backend: b, Model: req.Model, Economy: false}, nil
	}

	if !l.hasBackend(r.EconomyBackend) {
		b, err := l.selectBackend(req.Model)
		if err != nil {
			return routeDecision{}, err
		}
		return routeDecision{Backend: b, Model: req.Model, Economy: false}, nil
	}
	return routeDecision{Backend: r.EconomyBackend, Model: r.EconomyModel, Economy: true}, nil
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
