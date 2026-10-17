# Research Scout

面向信息安全课题组的个人文献研究助理。

Research Scout 以用户个人 Zotero Web API 文库中的可用全文与元数据为个人证据，
结合 Semantic Scholar、OpenAlex 和模型供应商原生联网搜索，在显式研究范围内生成带证据分层、
引用和缺口清单的研究简报。

## 产品方向

- [产品需求文档（PRD v1.4）](docs/prd/2026-10-16-zotero-research-assistant-prd.md)
- [架构基线 v1.1](docs/architecture/2026-10-16-tech-stack.md)
- [技术设计包 v1.1](docs/design/README.md)
- [OpenAPI 3.1 契约骨架](openapi/research-scout.yaml)
- [阶段 0 Change Spec（状态：Verified）](docs/changes/RS-SPEC-0001-stage0-smoke.md)
- 当前阶段：产品、架构和模块技术设计已冻结；**阶段 0 本地 Smoke 闭环已实现并通过验证**。

v1 使用 **Zotero Web API + 个人 My Library**，不开发或不要求安装 macOS Connector。
Connector 仅保留为未来可选扩展，继续复用统一的 `ZoteroSource` 领域边界。

原项目的 57,343 篇中央摘要库及其向量数据已删除，旧代码只保留作历史参考和离线评测基准，
不作为 Research Scout 的迁移来源或线上语料依赖。

## 核心边界

- Zotero 只读，不回写、不修改 collection，不删除条目。
- v1 只支持个人 My Library，不支持 group libraries。
- 服务端不保存 PDF，不解析 PDF，不提供手动 PDF 上传。
- Web API 可取 fulltext 时标记为 `personal_fulltext`；否则在有摘要时降级为 `structured_abstract`，仅元数据时标记为 `metadata_only`（不作为科学结论证据）。
- 本地-only、未同步或服务端无 fulltext 的内容作为已知缺口展示，不承诺覆盖全部 Zotero 资料。
- v1 独立开发，不接 Lab Nexus、不实现 OIDC；保留可替换身份适配器。
- v1 不包含站内分享、公共库、批注/笔记索引和 Zotero 写回。

## 本地运行（阶段 0）

阶段 0 用脱敏 Zotero fixture 跑通「迁移 → workspace → 建索引 → `my_library` 查询 → 可追溯 Brief → RAG 评测」，
**不访问真实 Zotero、LLM、Embedding、Rerank、Semantic Scholar、OpenAlex 或联网搜索**。

前置条件：Docker（含 Compose）可运行，Go 1.25，Ruby（`make openapi-lint` 解析 YAML）。

```bash
# 1. 启动本地 PostgreSQL 16 + pgvector（数据库端口映射到宿主机 55433）
make db-up

# 2. 执行迁移（幂等，可重复运行）
make migrate

# 3. 跑通阶段 0 Smoke：seed workspace、导入 fixture、建索引、发布 active epoch、生成可追溯 Brief
make smoke-stage0

# 4. 运行最小 RAG 评测，产出机器可读报告（eval/reports/）
make eval-rag

# 5. 全量门禁：OpenAPI lint + 单元测试 + 集成测试 + 契约测试
make verify
```

真实外部来源（Semantic Scholar / OpenAlex / Qwen 联网搜索）默认关闭，不参与 CI。
需要人工确认真实调用时，用成本受控的一次性 smoke：每个来源最多 1 次请求，只输出状态与公开计数，
不落库、不打印任何密钥（联网搜索需显式加 `-web`）。

```bash
# 只查结构化来源（无需数据库）
go run ./cmd/worker discovery-smoke
# 额外做 1 次联网搜索（需 DASHSCOPE_API_KEY 等已配置）
go run ./cmd/worker discovery-smoke -web
```

真实来源只在确认要调用时才通过环境变量开启：`S2_API_KEY`、`OPENALEX_EMAIL`、
`DASHSCOPE_API_KEY`、`DASHSCOPE_BASE_URL`、`QWEN_SEARCH_MODEL`，以及 `DISCOVERY_ENABLED`、
`WEB_SEARCH_ENABLED`。

启动 API：

```bash
RS_DATABASE_URL='postgres://research:research@localhost:55433/research_scout?sslmode=disable' \
AUTO_MIGRATE=true \
HTTP_ADDR=':8080' \
go run ./cmd/api
# 健康检查：GET /healthz、GET /readyz、GET /metrics、GET /api/v1/me
```

