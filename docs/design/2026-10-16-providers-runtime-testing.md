# Provider、运行与测试设计 v1.1

| 项目 | 内容 |
|---|---|
| 状态 | 设计冻结；可直接实现，Provider/Embedding 变更受 RAG 质量 Gate 约束 |
| 默认文本模型 | `qwen-plus` |
| 默认 Embedding | `Qwen3-Embedding-8B`，1024 维 |
| 默认 Rerank | `qwen3-rerank` |
| 默认联网 | Qwen/DashScope 原生搜索，不默认接第三方搜索 API |

## 1. Provider 领域接口

```go
type IdentityAdapter interface {
    Authenticate(ctx context.Context, req IdentityRequest) (IdentityContext, error)
}

type LLMProvider interface {
    Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error)
}

type StructuredGenerator interface {
    GenerateStructured(ctx context.Context, req StructuredRequest, out any) error
}

type EmbeddingProvider interface {
    Embed(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, error)
    Dimensions() int
    Capabilities() EmbeddingCapabilities
}

type RerankProvider interface {
    Rerank(ctx context.Context, query string, docs []RerankDocument) ([]RerankResult, error)
}

type WebSearchProvider interface {
    Search(ctx context.Context, req WebSearchRequest) (WebSearchResponse, error)
    Capabilities() WebSearchCapabilities
}

type ScholarlyDiscoverySource interface {
    Search(ctx context.Context, req DiscoveryRequest) (DiscoveryResponse, error)
    Get(ctx context.Context, id ExternalID) (ExternalWork, error)
}
```

`IdentityAdapter` 只负责把请求认证转换为服务端可信的 `actor_id`、`workspace_id` 和 `role`；业务模块不能从请求体接受租户字段。v1 提供固定本地适配器；生产环境未配置生产适配器时必须拒绝启动。

Embedding 契约：

- `EmbeddingRequest.kind = query | passage`，两侧允许使用不同 instruction；不能在查询和建索引时复用未声明用途的默认调用。
- `EmbeddingCapabilities` 至少报告实际维度、是否支持 `dimensions`、最大 batch 和最大输入长度；v1 不依赖 Provider 自动降维。
- `Dimensions()` 的实际值必须等于 `EMBEDDING_DIM`，否则启动 fail fast；模型返回不同维度时不得截断、补零或静默建列。
- 归一化、query/passage instruction、截断策略和模型任一变化都会改变 `embedding_version`，并触发新 `index_epoch`。

Provider 错误统一映射为 `ProviderError`：

```text
unavailable | rate_limited | unauthorized | invalid_response | timeout | timeout_budget
```

所有 Provider 必须记录 duration、status、retry count、model/provider 和请求 ID；不得记录完整私人全文、完整用户问题或密钥。

## 2. 模型 Provider

### 2.1 OpenAI 兼容协议

默认 LLM、Embedding 和 Rerank 都通过 OpenAI 兼容协议适配。模型名称、基址和 Key 完全配置化，核心领域不依赖某个 SDK。

- 文本生成：`/chat/completions`。
- 结构化生成：JSON mode 或明确的 JSON schema prompt；服务端必须重新解析和校验。
- Embedding：`/embeddings`；请求显式携带 query/passage 用途与 instruction，响应校验条数和维度后再持久化。
- Rerank：优先调用独立 rerank endpoint；不可用时退回基于 RRF/向量相似度的 LocalRerank。
- 超时：普通生成 60 秒；结构化生成 45 秒；embedding batch 60 秒。
- 重试：仅对 `429`、`5xx` 和网络错误进行最多 3 次指数退避；认证错误不重试。

### 2.2 默认模型语义

| 能力 | 默认 | 说明 |
|---|---|---|
| Query Planner | `qwen-plus` | 结构化输出，最多 5 个 subquery |
| Brief Writer | `qwen-plus` | 只根据验证后的 Evidence |
| Embedding | `Qwen3-Embedding-8B` / 1024 | batch 32；实际维度通过能力探测确认，必须与 pgvector 和 `EMBEDDING_DIM` 一致 |
| Rerank | `qwen3-rerank` | top 30；失败回退 RRF |
| Web Search | Qwen/DashScope | 通过 `enable_search`/`web_search` 能力 |

如果部署环境使用其他模型，只替换配置和 Provider adapter，不改变检索、Evidence 或 Brief schema。

