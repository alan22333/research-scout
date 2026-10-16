# Research Scout 实现蓝图 v1.1

| 项目 | 内容 |
|---|---|
| 状态 | 设计冻结；可进入编码，RAG 变更受质量 Gate 约束 |
| 日期 | 2026-10-16 |
| 关联 PRD | [PRD v1.4](../prd/2026-10-16-zotero-research-assistant-prd.md) |
| 关联架构 | [架构基线 v1.1](../architecture/2026-10-16-tech-stack.md) |
| 目标 | 冻结 v1 仓库、运行时、依赖、配置、错误、RAG 质量门禁和首轮实施顺序 |

## 1. 实现目标

阶段 0 结束时，开发者必须能用脱敏 Zotero fixture 完成一次完整验证：

```text
启动 PostgreSQL/pgvector
 -> 执行迁移骨架
 -> seed 本地 workspace
 -> 导入脱敏 Zotero fixture
 -> 建立文档与 chunk 索引并发布一个不可变 index_epoch
 -> 在 my_library 模式提问
 -> 生成带 claim-level 可追溯 Evidence 的 Brief
 -> 运行最小 golden question 并产出 make eval-rag 报告
 -> 通过 SSE 观察阶段、来源和降级状态
```

此验证不依赖真实用户、真实 PDF、Connector、OAuth 或 Lab Nexus。

## 2. 运行时与仓库

v1 采用模块化单体。API 与 Worker 共享领域模块和数据库，但通过不同进程命令启动：

```text
cmd/api       REST + SSE
cmd/worker    Zotero 同步、索引、发现和简报任务
internal/...  领域模块、Repository、Provider 适配器和任务处理器
web/          React/Vite 工作台
migrations/   Goose 迁移
openapi/      OpenAPI 3.1 契约
docs/         PRD、架构和模块设计
```

规则：

- API 不执行长任务；请求写入业务事务和 River job 后返回。
- Worker 不绕过领域服务直接修改他人模块的数据。
- Repository 隐藏 GORM 和 SQL；复杂 FTS/向量查询集中在 retrieval repository。
- 领域层不依赖 Gin、GORM、HTTP Provider 或具体模型 SDK。
- 跨模块通过明确接口和领域对象协作，不使用进程内全局状态。
- 所有资源查询先应用 `workspace_id`、生命周期、scope 和当前 `index_epoch` 过滤，再执行业务逻辑。
- 检索、Agent、Embedding 或 Prompt 变更必须更新对应版本字段并通过 RAG 质量 Gate；旧 turn/brief 固定原版本，不静默回读新版本。

## 3. 依赖方向

```text
HTTP/Worker transport
        |
        v
Agent / Sync / Lifecycle application services
        |
        v
Domain modules + repositories
        |
        v
PostgreSQL / Provider adapters / Zotero Web API
```

禁止方向：

- Provider 反向依赖 Agent 或 Brief。
- Web/接口层直接访问数据库。
- 检索模块自行扩大 workspace 或 collection scope。
- Agent 通过“追加 if”绕过固定工具权限。
- 模型输出直接成为证据；所有证据必须先经过服务端校验。

## 4. 配置

