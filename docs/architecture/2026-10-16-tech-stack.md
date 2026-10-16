# Research Scout 架构基线 v1.1：Zotero Web API-first

| 项目 | 内容 |
|---|---|
| 文档版本 | v1.1 |
| 状态 | 架构基线冻结；模块设计已展开，变更需 ADR |
| 日期 | 2026-10-16 |
| 关联 PRD | [`docs/prd/2026-10-16-zotero-research-assistant-prd.md`](../prd/2026-10-16-zotero-research-assistant-prd.md) |
| 目标 | 冻结 v1 系统边界、技术栈、Zotero 接入边界、数据与部署方向；技术细节见设计包 |
| 非范围 | UI 视觉规范、Connector 协议、OAuth 实现；字段、接口和模块细节见 [`docs/design/`](../design/README.md) |
| v1 Zotero 路径 | Zotero Web API，仅个人 My Library |
| v1 客户端 | Web 应用；Connector 不进入 v1 实现和验收 |

---

## 1. 决策摘要

### 1.1 一句话架构定义

Research Scout v1 采用 **Go 模块化单体（API + Worker）+ PostgreSQL/pgvector + React/Vite Web**，通过服务端代理 Zotero Web API，将用户个人 My Library 的元数据和可用 fulltext 增量索引为可追溯证据，并在项目级 collection scope 内执行三档检索与有界 RAG；Connector 仅保留为未来可能增加的可选本地读入实现。

### 1.2 基线决策

| 领域 | v1 决策 | 理由 |
|---|---|---|
| 架构形态 | 模块化单体，同仓多交付物 | 3–5 人试点不需要微服务；共享事务和领域边界更重要 |
| 服务端 | Go 1.25 + Gin | 适合同步、任务、检索编排和单二进制部署，并与现有技术经验一致 |
| 数据访问 | GORM；复杂全文/向量 SQL 封装在 repository | 统一事务与开发速度；不引入 pgx/sqlc 双轨 |
| 数据库 | PostgreSQL 16 + pgvector | 元数据、任务、审计、FTS 和向量集中在一个事务边界 |
| 迁移 | Goose | 版本化迁移简单可审计 |
| Web | React 19 + TypeScript + Vite + React Router 7 | 登录后工作台，无 SSR/SEO 刚需，构建边界清晰 |
| Web 数据层 | TanStack Query + 原生 fetch | 服务端状态、轮询、缓存和 SSE 状态易于管理 |
| UI | Tailwind CSS + shadcn/ui 方向 | 组件源码可控，适合快速形成内部工作台 |
| API 契约 | OpenAPI 3.1 + REST + SSE | Web 与未来 Connector 均可复用；流式进度使用 SSE |
| 后台任务 | PostgreSQL-backed durable queue，首选 River | 任务与业务数据同事务，不先引入 Redis/Kafka |
| Zotero 接入 | `ZoteroSource` 领域边界；v1 仅实现 `WebAPISource` | 隔离来源差异，未来 Connector 只能成为另一实现 |
| Zotero 范围 | 个人 My Library；v1 不支持 group libraries | 权限、同步和产品边界最清晰 |
| Zotero 同步 | 按需 + 手动刷新；library version + `?since=` 增量 | 不引入 Webhook 和定时全量轮询，降低试点复杂度 |
| Zotero 凭据 | Go 服务端加密保存并代理调用；开发期只读 API Key，试点前 OAuth 1.0a | 浏览器不持有长期 Key，调用和撤权集中治理 |
| 证据类型 | `personal_fulltext`、`structured_abstract`、`web_context` | 个人全文优先；摘要降级；网页只提供背景 |
| 检索模式 | `my_library`、`gap_fill`、`landscape` | 用户明确选择范围和外部来源权限 |
| 外部来源 | Semantic Scholar + OpenAlex；原生联网搜索仅全景模式 | 结构化发现优先；网页只提供背景，不单独支撑科学结论 |
| LLM/Embedding | OpenAI 兼容 Provider；默认 Qwen/DashScope | 默认 `qwen-plus`、1024 维 Qwen Embedding、`qwen3-rerank`，均可替换 |
| Agent | Go 内有界状态机，不使用自由 Agent 循环 | 成本、权限、证据和失败路径可测试 |
| 身份 | v1 独立开发；不接 Lab Nexus、不实现 OIDC | 身份不是当前关键路径；保留 `actor_id`/`workspace_id` 适配边界 |
| 本地验证 | PostgreSQL 容器 + Go/Web 原生运行 | 聚焦集成调试，降低开发环境复杂度 |
| 部署方向 | 单机 Linux + Docker Compose + Caddy | 试点规模不需要 Kubernetes；公网部署不阻塞当前开发 |
| 可观测性 | JSON 日志 + Prometheus 指标 + OpenTelemetry trace | 追踪同步、检索和外部调用，同时避免记录完整私人全文 |