## 3. 原生联网搜索

### 3.1 能力探测

启动或首次调用时读取 Provider 能力：

```json
{
  "supports_native_search": true,
  "provider": "qwen_dashscope",
  "max_results": 20,
  "supports_freshness": true,
  "supports_source_url": true
}
```

探测失败不影响 `my_library` 和 `gap_fill`。

### 3.2 Qwen 请求

- 默认走 OpenAI 兼容 Chat Completions。
- 请求参数包含 `model`、`messages`、`enable_search=true`、`search_options.enable_source=true`。
- 需要最新进展时允许受控 `freshness`；默认不限制时间。
- 不把完整个人 fulltext 或私人问题拼进搜索 query；只传去标识化研究主题和必要上下文。
- 搜索结果记录 provider、model、query fingerprint、retrieved_at、URL、title、snippet 和原始响应 ID。
- compatible-mode 未返回 `search_info` 时，可从回答正文的 Markdown/裸 HTTP(S) 链接回退提取来源，但必须标记 `degraded` / `web_search_content_fallback`，不得把模型总结冒充网页原文。

### 3.3 降级

- Key 缺失：`web_search_unavailable`，不重复调用。
- 时间/配额错误：记录 source status，当前 turn 继续。
- 结构化 `search_info` 为空但正文含 URL：接收为降级 `web_context`，reason 为 `web_search_content_fallback`。
- 完全没有 URL citation：丢弃网页证据并返回 `web_search_unavailable` / `web_search_no_sources`。
- Provider 不支持原生搜索：不自动推断使用第三方 API；后续通过新 adapter 接入。

## 4. Semantic Scholar 与 OpenAlex

### 4.1 Semantic Scholar

- 认证：`S2_API_KEY`。
- 默认限流：1 request/second。
- 429：读取 `Retry-After`；否则指数退避。
- 重试：最多 4 次。
- 超时：20 秒。
- 解析时清理非法控制字符，但保留原始响应用于短期诊断时先脱敏。

### 4.2 OpenAlex

- 请求带 `OPENALEX_EMAIL`。
- 默认并发 2，超时 20 秒。
- 使用 cursor pagination；不能假定前几页包含全部结果。
- abstract inverted index 还原失败时保留 `abstract=NULL` 并标记 `invalid_response`，不能让空摘要伪装成完整摘要。

### 4.3 统一输出

两类结果先归一化到：

```text
ExternalWork
  identity_key
  doi / s2_id / openalex_id / pmid
  title / abstract / year / venue / authors / citation_count / type
  source_status
  retrieved_at
```

外部结果进入 Brief 前必须经过 workspace scope、去重、引用和证据等级检查。

## 5. 本地运行

### 5.1 启动顺序

1. `docker compose up -d postgres`（仅数据库容器，不在本轮交付 Compose 文件）。
2. 运行 Goose migration。
3. 启动本地 API 进程。
4. 启动本地 Worker 进程。
5. 启动 React/Vite dev server。
6. 使用本地身份头或固定配置 seed workspace。

开发环境可以使用 Provider stub 完成离线闭环：

- `StubLLMProvider` 返回稳定结构化 JSON。
- `StubEmbeddingProvider` 返回固定维度的确定性向量。
- `StubRerankProvider` 保持输入顺序。
- `StubWebSearchProvider` 返回带 URL 的合规 fixture。
- `StubScholarlyDiscoverySource` 返回固定 DOI/摘要 fixture。

### 5.2 可观测性

- JSON 结构化日志，至少包含 `request_id`、`workspace_id`、`session_id`、`job_id`、`provider`、`duration_ms`、`status`。
- 指标：同步时长、失败率、fulltext/摘要/仅元数据计数、检索候选数、`Recall@12`、`nDCG@10`、citation precision、citation recall、unsupported claim rate、rerank 成功率、代理步骤、SSE 事件延迟、清理队列年龄。
- 索引与质量指标：index epoch publish duration/failure、building epoch 年龄、embedding 维度校验失败数、prompt injection detection count、hard gate failure count。
- 评测运行必须记录 `planner_version`、`retrieval_config_version`、`embedding_version`、`index_epoch_id`、`rerank_version` 和 `prompt_version`。
- Trace：HTTP、数据库事务、River job、Provider 调用；不得把 fulltext 或原始问题放入 attributes。
- 对外返回来源状态，不把内部 stack trace 返回给用户。

