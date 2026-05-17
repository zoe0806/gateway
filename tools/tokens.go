package tools

import (
	"github.com/sashabaranov/go-openai"
)

func UsageFromResponse(resp *openai.ChatCompletionResponse) (prompt, completion, total int) {
	if resp == nil {
		return 0, 0, 0
	}
	prompt = resp.Usage.PromptTokens
	completion = resp.Usage.CompletionTokens
	total = resp.Usage.TotalTokens
	return prompt, completion, total
}

func EstimatePromptTokens(messages []Message) int {
	chars := 0
	for _, m := range messages {
		chars += len(m.Content)
	}
	if chars == 0 {
		return 0
	}
	return chars/4 + 1
}

func EstimateCompletionTokens(text string) int {
	if text == "" {
		return 0
	}
	return len(text)/4 + 1
}