配置通过环境变量注入；本地可使用被 `.gitignore` 忽略的 `config.local.yaml`。密钥不得进入仓库、前端 bundle 或日志。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `APP_ENV` | `local` | `local/test/staging/production` |
| `HTTP_ADDR` | `:8080` | API 监听地址 |
| `DATABASE_URL` | 必填 | PostgreSQL/pgvector 连接 |
| `AUTO_MIGRATE` | `false` | 仅本地/测试可为 `true` |
| `SECRET_KEY` | 必填 | 凭据字段加密根密钥 |
| `DEV_AUTH_ENABLED` | `true`（仅 local） | 启用本地身份适配器 |
| `DEV_ACTOR_ID` | `local-user` | 固定 actor |
| `DEV_WORKSPACE_ID` | `local-workspace` | 固定 workspace |
| `DEV_ALLOW_ACTOR_HEADER` | `false` | 仅 local/test 可开启隔离测试 |
| `ZOTERO_API_BASE` | `https://api.zotero.org` | Zotero Web API |
| `ZOTERO_API_VERSION` | `3` | API 版本 |
| `ZOTERO_REQUEST_CONCURRENCY` | `4` | 最大并发，不得更高 |
| `S2_API_KEY` | 空 | Semantic Scholar |
| `OPENALEX_EMAIL` | 空 | Polite pool 标识 |
| `LLM_BASE_URL` | 必填 | OpenAI 兼容接口 |
| `LLM_API_KEY` | 必填 | 模型凭据 |
| `LLM_MODEL` | `qwen-plus` | 规划、结构化生成和简报 |
| `EMBEDDING_BASE_URL` | 必填 | Embedding API |
| `EMBEDDING_API_KEY` | 必填 | Embedding 凭据 |
| `EMBEDDING_MODEL` | `Qwen3-Embedding-8B` | 向量模型 |
| `EMBEDDING_DIM` | `1024` | 必须与数据库向量列一致 |
| `EMBEDDING_NORMALIZATION_VERSION` | `l2-v1` | Embedding 归一化协议版本 |
| `EMBEDDING_QUERY_INSTRUCTION` | 模型默认 | 查询侧 instruction；变化必须进入 `embedding_version` |
| `EMBEDDING_PASSAGE_INSTRUCTION` | 模型默认 | 文档侧 instruction；变化必须进入 `embedding_version` |
| `EMBEDDING_TRUNCATION_POLICY_VERSION` | `truncate-v1` | 超长输入截断策略版本 |
| `INDEX_EPOCH_PUBLISH_MODE` | `atomic` | v1 只允许校验完成后原子发布 |
| `PLANNER_VERSION` | `planner-v1` | Query Planner schema 与路由版本 |
| `RETRIEVAL_CONFIG_VERSION` | `retrieval-v1` | RRF、候选池、过滤和证据选择配置版本 |
| `PROMPT_VERSION` | `brief-v1` | Brief 生成与校验 Prompt 版本 |
| `RAG_EVAL_DATASET` | `eval/golden/rag-golden-v1.yaml` | 默认脱敏 golden set |
| `RERANK_BASE_URL` | 空 | 可为空 |
| `RERANK_API_KEY` | 空 | Rerank 凭据 |
| `RERANK_MODEL` | `qwen3-rerank` | 重排模型 |
| `WEB_SEARCH_MODE` | `provider_native` | `provider_native/disabled` |
| `WEB_SEARCH_PROVIDER` | `qwen_dashscope` | v1 首个实现 |
| `WORKER_CONCURRENCY` | `4` | Worker 并发 |
| `QUERY_MAX_STEPS` | `8` | Agent 工具步骤上限 |
| `QUERY_SOFT_TIMEOUT_SECONDS` | `120` | 查询软超时 |

生产环境缺少生产身份适配器时必须拒绝启动。`DEV_ALLOW_ACTOR_HEADER=true` 时仅允许读取测试用的 actor/workspace 请求头，并记录本地测试日志；非 local/test 环境忽略该开关。

RAG 版本规则：

- 候选池、RRF 权重、过滤、rerank 或证据选择变化：更新 `RETRIEVAL_CONFIG_VERSION`。
- Planner schema、意图或路由变化：更新 `PLANNER_VERSION`。
- Brief 生成或校验 Prompt 变化：更新 `PROMPT_VERSION`。
- Provider、模型、维度、归一化、query/passage instruction 或截断策略变化：生成新的 `embedding_version`，重建 chunk 并发布新 `index_epoch`。
- 运行中的 turn 和已发布 Brief 固定创建时的 `index_epoch_id`、`retrieval_config_version`、`planner_version` 和 `prompt_version`。

## 5. 统一错误模型

```json
{
  "error": {
    "code": "source_unavailable",
    "message": "Semantic Scholar is temporarily unavailable.",
    "retryable": true,
    "request_id": "req_...",
    "details": {}
  }
}
```

固定错误语义：

