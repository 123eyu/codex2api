# Responses 兼容性分阶段说明

Codex2API 的 Responses 入口同时服务官方 Codex 客户端、OpenAI SDK 和多个上游渠道。不同渠道的上游能力并不相同，因此不能把所有请求字段都当成可原样透传。

## 本阶段已完成

- 新增 `unsupported_feature` 错误码，HTTP 错误和 WebSocket 错误帧都可以明确表达“能力不存在”。
- `response.append` 和 `response.steer` 当前明确返回 `unsupported_feature`，不会返回一个看似成功但没有执行 steer 的结果。
- Responses 入口支持 `X-Codex2API-Strict-Compatibility: true`；也可以设置 `CODEX_STRICT_RESPONSES_COMPAT=true`。Codex 渠道在严格模式下会拒绝无法原样保留的字段，并返回字段列表。
- 账号模型能力探测会保存 `experimental_supported_tools`，模型清单按可用账号取交集，未知账号不会继承高级工具能力。

## 当前边界

- `response.steer` 尚未实现。要正确实现它，需要活动轮次的并发控制、上游连接写入、取消与后续 `response.create` 的状态机，不能只接受一帧 JSON。
- `stream_id` 目前用于网关侧抢占和缓存隔离，不代表同一上游 WebSocket 已实现官方并行 lane 语义。
- `reasoning_effort`、`response_format` 等字段已有明确的本地转换路径；严格模式不会把它们误报为完全不支持。
- Redis/Memory 续链、compact、工具调用和模型能力仍需用真实上游做端到端验证；模型目录或 HTTP 200 不等于生成和工具回合成功。

## 后续顺序

1. 原生 Responses WebSocket 状态机：`response.steer`、多 lane、取消和断线恢复。
2. Responses `conversation`、`context_management`、background 和缓存控制的持久化语义。
3. function/strict/parallel、hosted tools、MCP、文件、图片和音频的逐上游验收矩阵。

每个阶段都应以官方 SDK 直连结果作为基线，比较事件序列、字段、终态、工具回合和错误码，而不是只检查 HTTP 状态码。