### 1.3 已明确否决

| 替代方向 | 否决原因 |
|---|---|
| v1 强制安装 macOS Connector | Local API 只存在于用户机器；先引入安装、签名、公证、更新和配对，会显著扩大首版闭环 |
| 浏览器直接访问 Zotero Local API | 本地 API 的 Origin 行为和跨源限制不可控，也不适合集中部署 |
| 把 Local API demo 当作线上接入方案 | demo 证明本地读取可行，没有解决公网服务访问用户本机的问题 |
| Lab Nexus 强制 OIDC | 两边都在开发中，接口尚未稳定；v1 不应把身份集成变成阻塞项 |
| 浏览器持久化 Zotero API Key | 泄露面、撤权和审计边界不可接受 |
| pgx + sqlc | 增加一套与 GORM 并行的数据访问方式，收益不足以抵消复杂度 |
| Redis/Kafka、独立向量库、Elasticsearch | 当前规模可由 PostgreSQL 承担，减少有状态依赖 |
| Connector、PDF 解析、批注/笔记索引进入 v1 | 不是验证“个人文库 + 外部发现 + 简报”价值的必要路径 |
| Tavily/Brave/自建爬虫作为默认联网方案 | 主流模型已有原生搜索 grounding；先复用能力，失败时显式降级 |
| 开发者维护新的中央语料库 | 版权、全文获取、存储和覆盖责任不可控，已由产品方向排除 |

---

## 2. 背景、事实与约束

### 2.1 Local API demo 的结论

本地原型已经验证：

- Zotero 桌面端可以提供 collection、item 元数据和 fulltext。
- 一个运行在用户 Mac 上的本地进程可以代理 `http://localhost:23119`，解决浏览器直连时的跨源和 Origin 问题。
- 因此，Local API 可以作为未来 Connector 的技术基础。

但本地原型同时证明：

- `localhost:23119` 只在用户电脑上存在。
- 集中部署的公网服务无法访问用户的 loopback 地址。
- 浏览器直连 Local API 受到跨源、Private Network Access 和 Origin 行为约束。
- 要让服务端读取本地-only 内容，必须额外开发、分发和维护一个 Connector。

**架构结论**：Local API demo 是 Connector 原型，不是 v1 的集中部署路径。本期不开发 Connector，不设计设备配对、安装包、签名、公证、自动更新或本地同步协议。

### 2.2 Zotero Web API 的 v1 边界

v1 使用官方 Zotero Web API：

- 基址：`https://api.zotero.org`
- API 版本头：`Zotero-API-Version: 3`
- 认证：服务端使用用户授权的只读凭据调用
- 分页：每页最多 100，使用 `Link`/`Total-Results` 读取完整集合
- 限流：遵守 `429`、`Retry-After` 和 `Backoff`，并发不超过 4
- 增量：使用 library version、`?since=` 和 `format=versions` 获取变更
- 删除：读取 deleted 数据并在本地建立 tombstone
- 全文：尝试读取 Zotero 服务端可用的 fulltext；附件没有可用全文时按正常缺省处理

必须明确的产品事实：

- Web API 只覆盖已经同步到 `zotero.org` 的数据。
- 题录同步不等于 PDF 或 fulltext 一定同步。
- 本地-only 文献、未同步附件和 Zotero 服务端无 fulltext 的条目属于已知缺口。
- 系统不得把摘要或元数据伪装成 `personal_fulltext`。
- 全文缺失不阻断条目同步、元数据检索和摘要级回答。

### 2.3 产品与规模约束

- v1 目标用户是 3–5 名信息安全课题组学生与教师。
- 使用方式是公网 HTTPS Web 应用，用户无需安装桌面客户端。
- 每个人只访问自己的 workspace、项目、Zotero 连接和派生索引。
- 不保存 PDF 二进制；不提供 PDF 上传；不解析 PDF；不索引 Zotero 批注和笔记。
- 外部模型只处理检索命中的必要片段，并受首次知情同意和日志脱敏约束。