默认连接串为 `postgres://research:research@localhost:55433/research_scout?sslmode=disable`；
可用 `RS_DATABASE_URL`（或 `DATABASE_URL`）覆盖。`make` 目标同样接受 `DB_URL=...`。
端口使用 55433 是为了避开宿主机其它 PostgreSQL 实例（`55432` 可能已被占用）。

停止本地依赖：`make db-down`（保留数据卷）；需要彻底重置时用 `docker compose down -v`。

## 阶段 0–3 的边界

阶段 0（本地闭环）、阶段 1（Zotero Web API 同步与生命周期）、阶段 2（个人文库 RAG：检索规划、
混合检索与 rerank 回退、scope 管理、可追溯 Brief 与生命周期路由）、阶段 3（外部发现：Semantic Scholar
与 OpenAlex 归一化与去重、三类缺口判定、`gap_fill` / `landscape` 模式、Provider-native Web Search 边界、
来源状态显式降级与冲突证据保留）均已实现，可离线复现。

已实现（可用 `make verify`/`make smoke-stage0` 复现）：

- Go 模块化单体骨架：`cmd/api`（REST + SSE）、`cmd/worker`（migrate/seed/import/smoke/eval）。
- PostgreSQL 16 + pgvector 迁移与核心 repository，`building → active` 的不可变 `index_epoch` 原子发布。
- 脱敏 Zotero fixture 导入：文档、版本、chunk、metadata-only 覆盖状态，重复导入幂等。
- 最小 `my_library` 查询：确定性检索、Brief、Evidence、Claim 与 claim-level `claim_evidence` 追溯。
- 硬门禁：`metadata_only` 不得进入 evidence/科学结论；每个 claim 必须有证据。
- 本地身份适配器、`/healthz`、`/readyz`、`/metrics`、统一错误模型和请求 ID。
- OpenAPI 3.1 路由契约测试、golden set、机器可读 RAG 评测报告与 hard gate。

尚未实现（后续阶段）：

- Zotero OAuth 1.0a（开发期使用只读 API Key；OAuth 流程留待试点前 ADR）。
- 真实 LLM 生成层（Brief 仍用确定性证据抽取）、真实 Embedding 与 Rerank Provider 接线。
- 联网搜索的真实凭据联通（现有 DashScope 凭据仅覆盖自建 rerank 端点，返回 401，显式降级）。
- 引用图二次召回（`/discovery/works/{id}/citations` 目前只做只读透传）。
- Web 工作台（`web/` 仍为空壳）、研究简报 UI、SSE 前端消费。
- 多租户权限、管理员审计、30 天清理调度、备份恢复与生产部署（Docker Compose 单机）。

## 开发与工程规范

- [SDD + TDD 开发规范 v1.0](docs/development/2026-10-16-sdd-tdd-development-standard.md)
- [Agent 强制约束](AGENTS.md)

所有实现采用规格驱动与测试驱动：Change Spec 通过 Ready 门禁后才写业务代码；
每个行为先有失败的 Red 测试，再以最小实现进入 Green，重构后复跑完整回归。

RAG 检索、Agent、Embedding 或 Prompt 变更必须提交 `index_epoch`/版本信息，并通过 `make eval-rag` 与质量 Gate。

## 技术基线

架构基线（设计冻结，见 [架构基线 v1.1](docs/architecture/2026-10-16-tech-stack.md)）：

- Go 模块化单体：API + Worker 同仓。
- Gin + GORM + PostgreSQL 16 + pgvector。
- React 19 + TypeScript + Vite + React Router。
- OpenAPI 3.1 + REST + SSE。
- PostgreSQL-backed durable queue（首选 River）。
- Semantic Scholar、OpenAlex、Provider-native Web Search、LLM 和 Embedding 通过适配器接入。
- 默认模型方向为 OpenAI 兼容的 Qwen 系列；联网搜索优先复用 Qwen/DashScope 原生能力，不默认接第三方搜索 API。

阶段 0 实现偏差（有意为之，见 [Change Spec §12](docs/changes/RS-SPEC-0001-stage0-smoke.md)）：
当前代码直接使用 `pgx`、自建 migration runner 和 PostgreSQL 表，尚未引入 GORM、Goose 与 River。
阶段 0 目标是先证明闭环，数据访问/迁移/任务队列的栈内对齐在阶段 1 以 ADR 决定。
