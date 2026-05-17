package logic

import (
	"fmt"
	"strings"

	"gateway/config"
)

func buildModelMap(raw map[string]config.ModelRoute) map[string]config.ModelRoute {
	out := make(map[string]config.ModelRoute, len(raw))
	for k, v := range raw {
		out[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return out
}

func (l *ChatLogic) backendForModel(model string) (string, error) {
	m := strings.ToLower(strings.TrimSpace(model))
	if entry, ok := l.modelMap[m]; ok && entry.Backend != "" {
		if !l.hasBackend(entry.Backend) {
			return "", fmt.Errorf("model_map backend %q not configured", entry.Backend)
		}
		return entry.Backend, nil
	}
	return l.selectBackend(model)
}

func (l *ChatLogic) upstreamModel(clientModel, routedModel string) string {
	if entry, ok := l.modelMap[strings.ToLower(strings.TrimSpace(routedModel))]; ok && entry.Upstream != "" {
		return entry.Upstream
	}
	if entry, ok := l.modelMap[strings.ToLower(strings.TrimSpace(clientModel))]; ok && entry.Upstream != "" {
		return entry.Upstream
	}
	return routedModel
}