---

## 3. 系统上下文

```text
                         ┌──────────────────────────┐
                         │      React/Vite Web      │
                         │ 工作台 / 会话 / 简报 / 设置 │
                         └────────────┬─────────────┘
                                      │ HTTPS REST + SSE
                         ┌────────────▼─────────────┐
                         │   Research Scout Core    │
                         │ Go API + Worker（单体）    │
                         │ 身份适配 / 项目 / Agent    │
                         └───┬────────┬────────┬────┘
                             │        │        │
                 ┌───────────▼──┐ ┌───▼────┐ ┌▼──────────────┐
                 │ PostgreSQL   │ │ Zotero │ │ External       │
                 │ + pgvector   │ │ Web API│ │ Providers      │
                 │ 业务/任务/索引 │ │        │ │ S2/OpenAlex/Web │
                 └──────────────┘ └───┬────┘ └────────────────┘
                                      │
                         ┌────────────▼─────────────┐
                         │ 用户已同步的个人文库       │
                         │ My Library，非 group      │
                         └──────────────────────────┘

未来可选扩展（不在 v1 接口和排期中）：
[Local Connector] ──> 同一 ZoteroSource 边界
```

### 3.1 运行时单元

| 单元 | 职责 | v1 形态 |
|---|---|---|
| Web | 登录占位、工作台、提问、过程、简报、设置 | React 静态资源 |
| API | 鉴权占位、项目、连接、查询、同步控制、审计 | Go 进程 |
| Worker | Zotero 同步、索引、embedding、外部发现、简报任务 | 同仓 Go 进程 |
| PostgreSQL | 业务数据、任务、FTS、向量、审计、生命周期 | 容器 |
| Zotero Web API | 个人文库事实源 | 外部服务 |
| Provider adapters | S2、OpenAlex、联网搜索、LLM、Embedding | 外部服务 |

API 与 Worker 共享领域模块和数据库，不共享进程内状态。它们可以部署为同一镜像的两个启动模式。

---

## 4. 模块边界

### 4.1 Identity/Workspace Shell

v1 不接 Lab Nexus、不实现 OIDC。开发期使用可替换的身份适配器：

- `actor_id`：当前用户或服务身份。
- `workspace_id`：数据与授权边界。
- `role`：普通用户或显式系统管理员。
- 所有查询和写入必须由服务端注入这些边界，不能信任前端传入。

未来接入 Lab Nexus 或其他 IdP 时，只替换身份适配器，不改变业务对象和授权检查位置。

### 4.2 Zotero 模块

定义统一领域边界 `ZoteroSource`：

- 描述连接和可访问文库。
- 列出 collection 及层级。
- 读取 library 当前版本和增量变更。
- 读取 item/attachment 元数据。
- 读取服务端可用 fulltext。
- 返回规范化错误，不把供应商错误泄漏到领域层。

v1 唯一实现是 `WebAPISource`。未来 Connector 只能是另一个实现，不改变 `IndexedDocument`、`Evidence` 和同步任务语义。

### 4.3 同步与索引模块

职责：

- 管理连接、同步游标、重试和覆盖状态。
- 规范化 Zotero item、attachment、collection 关系。
- 计算内容 hash，执行版本化写入。
- 生成 chunk 和 embedding。
- 产出 `personal_fulltext` 或 `structured_abstract` 证据状态。
- 处理 tombstone、重命名、移动和删除。

### 4.4 检索与证据模块

职责：

- 强制应用 workspace、用户、项目、collection scope、tombstone 和版本过滤。
- 执行 PostgreSQL FTS、pgvector HNSW 和 RRF 融合。
- 按需调用 reranker adapter。
- 将候选组织为带来源、版本和定位信息的 `Evidence`。
- 对结果执行去重、冲突标记和证据充分性判断。

### 4.5 Agent/QuerySession 模块

采用有界状态机：

```text
受理
 -> 意图与模式校验
 -> SearchPlan
 -> 工具执行
 -> 结果归一化与去重
 -> 证据选择与重排
 -> 简报生成
 -> 引用与证据校验
 -> 持久化与完成
```

工具权限由服务端根据模式强制：

