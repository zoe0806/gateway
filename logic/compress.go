package logic

import (
	"strings"

	"gateway/config"
	"gateway/tools"
)

// CompressMessages 在发往上游前归一化并裁剪上下文，减少 token。
func CompressMessages(msgs []tools.Message, cfg config.PromptCompressConfig) []tools.Message {
	if len(msgs) == 0 || !cfg.Enabled {
		return msgs
	}

	out := make([]tools.Message, 0, len(msgs))
	for _, m := range msgs {
		c := m.Content
		if cfg.CollapseBlankLines {
			c = collapseBlankLines(c)
		}
		c = strings.TrimSpace(c)
		if cfg.MaxMessageChars > 0 && len(c) > cfg.MaxMessageChars {
			c = truncateUTF8(c, cfg.MaxMessageChars) + "\n[... truncated by gateway ...]"
		}
		out = append(out, tools.Message{Role: m.Role, Content: c})
	}

	if cfg.MaxTotalChars <= 0 {
		return out
	}

	total := 0
	for _, m := range out {
		total += len(m.Content)
	}
	if total <= cfg.MaxTotalChars {
		return out
	}

	var prefix []tools.Message
	i := 0
	for i < len(out) && strings.EqualFold(out[i].Role, "system") {
		prefix = append(prefix, out[i])
		i++
	}
	tail := out[i:]

	const bridge = "[Gateway] Earlier turns were omitted to fit the context budget.\n"
	budget := cfg.MaxTotalChars
	for _, m := range prefix {
		budget -= len(m.Content)
	}
	budget -= len(bridge)

	picked := pickTailWithinBudget(tail, budget)
	if len(picked) == len(tail) {
		return append(append([]tools.Message{}, prefix...), picked...)
	}

	bridgeMsg := tools.Message{Role: "user", Content: bridge}
	return append(append(append([]tools.Message{}, prefix...), bridgeMsg), picked...)
}

func truncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	b := []byte(s)
	n := maxBytes
	for n > 0 && n < len(b) && b[n]&0xC0 == 0x80 {
		n--
	}
	return string(b[:n])
}

func collapseBlankLines(s string) string {
	s = strings.TrimSpace(s)
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	for strings.Contains(s, "\r\n\r\n\r\n") {
		s = strings.ReplaceAll(s, "\r\n\r\n\r\n", "\r\n\r\n")
	}
	return s
}

func pickTailWithinBudget(tail []tools.Message, budget int) []tools.Message {
	if budget <= 0 || len(tail) == 0 {
		return nil
	}
	used := 0
	acc := make([]tools.Message, 0)
	for idx := len(tail) - 1; idx >= 0; idx-- {
		m := tail[idx]
		l := len(m.Content)
		remain := budget - used
		if l <= remain {
			acc = append([]tools.Message{m}, acc...)
			used += l
			continue
		}
		if remain > 256 {
			partial := tools.Message{Role: m.Role, Content: truncateUTF8(m.Content, remain-80) + "\n[... truncated ...]"}
			acc = append([]tools.Message{partial}, acc...)
		}
		break
	}
	return acc
}
