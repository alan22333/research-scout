# RS-SPEC-0001：阶段 0 本地 Smoke 闭环

| 项目 | 内容 |
|---|---|
| 状态 | Verified |
| 日期 | 2026-10-16 |
| 负责人 | Codex |
| 上游依据 | PRD v1.4；架构基线 v1.1；技术设计包 v1.1；OpenAPI 1.1.0 |
| 前置规格 | 无 |

## 1. 问题与目标

当前仓库只有冻结的产品、架构、模块设计和 OpenAPI 契约，没有可运行实现。阶段 0 的目标不是完成全量产品，而是建立最小可验证闭环，证明工程骨架、数据库迁移、不可变 `index_epoch`、脱敏 Zotero fixture、个人文库检索和 claim-level Brief 追溯可以落地。

## 2. 范围

### 做

- 建立 Go 模块化单体骨架：`cmd/api`、`cmd/worker`、`internal` 领域模块。
- 建立 PostgreSQL/pgvector 迁移与核心 repository（阶段 0 用自建 migration runner，偏差见 §12）。
- 实现本地身份适配器、health、ready、统一错误模型。
- 建立脱敏 Zotero fixture 导入、文档版本、chunk、检索索引和不可变 `index_epoch` 原子发布。
- 实现最小 `my_library` 查询：FTS/向量接口边界、确定性检索、Session、Turn、Brief、Evidence、Claim、SSE 事件。
- 建立 `make verify`、`make test-unit`、`make test-integration`、`make test-contract`、`make openapi-lint`、`make smoke-stage0`、`make eval-rag`。
- 提供一个离线 golden set schema、最小 golden question 和机器可读评测报告。

### 不做

- 不调用真实 Zotero Web API、OAuth、LLM、Embedding、Rerank、S2、OpenAlex 或联网搜索 Provider。
- 不实现真实凭据加密密钥管理、生产部署、前端界面、外部发现、分享、公共库、Connector 或 PDF 处理。
- 不实现完整多租户权限、审计后台、30 天清理调度和备份恢复。
- 不承诺阶段 0 的检索质量达到产品质量 Gate；质量 Gate 只验证命令、报告和 hard gate 机制存在。

## 3. 行为规格与约束

- 所有数据对象必须属于固定本地 workspace；测试请求头切换 actor 仅允许在 local/test 环境显式开启。
- `metadata_only` 文档可以展示覆盖状态，但不能进入 evidence、claim 或 scientific conclusion。
- 同步/索引导入使用 building → active 的 `index_epoch` 原子发布；失败保留旧 active epoch。
- 查询必须固定当前 `index_epoch_id`、`planner_version`、`retrieval_config_version`、`prompt_version`。
- 每个 finding 或 summary claim 必须至少有一条 `claim_evidence`；无证据内容只能进入 `open_questions`。
- SSE 事件只展示阶段、工具、来源、降级和结果，不包含隐藏推理或私人全文原文。
- 默认测试和评测不得访问真实外部 Provider。

## 4. 接口与数据影响

- API/OpenAPI：实现 `/healthz`、`/readyz`、`/metrics`、`/api/v1/me`、项目、scope、session、turn、events、brief、evidence 的阶段 0 子集。
- 数据库/迁移：创建阶段 0 所需核心表、索引、pgvector/pg_trgm 扩展和唯一约束。
- Provider：使用接口和 stub；Embedding Provider 可用确定性本地 stub，不允许访问真实服务。
- 前端：本规格不做 Web UI。
- 文档：新增本 Change Spec；实现完成后更新 README 本地运行说明。
- 观测与日志：health/ready、请求 ID、阶段事件、评测 JSON 报告。

## 5. 验收条件

- AC-001：Given 干净仓库和本地 PostgreSQL/pgvector，When 运行 `make smoke-stage0`，Then 自动迁移、seed workspace、导入脱敏 fixture、发布 active epoch、执行一次 `my_library` 查询并生成可读取 Brief。
- AC-002：Given fixture 中同时包含 fulltext、abstract 和 metadata-only 文档，When 查询需要证据的问题，Then Brief 中的 finding/claim 只能引用 fulltext 或 abstract 证据，不能引用 metadata-only 文档。
- AC-003：Given 已生成 Brief，When 查询证据追溯，Then 每条 claim 可以通过 `claim_evidence` 找到 evidence、document_version 和 chunk locator。
- AC-004：Given 新 building epoch 未发布，When 再次查询，Then 查询继续使用旧 active epoch，Brief 不读取新 building 内容。
- AC-005：Given 重复导入同一 fixture，When smoke 再次执行，Then 不创建重复 document、chunk、epoch membership 或 claim/evidence 行。
- AC-006：When 运行 `make eval-rag`，Then 产生机器可读 JSON 报告，包含数据集版本、`index_epoch_id`、配置版本、指标和 hard gate 结果。
- AC-007：When 运行 `make openapi-lint` 和 `make verify`，Then 契约解析、格式、单元测试、集成测试和契约测试全部通过。

## 6. 测试计划