- `my_library`：只允许个人文库检索。
- `gap_fill`：个人文库 + S2/OpenAlex。
- `landscape`：个人文库 + S2/OpenAlex + 联网搜索。

Agent 可以建议升级模式，但必须由用户确认，不能静默扩大范围。

### 4.6 外部来源模块

统一适配边界：

- `ScholarlyDiscoverySource`：Semantic Scholar、OpenAlex。
- `WebSearchProvider`：模型供应商原生联网搜索或未来独立搜索实现。
- `LLMProvider`：生成、结构化输出和可选重排。
- `EmbeddingProvider`：向量化。

v1 首个联网实现是 `QwenNativeWebSearch`。未支持原生搜索时返回 `web_search_unavailable`，不静默依赖第三方搜索。外部结果必须归一化到内部 `Work`、规范化论文标识和 `Evidence`。供应商 ID、DOI、年份、venue、原始 URL 和获取时间必须保留。

### 4.7 生命周期与审计模块

- 用户删除、撤权和主动清理进入统一生命周期流程。
- 文档状态使用 `active | removed_pending_purge | purged`。
- v1 使用明确的删除截止时间和可重试清理任务。
- 管理员访问派生内容必须记录 actor、workspace、对象、原因、时间和请求标识。
- 用户能看到与自己相关的管理员访问记录。

---

## 5. 数据架构

### 5.1 存储边界

| 数据 | v1 保存位置 | 服务端是否接收 PDF | 说明 |
|---|---|---:|---|
| Zotero 连接和凭据 | PostgreSQL，加密保存 | 否 | 服务端调用 Web API |
| Zotero item/attachment 标识 | PostgreSQL | 否 | 追溯与幂等同步 |
| 标准元数据 | PostgreSQL | 否 | 标题、作者、年份、venue、DOI 等 |
| 服务端可用 fulltext | PostgreSQL `text` | 否 | 作为派生文本保存 |
| Chunk | PostgreSQL | 否 | 保留文档、版本、序号和可用位置 |
| Embedding | PostgreSQL + pgvector | 否 | 记录 provider、model、维度、版本 |
| 查询、会话、简报 | PostgreSQL | 否 | 用户可见，可删除 |
| 审计日志 | PostgreSQL | 否 | 独立访问记录 |
| 原始网页 | 不长期保存 | 否 | 只保留引用所需 URL、标题、时间和短片段 |
| 原始 PDF | 用户 Zotero | 否 | 产品不接收、不解析、不托管 |

v1 不引入对象存储。试点数据量和恢复目标若出现瓶颈，再通过 ADR 评估。

### 5.2 Zotero 同步算法

1. 用户提交只读凭据或完成 OAuth 1.0a 授权。
2. 服务端验证连接，读取个人 My Library 和 collection 树。
3. 首次同步读取完整元数据、collection 关系和可用 fulltext。
4. 后续同步读取 library version 和 `?since=` 变更。
5. 对 Zotero 删除和连接撤权的对象建立 tombstone；项目/查询 scope 调整只改变过滤条件。
6. 对内容 hash 变化的文档重新分块和 embedding。
- 无 fulltext 但有摘要时保留 `structured_abstract` 证据；无摘要时标记 `metadata_only`。
8. 每次同步记录游标、错误、成功数量和覆盖状态，支持断点续跑。

关键约束：

- 重复同步必须幂等。
- 429 使用 `Retry-After`，无该头时指数退避。
- 并发不超过 4。
- 单个条目失败不阻断整个文库。
- 不能把“未取得全文”写成同步失败或检索失败。

### 5.3 索引流水线

```text
Web API 连接
  -> library/version 增量发现
  -> item/attachment 元数据归一化
  -> fulltext 可用性检查与 hash
  -> 结构感知 chunk
  -> embedding
  -> 版本校验与发布
  -> 覆盖状态更新
```

- 文档只有完整发布后才进入检索。
- hash 未变化时不重新 embedding。
- 模型版本变化时创建新向量版本，不原地覆盖。
- collection scope 只影响查询过滤，不复制出多份文档。
- 同一论文出现在多个 collection 中时，只保留一个文档实体和多条 scope 关系。

### 5.4 删除与 tombstone