## 6. 测试矩阵

| 层级 | 测试内容 | 通过条件 |
|---|---|---|
| 单元测试 | 枚举、scope、identity、幂等键、状态机、错误映射 | 阶段 0 后 `make test-unit` 通过；对应 Go `go test ./...` |
| 数据库集成 | Goose、事务、FTS、pgvector、RRF、tombstone、index epoch 原子发布、并发任务 | Testcontainers PostgreSQL/pgvector 可重复通过 |
| Zotero 合同 | fixture 分页、since、附件、fulltext、429、deleted、同步中旧 epoch 查询 | 不访问真实私人 library |
| Provider 合同 | S2、OpenAlex、Qwen 搜索、Embedding 维度/instruction、模型超时和降级 | 用 fixture 验证解析、能力与错误映射 |
| RAG 质量评测 | golden set、Recall/nDCG、claim grounding、citation precision、缺口和拒答 | `make eval-rag` 产出机器可读报告，hard gate 全部通过 |
| RAG 安全与一致性 | prompt injection、workspace/epoch 越界、metadata-only 证据、引用图扩展 | 注入内容不能成为指令或 scientific claim，旧 brief 不读新 epoch |
| API 合同 | OpenAPI 校验、幂等、权限、分页、SSE 重连 | 与 OpenAPI 骨架一致 |
| Web E2E | 连接、同步状态、三种模式、证据追溯、清理 | Playwright 在本地 stub 环境通过 |
| 安全测试 | workspace 隔离、密钥脱敏、日志、管理员审计 | 无跨 workspace 泄漏 |

## 7. 阶段 0 smoke test

必须能一条命令或明确步骤重复执行：

```text
1. 启动 PostgreSQL/pgvector。
2. 执行 Goose migration 骨架。
3. seed 本地 workspace、项目和一个 collection。
4. 导入脱敏 Zotero fixture（含全文、摘要、仅元数据各一条）。
5. 触发同步和索引，等待任务完成。
6. 验证 building epoch 完整校验后原子发布为 active，旧 active epoch 仍可追溯。
7. 检查 fulltext/摘要/仅元数据覆盖计数。
8. 在 my_library 模式提交一个 golden question。
9. 等待 SSE 完成并获取 Brief，记录 turn/brief 使用的 epoch 和三个版本字段。
10. 验证至少一条 finding 的 claim 能通过 claim_evidence 追溯到 evidence/chunk；metadata-only 不在证据中。
11. 运行 `make eval-rag`，产出机器可读报告并验证 hard gate 全部通过。
12. 重复执行同步和查询，验证无重复行、无重复事件、无重复向量，且不会混读旧/新 epoch。
```

离线 Provider stub 版本是 CI 的默认 smoke test；真实 Zotero/API Key 只允许在本地手动验证，不进入 CI。

## 8. 验收场景

- 首次连接并完成全量同步。
- 中断后增量同步从 cursor 继续。
- Web API fulltext 缺失时摘要降级或 metadata-only。
- collection scope 改变后查询结果和 snapshot 同步变化。
- `my_library` 在外部来源全部失败时仍可完成。
- `gap_fill` 的库内/库外去重与三类缺口。
- `landscape` 的原生搜索、无 URL 引用、限流和降级。
- SSE 断线重连、Last-Event-ID 重放。
- 证据冲突、证据不足和 open question。
- claim support/refute 关系完整，无支持 claim 不得进入 findings。
- 检索内容包含 prompt injection 时，工具调用、schema 和 scientific claims 不被改变。
- 同步构建新 epoch 时，进行中的查询固定旧 epoch，结果不混入新文档版本。
- Zotero 删除、撤权、主动清空、30 天清理。
- 两个 workspace 之间的数据和审计隔离。
- 恢复备份后 tombstone 仍能阻止已删除对象重新出现。

## 9. 部署方向

本地验证优先；生产目标为单机 Linux：

```text
Caddy TLS
  -> React static assets
  -> Go API
  -> Go Worker
  -> PostgreSQL/pgvector volume
```

API 和 Worker 使用同一镜像、不同 command。生产环境必须替换本地身份适配器，配置真实密钥管理，启用 HTTPS、备份、指标和告警。本轮不实施部署，不冻结域名、主机、OAuth 或费用上限。
