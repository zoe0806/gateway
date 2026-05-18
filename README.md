# Gateway

轻量级 **AI API 网关**：统一接入 OpenAI / DeepSeek / Gemini 等上游，在转发前完成鉴权、路由、降本与稳定性治理。

---

## 核心功能

### 协议与接入

| 能力 | 说明 |
|------|------|
| 统一聊天接口 | `POST /chat`，请求/响应与 OpenAI Chat Completions 兼容，支持 `stream: true` SSE |
| 鉴权 | `Authorization: Bearer <key>` 或 `X-Api-Key`；MySQL 存 Key，Redis 热缓存 |

### 路由与降本

| 能力 | 说明 |
|------|------|
| 智能模型路由 | `routing`：简单对话走经济模型，复杂任务（长上下文 / 关键词）走客户端指定模型 |
| 路由模式 | 请求头 `X-Routing-Mode`：`auto`（默认）/ `economy` / `premium` |
| 模型映射 | `model_map`：客户端 model → `backend` + 上游 `upstream`，避免字符串猜测 |
| Prompt 压缩 | 裁剪单条/总字符、合并空行，降低上游 token 消耗 |

### 稳定性

| 能力 | 说明 |
|------|------|
| 多实例负载均衡 | 同一 `name` 配置 `url` + `urls[]`，轮询选取 |
| 粘性会话 | Redis 绑定 `backend + session` → 固定实例，多轮对话更稳定 |
| 实例 Failover | 上游超时 / 5xx / 熔断打开时自动换实例重试 |
| 熔断 | 按 `backend|url` 维度 gobreaker |
| 限流 | 按 backend 令牌桶；超限返回友好降级 |
| 并发控制 | 全局限流 + 单 API Key 并发上限 |

### 可观测

| 能力 | 说明 |
|------|------|
| 用量审计 | 异步写入 MySQL `usage_logs`（prompt/completion tokens、耗时、是否经济路由等） |
| 用量查询 | `GET /usage?days=7`，按当前 Key 汇总（可供 HTML+JS 面板调用） |

---

## 技术架构

### 技术栈

| 层级 | 技术 |
|------|------|
| 语言 | Go 1.25+ |
| HTTP | Gin |
| 上游客户端 | [go-openai](https://github.com/sashabaranov/go-openai) |
| 数据库 | MySQL + GORM |
| 缓存 | Redis（API Key 缓存、粘性会话） |
| 韧性 | gobreaker、x/time/rate |

### 目录结构

```
gateway/
├── main.go              # 入口、路由注册
├── config.json          # 运行时配置（与二进制同目录）
├── config/              # 配置加载
├── auth/                # API Key 鉴权（Redis + MySQL）
├── handler/             # /chat、用量 HTTP 处理
├── logic/               # 路由、压缩、LB、failover、并发
├── model/               # GORM 模型（api_keys、usage_logs）
├── store/               # MySQL / Redis / 用量 / 粘性会话
└── tools/               # ServiceContext、请求类型
```

### 请求链路

1. **鉴权**：Redis 命中 → 否则查 MySQL `api_keys` 并回写缓存  
2. **路由**：`model_map` + `routing` 决定 backend 与上游 model  
3. **压缩**：`prompt_compress` 处理 messages  
4. **并发 / 限流**：全局与 per-key 信号量；backend 令牌桶  
5. **选实例**：粘性会话优先，否则轮询；失败可 failover  
6. **上游调用**：非流式 JSON / 流式 SSE  
7. **审计**：异步写入 `usage_logs`

---

## 快速开始

### 环境要求

- Go 1.25+
- MySQL 5.7+ / 8.0
- Redis 6+


### 2. 配置

复制并编辑项目根目录 `config.json`（与运行目录一致）：

```json
{
  "port": 8080,
  "mode": "release",
  "mysql": {
    "host": "127.0.0.1",
    "port": 3306,
    "user": "root",
    "password": "your_password",
    "database": "test"
  },
  "redis": {
    "addr": "127.0.0.1:6379",
    "password": "",
    "db": 0,
    "api_key_cache_ttl_sec": 300
  },
  "apis": [
    {
      "name": "deepseek",
      "url": "https://api.deepseek.com/v1",
      "api_key": "sk-upstream-deepseek"
    }
  ]
}
```

同一后端多实例示例：

```json
{
  "name": "deepseek",
  "urls": [
    "https://api.deepseek.com/v1",
    "https://backup.example.com/v1"
  ],
  "api_key": "sk-xxx"
}
```

### 3. 编译与运行

```bash
cd gateway
go mod tidy
go build -o gateway .
./gateway
```

Windows：

```powershell
go run .
```

日志出现 `listening on :8080` 即启动成功。

### 4. 健康检查

```bash
curl http://127.0.0.1:8080/health
```

### 5. 聊天接口 `POST /chat`

请求体与 [OpenAI Chat Completions](https://platform.openai.com/docs/api-reference/chat/create) 一致。SDK 可将 `base_url` 设为 `http://127.0.0.1:8080` 并请求 `/chat`（若 SDK 固定 `/v1/chat/completions`，可用 Nginx 重写或换支持自定义 path 的客户端）。

**非流式：**

```bash
curl http://127.0.0.1:8080/chat \
  -H "Authorization: Bearer sk-proj-1234567890" \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"deepseek-v4-flash\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}"
```

**流式：**

```bash
curl http://127.0.0.1:8080/chat \
  -H "Authorization: Bearer sk-proj-1234567890" \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"deepseek-v4-flash\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}"
```

**可选请求头：**

| Header | 说明 |
|--------|------|
| `X-Routing-Mode` | `auto` / `economy` / `premium` |
| `X-Session-Id` | 粘性会话 ID；不传则用首条 user 消息哈希 |

### 6. 查询用量

```bash
curl "http://127.0.0.1:8080/usage/summary?days=7" \
  -H "Authorization: Bearer sk-proj-1234567890"
```

---

## 配置说明（节选）

| 配置块 | 作用 |
|--------|------|
| `apis` | 上游列表：`name`、`url` / `urls`、`api_key` |
| `model_map` | 客户端 model → `backend`、`upstream` |
| `routing` | 经济路由开关、经济模型、复杂度阈值与关键词 |
| `prompt_compress` | 上下文压缩开关与字符上限 |
| `sticky_session` | Redis 粘性会话 TTL |
| `concurrency` | 全局 / 单 Key 最大并发 |
| `gateway` | 读写超时、上游超时、failover 次数、body 上限 |

---



## 许可证

MIT LICENSE