| 事件 | 立即行为 | 最终清理 |
|---|---|---|
| Web API 返回删除 | 文档移出检索 | 30 天后清除派生数据 |
| 用户撤权或断开连接 | 连接失效，内容移出检索 | 30 天后清除派生数据 |
| 项目/查询 scope 调整 | 立即改变后续查询过滤 | 不清除个人文库索引 |
| 主动清空索引 | 立即不可检索 | 立即清除 |
| 删除项目/会话 | 立即不可见 | 30 天后清除 |
| 删除账户 | 撤销会话 | 30 天内清除全部派生数据 |

清理范围包括派生全文、chunk、向量、scope 关系和缓存；不触碰用户 Zotero。

---

## 6. 检索与回答

### 6.1 检索模式

| 模式 | 个人文库 | S2/OpenAlex | 联网搜索 | 输出 |
|---|:---:|:---:|:---:|---|
| `my_library` | ✓ |  |  | 个人文库问答与综合 |
| `gap_fill` | ✓ | ✓ |  | 库内外对照与缺口 |
| `landscape` | ✓ | ✓ | ✓ | 全景简报 |

### 6.2 证据规则

```text
EvidenceType = personal_fulltext | structured_abstract | web_context
```

| 证据类型 | 来源 | 可作为科学结论依据 |
|---|---|---:|
| `personal_fulltext` | Zotero Web API 可用 fulltext | 是，最高优先级 |
| `structured_abstract` | Zotero 元数据、S2 或 OpenAlex | 是，但必须标注摘要级 |
| `web_context` | 模型供应商原生联网搜索 | 否，只能提供背景和线索 |

- 联网上下文不能单独支撑无保留的科学事实。
- 证据不足时必须明确说明，并输出未决问题或缺口。
- 外部发现不得改变个人全文的证据层。
- 引用必须保留标题、年份、来源、DOI/URL 和可用的文档内定位。

### 6.3 混合检索

1. 应用 workspace、用户、项目、scope、tombstone 和版本过滤。
2. PostgreSQL FTS 检索标题、元数据和 chunk 文本。
3. pgvector HNSW 做语义近邻检索。
4. 使用 RRF 融合关键词和向量排名。
5. 调用 reranker adapter 重排候选。
6. 组装证据包并执行引用完整性、冲突和覆盖检查。

`landscape` 的联网上下文来自 `WebSearchProvider`，默认复用 Qwen/DashScope 的原生搜索能力。网页结果必须带 URL 和获取时间，只能作为 `web_context`；发现 DOI/论文链接后再次查询 S2/OpenAlex。

v1 不发放独立 Elasticsearch、Qdrant 或 Milvus。只有实测出现明确瓶颈时才通过 ADR 引入。

---

## 7. 身份、凭据与安全

### 7.1 身份边界

- v1 独立运行，不接 Lab Nexus，不要求 OIDC。
- 开发期可以使用本地身份适配器、测试用户或内部服务身份。
- 业务层只依赖 `actor_id`、`workspace_id` 和显式角色。
- 未来接入外部 IdP 时，只替换适配器，不改变租户和审计边界。

### 7.2 Zotero 凭据

开发期：

- 用户提交只读 Zotero Web API Key。
- API Key 只在服务端加密保存，浏览器不得持久化。
- 所有 Zotero 请求由 Go 服务端代理。

正式试点前：

- 切换为 Zotero OAuth 1.0a，获得只读授权。
- 支持撤销连接和清理凭据。
- Zotero OAuth 2.0 尚不是当前可用前提，不能把它写成 v1 必需能力。

未来 Connector 若实施，应使用独立设备授权和最小权限凭据，不进入本次架构冻结范围。

### 7.3 安全控制

- 所有公网通信使用 HTTPS。
- API 和 Worker 均执行服务端授权，不能只依赖前端隐藏按钮。
- 凭据、密钥和 token 不写入仓库、日志或前端 bundle。
- 日志不得记录完整私人全文和用户原始问题，除非用户明确启用调试并知情同意。
- 管理员访问派生内容必须重新认证并生成审计事件。
- 删除操作必须在活跃索引、向量、缓存和会话关联中一致执行。
- 搜索和模型请求必须可关闭、可降级、可观测。

---

## 8. 接口与契约

采用 OpenAPI 3.1 描述 REST 接口，SSE 单独定义事件契约。v1 不规定具体数据库字段和完整请求体，只冻结接口域：