| code | HTTP | 语义 |
|---|---:|---|
| `invalid_request` | 400 | 请求或枚举不合法 |
| `unauthorized` | 401 | 身份缺失或失效 |
| `forbidden` | 403 | 越权或 scope 不允许 |
| `not_found` | 404 | 对象不存在或不可见 |
| `conflict` | 409 | 幂等键冲突或状态不允许 |
| `scope_empty` | 422 | 显式 scope 没有可检索内容 |
| `index_not_ready` | 409 | 需要先完成同步或索引 |
| `source_unavailable` | 503 | 外部来源不可用，可降级 |
| `web_search_unavailable` | 503 | 原生联网能力不可用 |
| `rate_limited` | 429 | 上游限流或本地并发保护 |
| `internal` | 500 | 未分类服务端错误 |

错误可以出现在 SSE 的 `error` 事件中；用户可见简报同时保留 `source_status`。

## 6. 实施顺序

### 阶段 0：契约与骨架

- 建立 Go 模块、API/Worker 命令、配置加载、统一错误和 health。
- 建立 PostgreSQL/pgvector、Goose 迁移骨架和 repository 边界。
- 生成 OpenAPI 服务端/客户端骨架；SSE 事件模型先行。
- 实现本地身份适配器和测试 workspace seeding。
- 完成脱敏 Zotero fixture、golden set schema 和一个最小 golden question。
- 完成 `make eval-rag`、`make eval-retrieval`、`make eval-brief` 和 stage-0 smoke test 入口。

### 阶段 1：Zotero 同步与索引闭环

- 实现 `WebAPISource`、凭据加密、全量/增量同步、游标和 429 重试。
- 实现文档版本、chunk、embedding、全文/摘要/仅元数据状态，以及 building → active 的 `index_epoch` 原子发布。
- 实现覆盖状态、tombstone、River 任务和清理截止时间。
- 以 fixture 验证重复同步不产生重复文档、chunk 或向量。

### 阶段 2：个人文库 RAG

- 实现 Projects、query scope、混合检索、RRF 和 rerank fallback。
- 实现 QuerySession、QueryEvent、Brief、claim-level Evidence 和 claim_evidence 关系。
- 完成 `my_library`、SSE 过程展示、claim grounding、引用完整性和证据充分性。
- 运行小型、合规、脱敏 golden set。

### 阶段 3：外部发现与全景模式

- 实现 Semantic Scholar、OpenAlex 和外部工作归一化。
- 实现 `gap_fill` 的三类缺口和库内外去重。
- 实现 Qwen/DashScope 原生联网搜索和 `web_search_unavailable` 降级。
- 完成限流、冲突、来源状态、引用图扩展和外部结果只读导出。
- 增加 `gap_fill`、`landscape`、时间型和冲突型 RAG 评测，只有指标改善才扩大 Web Search 使用范围。

### 阶段 4：生命周期、试点与部署

- 完成删除队列、30 天清理、备份/恢复演练和审计。
- 完成 staging、指标、日志、trace 和告警。
- 进行 3–5 人试点，评审是否需要 OAuth、Connector 或其他扩展。

## 7. Definition of Ready

进入编码前必须具备：

- PRD、架构基线、设计包和 OpenAPI 骨架版本一致。
- 所有模块接口、枚举、数据库表和生命周期规则已冻结。
- 当前垂直切片已有 `Ready` 状态的 Change Spec，验收条件、测试映射、迁移/隐私/回滚影响完整。
- 本地 fixture、Provider stub 和 golden set schema 可以离线运行。
- RAG 指标、hard gate、baseline 比较和机器可读报告格式已冻结。
- 阶段 0 smoke test 的组成、通过条件和日志检查已写明。
- 不把真实用户数据、真实 PDF、真实 API Key 或未脱敏查询写入仓库。

## 8. 可交付边界

本设计包只交付文档和 OpenAPI 契约，不交付应用代码、迁移文件、部署配置或真实密钥。阶段 0 之后的代码必须遵循本目录文档；任何改变架构边界的实现必须先新增 ADR。
