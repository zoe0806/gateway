package tools

// Message 与 OpenAI chat messages 对齐（仅 string content）。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