| 接口域 | 责任 |
|---|---|
| Identity/Workspace | 当前 actor、workspace、角色和开发身份适配 |
| Zotero Connections | 连接、验证、撤权、同步状态和覆盖信息 |
| Collections/Scopes | collection 树、项目 scope 和查询 scope |
| Projects/Sessions | 项目、查询会话、追问和过程状态 |
| Briefs/Evidence | 简报、证据、引用和缺口 |
| Discovery | S2/OpenAlex 结果、归一化和去重 |
| Lifecycle/Audit | 删除、清理、管理员访问记录 |
| Health/Operations | 健康检查、任务状态和指标入口 |

SSE 只承载受理、阶段、工具状态、降级和完成事件，不传递隐藏推理链。

---

## 9. 部署与运行

### 9.1 本地验证

- PostgreSQL/pgvector 使用容器。
- Go API、Go Worker 和 React Web 原生运行。
- 使用开发身份适配器和测试 workspace。
- 外部供应商凭据通过本地环境变量或 secret 文件注入。
- 不启动 Connector、不依赖本机 Zotero Local API。

### 9.2 后续单机部署

架构方向是单台 Linux VM + Docker Compose + Caddy：

- Caddy 终止 TLS 并提供同源 Web 静态资源。
- API 和 Worker 使用同一镜像的不同命令。
- PostgreSQL/pgvector 作为同机服务，配置卷和每日备份。
- 远程日志、指标和告警按试点需求逐步接入。
- 不在本轮实施部署、不冻结域名和服务器供应商。

### 9.3 备份与恢复

- 备份必须覆盖 PostgreSQL 业务数据、派生索引和任务状态。
- PDF 不在服务端，因此不存在 PDF 备份责任。
- 恢复演练至少验证连接、scope、派生文本、向量和删除状态的一致性。
- 备份保留期和 RPO/RTO 在部署设计阶段锁定。

---

## 10. 测试与质量

| 层级 | 工具/方法 | 重点 |
|---|---|---|
| Go 单元测试 | `go test` | scope、状态机、同步幂等、删除规则、证据分层 |
| 数据库集成测试 | testcontainers + PostgreSQL/pgvector | FTS、向量、事务、迁移、并发任务 |
| Zotero Web API 合同测试 | fixture + 沙箱/脱敏响应 | 分页、since、deleted、429、fulltext 缺失 |
| Provider 合同测试 | 适配器 fixture | S2/OpenAlex/搜索/LLM 超时和降级 |
| Web E2E | Playwright | 连接、同步状态、提问、过程、简报、清理 |
| 检索质量 | 小型合规 golden set | 引用可追溯、证据充分性、缺口判定 |
| 安全测试 | 授权矩阵 + 脱敏日志检查 | workspace 隔离、凭据、审计、删除 |

禁止测试用例把真实 PDF 或用户私人全文提交到仓库。

---

## 11. 建议仓库结构

```text
research-scout/
├── cmd/
│   ├── api/
│   └── worker/
├── internal/
│   ├── identity/
│   ├── workspace/
│   ├── zotero/
│   │   ├── source.go          # ZoteroSource
│   │   └── webapi/            # v1 WebAPISource
│   ├── sync/
│   ├── indexing/
│   ├── retrieval/
│   ├── evidence/
│   ├── agent/
│   ├── discovery/
│   ├── brief/
│   ├── lifecycle/
│   └── audit/
├── migrations/
├── web/
├── openapi/
├── docs/
│   ├── prd/
│   └── architecture/
└── deploy/
    └── compose/
```

未来 Connector 若实施，应作为独立交付物接入 `internal/zotero` 的同一领域边界，不能把 Local API 逻辑写进 API/Worker。

---

## 12. 架构决策记录

| ADR | 决策 | 状态 |
|---|---|---|
| ADR-001 | Go 模块化单体，API + Worker 同仓 | 已采用 |
| ADR-002 | PostgreSQL/pgvector 作为唯一数据核心 | 已采用 |
| ADR-003 | GORM 统一数据访问，复杂 SQL 封装在 repository | 已采用 |
| ADR-004 | PostgreSQL-backed 任务队列优先 River | 已采用 |
| ADR-005 | React 19 + Vite + TanStack Query | 已采用 |
| ADR-006 | OpenAPI 3.1 + REST + SSE | 已采用 |
| ADR-007 | v1 仅使用 Zotero Web API，个人 My Library | 已采用 |
| ADR-008 | Connector 仅作为未来可选实现 | 已采用 |
| ADR-009 | 服务端加密保存 Zotero 凭据并代理调用 | 已采用 |
| ADR-010 | 开发期 API Key，试点前 OAuth 1.0a | 已采用 |
| ADR-011 | v1 不接 Lab Nexus、不实现 OIDC | 已采用 |
| ADR-012 | 不保存 PDF、不解析 PDF、不索引批注/笔记 | 已采用 |
| ADR-013 | 证据分层和三种检索模式固定 | 已采用 |
| ADR-014 | 本地验证优先，后续单机 Docker Compose | 已采用 |
| ADR-015 | 优先复用模型供应商原生联网搜索，默认 Qwen/DashScope | 已采用；见 [`ADR-015`](./adr/ADR-015-provider-native-web-search.md) |