测试标识为仓库中真实存在的测试函数；`RS_DATABASE_URL` 已设置的集成测试会连接本地 PostgreSQL/pgvector。

| AC | 测试层级 | 测试标识/文件 | Red 证据 | Green 证据 |
|---|---|---|---|---|
| AC-001 | Integration | `TestIntegrationStage0Smoke`（`internal/integration/stage0_integration_test.go`） | 实现前仓库无 `cmd/`、`migrations/`、`Makefile`，无对应测试（原始日志未留存，见 §10） | `make smoke-stage0` 与集成测试通过 |
| AC-002 | Unit + Integration | `TestMetadataOnlyCannotBecomeEvidence`、`TestFulltextAndAbstractEvidenceAreAllowed`（`internal/brief/policy_test.go`）；`TestIntegrationStage0Smoke` 断言 `metadata_only` evidence 计数为 0 | 实现前包不存在 | `go test ./internal/brief` 通过；smoke 报 `metadata_only_evidence=0` |
| AC-003 | Unit + Integration | `TestBuildBriefRequiresClaimEvidence`（`internal/query/service_test.go`）；`TestIntegrationStage0Smoke` 逐条 claim 校验 `ClaimEvidence` | 实现前包不存在 | `go test ./internal/query` 通过；smoke 报 `traceable_claims=5/5` |
| AC-004 | Unit + Integration | `TestPublishEpochIsAtomicAndIdempotent`、`TestChunkRequiresEpochScopedUniqueness`（`internal/indexing/epoch_test.go`）；`TestIntegrationQueryUsesActiveEpochNotBuilding` | 实现前包不存在 | `go test ./internal/indexing` 通过；集成测试确认 building epoch 内容未泄漏 |
| AC-005 | Integration | `TestIntegrationFixtureImportIdempotent`；重复 `make smoke-stage0` | 实现前 importer 不存在 | 二次导入 `new_epoch=false`、`versions_added=0`，行数不增长 |
| AC-006 | Unit + Script | `TestReportContainsVersionAndHardGateFields`（`internal/eval/report_test.go`）；`make eval-rag` | 实现前 eval 命令不存在 | `make eval-rag` 通过并产出 `eval/reports/*.json` |
| AC-007 | Contract | `TestContractImplementedRoutesExistInOpenAPI`、`TestContractHealthAndUnauthorizedShape`（`internal/httpserver/contract_test.go`）；`make openapi-lint` | 实现前无 Makefile 入口 | `make verify` 通过 |

补充覆盖（超出原计划但已实现）：`TestIntegrationProjectScopeFiltersCollections` 验证 `selected_collections` scope 不越界；`TestLocalAdapterDoesNotTrustHeadersByDefault`、`TestLocalAdapterAllowsExplicitDevHeader` 验证 dev header 只在显式开启时生效；`TestLoadDefaultsToLocalAtomicMode` 验证默认本地 atomic 配置。

## 7. 安全、隐私与生命周期

- workspace/scope：阶段 0 使用固定 `local-workspace`；所有 repository 测试必须带 workspace 过滤。
- 凭据与日志：不写入真实 Key；Provider stub 不包含密钥字段。
- 删除与保留：只实现 tombstone 字段和基础过滤；30 天调度延后。
- Provider 降级：默认 stub 标记 `skipped`；评测报告必须记录 source status。

## 8. 迁移、发布与回滚

- 迁移步骤：`make migrate`（或 `make smoke-stage0` 自动执行）在 `migrations/` 上按文件名顺序应用未执行的迁移；阶段 0 由自建 runner 实现，偏差见 §12。
- 兼容窗口：无历史生产数据。
- 回滚方式：删除本地数据库（`docker compose down -v`）或重建 schema；代码回滚到本 Spec 之前。
- 监控信号：readyz、迁移版本、epoch 发布状态、评测 hard gate failures。

## 9. 风险与待确认项

| 风险/问题 | 影响 | 默认决策 | 最迟决策点 | 状态 |
|---|---|---|---|---|
| 本地没有 PostgreSQL/pgvector | smoke 无法执行 | 提供 Docker Compose 本地依赖，不在本规格完成生产部署 | 阶段 0 验证 | Closed（`make db-up` + `make smoke-stage0` 已验证） |
| 宿主机端口冲突 | 数据库无法连接 | 映射到宿主 `55433`，避开占用中的 `55432` | 阶段 0 验证 | Closed |
| 真实 Embedding 维度不可离线验证 | 无法调用 Provider | 阶段 0 用确定性 1024 维 stub，Provider 合同测试延后 | 阶段 1 | Open |
| 检索质量没有真实 baseline | 不能判断优化收益 | 阶段 0 只保证质量门禁机制和最小 golden question | 阶段 2 | Open |
| API 契约较大，阶段 0 只实现子集 | 前端无法完整开发 | 保持 OpenAPI 为冻结契约；实现侧明确子集 | 阶段 2 | Open |
| 数据访问栈与冻结架构不一致 | 阶段 1 需迁移或确认 | 记录为实现偏差（§12），阶段 1 以 ADR 决定是否对齐 GORM/Goose/River | 阶段 1 开工 | Open |
| `eval-rag` 每次运行新建 session/brief | 重复评测会在本地库累积 brief | 阶段 0 可接受；评测产物按 run_id 归档 | 阶段 2 | Open |

