package tools

type Request struct {
	ApiKey string `json:"api_key"`
	Model  string `json:"model"`
	// RoutingMode: 空或 "auto" 按网关策略；"economy" 强制经济模型；"premium" 强制使用 Model 字段。
	RoutingMode string    `json:"routing_mode,omitempty"`
	Messages    []Message `json:"messages"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Response struct {
	Id      string   `json:"id"`
	Object  string   `json:"object"`
	Choices []Choice `json:"choices"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}