任何改变上述边界的方案必须新增 ADR，不能以“顺手扩展”方式绕过。

---

## 13. 实施顺序

### 阶段 0：架构与协议验证

1. 用脱敏 fixture 验证 Zotero Web API 的 collections、items、`since`、`format=versions`、deleted 和 fulltext 行为。
2. 验证 API Key 服务端加密、注销和 429 退避。
3. 建立 OpenAPI 骨架、PostgreSQL migration 和 Worker 任务模型。
4. 验证 pgvector 维度、FTS 和 scope 过滤在个人文库样本上的延迟。

### 阶段 1：Zotero 同步与索引闭环

- 完成开发身份适配器和 workspace 边界。
- 完成 Web API 连接、My Library 全量同步和增量同步。
- 完成元数据、available fulltext、chunk、embedding 和覆盖状态。
- 完成本地-only 缺口提示、tombstone 和清理任务。

### 阶段 2：个人文库 RAG

- 完成项目、collection scope、QuerySession 和 ResearchBrief。
- 完成 FTS + pgvector + RRF + reranker adapter。
- 完成有界状态机、SSE 过程展示、证据分层和引用校验。
- 验证 `my_library` 模式和摘要降级。

### 阶段 3：外部发现与全景调研

- 接入 Semantic Scholar、OpenAlex。
- 完成 DOI/标题归一化、库内外去重和三类缺口。
- 接入 Qwen/DashScope Provider-native Web Search，限制为 `landscape`。
- 完成来源限流、降级和冲突展示。

### 阶段 4：生命周期、试点与部署

- 完成审计、删除队列、30 天清理和恢复演练。
- 完成 staging、备份、监控和告警。
- 进行 3–5 人试点，决定是否需要 Connector 等后续能力。

---

## 14. 待确认项

以下问题不改变架构边界，只影响后续技术设计：

| 问题 | 影响 | 当前默认 |
|---|---|---|
| Zotero OAuth 1.0a 的具体接入库和回调域名 | 试点前后台登录和撤权 | 开发期 API Key；试点前 OAuth |
| Web API fulltext 的实际覆盖率 | 影响 `personal_fulltext` 比例 | 不绕过 Web API，缺口显式可见 |
| 首个 LLM/Embedding provider 和数据出境策略 | 成本、延迟、隐私声明 | Provider adapter；优先评估 DashScope/Qwen |
| 联网搜索 provider | `landscape` 质量和可用性 | 默认 Qwen/DashScope 原生搜索；不可用时显式降级 |
| 远程部署域名、主机和 TLS | 后期部署与备份 | 本轮不实施 |
| 备份保留期和 RPO/RTO | 运维和合规 | 部署设计阶段确定 |
| 个人文库最大规模 | worker 并发和索引成本 | 先按试点数据，后续根据指标调整 |

---

## 15. 结论

v1 的核心是先把“用户已有文库、外部发现、证据化简报”做成可运行的闭环，而不是先解决所有来源和本地安装问题：

- Zotero Web API 提供 v1 可集中部署的事实源。
- `ZoteroSource` 隔离未来 Connector，不让本地 API 绑死当前架构。
- Go 模块化单体负责同步、索引、检索和 Agent。
- PostgreSQL/pgvector 作为唯一可验证数据状态。
- React/Vite Web 提供工作台和过程可见性。
- S2、OpenAlex、原生联网搜索和模型都是可替换适配器。
- 本地-only 缺口被明确展示，而不是被伪装成覆盖完整。
- v1 先独立于 Lab Nexus 和 Connector 建立产品闭环，架构保留未来替换和扩展边界。