## 10. 验证证据

以下证据于 2026-10-16 在干净数据库（`docker compose down -v` 后重建）上实跑采集。

- Red：本 Spec 之前仓库不含 `cmd/`、`internal/`、`migrations/`、`Makefile` 与任何 Go 测试，因此 `make verify`/`make smoke-stage0` 必然失败；**初始化阶段的终端日志未留存**，此处不伪造 Red 输出。可复现的 Red 方式：在实现前状态执行上述命令会因缺少目标/包而失败。
- Green（关键命令实测输出）：
  - `make migrate`（空库）：`{"applied":["0001_stage0"],"skipped":null}`；再次执行 `{"applied":null,"skipped":["0001_stage0"]}`。
  - `make smoke-stage0` 第 1 次：`new_epoch=true`、`documents=3`、`versions_added=3`、`chunks=4`、`metadata_only=1`；`brief_id=brief-94c03969a374c7b97f28e63c`、`claims=5`、`citations=4`、`traceable_claims=5`、`metadata_only_evidence=0`。
  - `make smoke-stage0` 第 2 次：`new_epoch=false`、`versions_added=0`；`brief_id`/`session_id`/`turn_id` 与第 1 次完全一致，`claims=5`、`citations=4`、`metadata_only_evidence=0`。
  - 两次 smoke 后数据库计数：`documents=3`、`document_versions=3`、`chunks=4`、`index_epochs=1`、`active_epochs=1`、`briefs=1`、`claims=5`、`evidence=4`、`claim_evidence=8`（证明导入与 smoke 幂等，未产生重复行）。
  - `make eval-rag`：`evidence_coverage=1`、`retrieval_hit_rate=1`、`claim_traceability_rate=1`、`claim_count=10`、`hard_gate_failures=[]`；报告写入 `eval/reports/rag-eval-20261016T154914.514237000Z.json`，并更新 `eval/reports/latest.json`。
- Full verification：`make verify` 全绿（OpenAPI YAML 解析 + `test-unit` + `test-integration` + `test-contract`）；`go test ./... -count=1` 全绿。
- 手工/探索验证：启动 `go run ./cmd/api`，`GET /healthz` → `200 {"status":"ok"}`，`GET /readyz` → `200 {"database":"ready","migrations":"ready","status":"ready"}`，`GET /api/v1/me` → `200` 本地 actor，`GET /metrics` → `200` Prometheus 文本；在 local 模式下未带请求头访问 `/api/v1/projects` 返回 `200`（默认解析为本地 dev actor，非鉴权绕过）；未配置身份适配器时 `/api/v1/me` 返回 `401` 由契约测试 `TestContractHealthAndUnauthorizedShape` 覆盖。
- 已知限制：阶段 0 不访问真实 Zotero/Provider；`web/` 无 UI；`eval-rag` 每次运行新建 session/brief，重复评测会在本地库累积 brief；数据访问栈与冻结架构存在 §12 记录的偏差。

## 11. 决策记录

| 日期 | 决策 | 原因 | 依据 |
|---|---|---|---|
| 2026-10-16 | 阶段 0 只做本地 fixture 闭环 | 避免同时处理真实 Zotero、Provider 和部署复杂度 | 实现蓝图阶段 0 |
| 2026-10-16 | 默认 Embedding 使用确定性 stub | 保证离线可验证；真实 Provider 合同测试延后 | SDD 默认 CI 不访问真实 Provider |
| 2026-10-16 | 本地数据库端口映射到 55433 | 避开宿主机占用中的 55432 | 本 Spec §9 |
| 2026-10-16 | 阶段 0 直接使用 pgx + 自建 migration runner | 先证明闭环，减少阶段 0 引入的框架面 | 本 Spec §12（与冻结架构的偏差） |

## 12. 与冻结设计的实现偏差

本 Spec 是**阶段 0 实现记录**。实现与已冻结的架构基线存在以下有意偏差，架构文档本身在本轮不做改写：

| 领域 | 冻结架构（基线 v1.1） | 阶段 0 实际实现 | 说明 |
|---|---|---|---|
| 数据访问 | GORM，复杂 SQL 封装在 repository | 直接使用 `pgx`（含连接池） | 阶段 0 表结构与查询有限，pgx 足够；阶段 1 决定是否切换或补齐 GORM |
| 迁移 | Goose | 自建文件名顺序迁移 runner（`internal/storage/migrate.go`） | 迁移语义（版本化、幂等）一致；工具不同 |
| 后台任务 | PostgreSQL-backed durable queue（首选 River） | 未引入任务队列，worker 命令同步执行 | 阶段 0 无长任务；阶段 1 接入 River 时另立 ADR |
| API 框架 | Gin | Gin | 一致，无偏差 |

对齐这些偏差需要用户在阶段 1 开始时决策，或按开发规范补一份 ADR；在此之前，架构基线仍是后续阶段的权威设计。
