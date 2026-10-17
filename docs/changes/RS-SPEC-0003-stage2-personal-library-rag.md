# RS-SPEC-0003：阶段 2 个人文库 RAG

| 项目 | 内容 |
|---|---|
| 状态 | Verified |
| 日期 | 2026-10-17 |
| 负责人 | Codex |
| 上游依据 | PRD v1.4；架构基线 v1.1；技术设计包 v1.1；OpenAPI 1.1.0 |
| 前置规格 | RS-SPEC-0002（Verified） |

## 1. 问题与目标

阶段 1 已经完成 Zotero Web API 同步、版本化索引和原子 epoch 发布，但查询层仍是单路全文排序占位实现：没有 Query Planner、没有多 subquery、没有 FTS + vector + RRF 融合、没有 rerank fallback 和文档级配额。Brief 仍把每条 chunk 机械写成 `sufficient/verified`，不能表达摘要级证据的不确定性，也没有完整的 scope 校验和会话生命周期接口。

本切片交付 `my_library` 的个人文库 RAG 最小闭环：确定性计划、混合候选融合、证据分层、claim-level 可追溯 Brief、基础评测指标和 OpenAPI 已冻结的会话生命周期接口。`gap_fill`、`landscape`、S2、OpenAlex 和联网搜索仍属于阶段 3。

## 2. 范围

### 做

- 实现确定性 Query Planner；最多 5 个 subquery；Planner 失败时回退原问题。
- 将 Planner 输出持久化到 `query_turns.plan_json` 和 `plan_updated` SSE 事件。
- 实现 FTS top 40、vector top 40、weighted RRF（k=60）和可选 rerank fallback。
- 最终证据最多 12 个 chunk、6 篇 document、每篇最多 2 个 chunk，并遵守约 12K token 预算。
- 证据优先级固定为 `personal_fulltext > structured_abstract`；`metadata_only` 永不进入 evidence。
- Brief 根据证据深度生成 `sufficient | partial | insufficient`，禁止仅因有 evidence 就硬编码 `verified`。
- 没有科学证据时生成明确的 insufficient summary 和 open question，不生成伪 finding。
- 实现 project scope 校验：selected scope 非空，collection 必须属于当前 workspace 且未删除。
- 实现 OpenAPI 已冻结的 project/session delete、session cancel、brief feedback 和 brief export。
- 扩展 golden set schema 和评测指标：Recall@40、Recall@12、MRR@10、nDCG@10、ExactMatch@3、Citation Precision、Unsupported Claim Rate、Abstention Precision、Rerank Delta。
- 新增 `migrations/0003_stage2_rag_indexes.sql`，只新增索引、`query_turns.plan_json` 和 `brief_feedback`，不改写现有数据。

### 不做

- 不实现 S2、OpenAlex、Web Search、`gap_fill`、`landscape`。
- 不实现 LLM Provider 调用；生成层先使用确定性的证据抽取 fallback，接口保持可替换。
- 不实现 Connector、Local API、OAuth、group library、PDF 上传/解析、批注/笔记或 Zotero 写回。
- 不修改 PRD 产品范围和现有评测历史报告。
- 不提交真实 API Key、私人查询、私人全文、PDF 或未脱敏 Provider 响应。

## 3. 行为规格与约束

- Planner 输出 `language/intent/route/subqueries/weights`，只允许固定枚举和最多 5 个 subquery。
- 所有检索先应用 `workspace_id`、active `index_epoch_id`、active document version、scope、`removed_at IS NULL`、`index_status=indexed`。
- RRF 公式为 `Σ query_weight(q) * source_weight(s) / (60 + rank_s,q)`。
- reranker 缺失或返回错误时使用 RRF + vector 分数，不阻断查询。
- 选择阶段先按融合分数排序，再执行每篇 document cap 和最终 document cap。
- `structured_abstract` 最多支撑摘要级结论，标记为 `partial`；只有 `personal_fulltext` 可以把 claim 标记为 `verified`。
- 每个 summary/finding claim 必须通过 `claim_evidence` 追溯 evidence、document version 和 chunk locator。
- Project scope 为 `selected_collections` 时，空数组返回 `scope_empty`；任何不属于当前 workspace 或已删除的 collection 返回 `forbidden` 或 `invalid_request`。
- 删除 project/session 使用软删除和 30 天 purge deadline；删除后在默认读取和查询中不可见。
- Brief 发布后不可变；feedback 只新增记录，不修改 Brief。

## 4. 接口与数据影响

- API/OpenAPI：实现已有冻结路由；不改变请求/响应语义。
- 数据库/迁移：`query_turns.plan_json JSONB`、`brief_feedback` 表、scope/检索/事件排序索引。
- Provider：新增 `EmbeddingProvider`、`RerankProvider` 边界；默认使用本地 stub/fallback，不访问网络。
- 前端：不实现。
- 文档：新增本 Change Spec；如实现与设计冲突，先修设计再继续。
- 观测与日志：只记录版本、计数、延迟和稳定错误码，不记录私人 query 或全文。

## 5. 验收条件

- AC-001：Given 一个 `known_item` 问题，When Planner 执行，Then 返回固定 route 且最多 5 个 subquery；中文/空结果回退原问题。
- AC-002：Given 相同 chunk 同时进入 FTS 和 vector 候选，When RRF 融合，Then 分数按 query/source 权重和 rank 计算，不重复为多个证据。
- AC-003：Given reranker 不存在或报错，When 检索，Then 使用 RRF + vector fallback 且不返回错误。
- AC-004：Given 多篇文档的候选，When 证据选择，Then 最多 12 chunk、6 文档、每文档 2 chunk，并满足 token budget。
- AC-005：Given 只有 abstract evidence，When 生成 Brief，Then summary/finding 为 `partial` 且不得标记 `verified`。
- AC-006：Given 没有可科学支撑的 evidence，When 生成 Brief，Then summary 明确 insufficient、没有伪 finding，并给出 open question。
- AC-007：Given selected scope 为空或包含海外/已删除 collection，When 更新 scope，Then 拒绝并返回稳定错误，不落库。
- AC-008：Given 已发布的 Brief，When 提交 feedback，Then 写入独立反馈记录且 Brief 不变。
- AC-009：Given 一个 running session，When cancel/delete，Then 状态转换正确且默认 API 不再可见。
- AC-010：Given golden set，When eval 运行，Then 报告包含检索、引用、拒答指标和全部版本字段。
- AC-011：Given 任意 Brief，When API 导出，Then JSON/BibTeX/CSV 只包含已授权当前 workspace 的 citations。

## 6. 测试计划

| AC | 测试层级 | 测试标识/文件 | Red 证据 | Green 证据 |
|---|---|---|---|---|
| AC-001 | Unit | `internal/planner.TestPlannerRoutesAndLimits` | `go test ./internal/planner` 未定义 `Plan`/`QueryIntent` | Green（`go test ./internal/planner ./internal/retrieval ./internal/query ./internal/eval ./internal/httpserver -count=1` 通过；集成测试见 §10） |
| AC-002 | Unit | `internal/retrieval.TestWeightedRRFMergesDuplicateChunks` | `go test ./internal/retrieval` 未定义 `RankedList`/`FuseRRF` | Green（`go test ./internal/planner ./internal/retrieval ./internal/query ./internal/eval ./internal/httpserver -count=1` 通过；集成测试见 §10） |
| AC-003 | Unit | `internal/retrieval.TestRerankFallbackKeepsRRF` | 未定义 `RerankOrFallback` | Green（`go test ./internal/planner ./internal/retrieval ./internal/query ./internal/eval ./internal/httpserver -count=1` 通过；集成测试见 §10） |
| AC-004 | Unit | `internal/retrieval.TestSelectionCapsAndBudget` | 未定义 `Select` | Green（`go test ./internal/planner ./internal/retrieval ./internal/query ./internal/eval ./internal/httpserver -count=1` 通过；集成测试见 §10） |
| AC-005/006 | Unit + Integration | `internal/query.TestGroundingPolicy`; `TestIntegrationStage2GroundedBrief` | `go test ./internal/query` 未定义 `buildBriefContent` | 单元 Green + `RS_DATABASE_URL=... go test ./internal/integration -run TestIntegrationStage2 -count=1 -v` 全部 PASS |
| AC-007 | Integration | `internal/integration.TestIntegrationStage2ScopeValidation` | scope 校验分支未实现，集成用例失败 | `TestIntegrationStage2ScopeValidation` PASS |
| AC-008/009 | Integration + Contract | `internal/integration.TestIntegrationStage2LifecycleRoutes`; `internal/httpserver.TestContractStage2Routes` | 合同测试报告缺少 `DELETE /api/v1/projects/:projectId` 等 5 条路由 | `TestContractStage2Routes`、`TestIntegrationStage2LifecycleRoutes` PASS |
| AC-010 | Unit | `internal/eval.TestRetrievalMetrics` | `go test ./internal/eval` 未定义 `ComputeRetrievalMetrics` | `TestRetrievalMetrics`、`TestScoreQuestionComputesStage2Metrics` PASS |
| AC-011 | Integration | `internal/integration.TestIntegrationStage2BriefFeedbackAndExport` | 导出与反馈路由未实现 | `TestIntegrationStage2BriefFeedbackAndExport` PASS |

## 7. 安全、隐私与生命周期

- workspace/scope：所有读取和写入显式带 `workspace_id`、active epoch、scope、tombstone 和生命周期过滤。
- 凭据与日志：不读取、不输出、不记录任何真实凭据；测试只用固定 fake key。
- 删除与保留：project/session 软删除 30 天；feedback 随 workspace 生命周期清理。
- Provider 降级：本切片无网络 Provider；rerank 缺失必须显式按 fallback 运行并在 source status 中记录。

## 8. 迁移、发布与回滚

- 迁移步骤：新增 `0003_stage2_rag_indexes.sql`，`ALTER TABLE ... ADD COLUMN IF NOT EXISTS` 与 `CREATE TABLE/INDEX IF NOT EXISTS`。
- 兼容窗口：旧 `query_turns` 允许 `plan_json` 默认 `{}`；旧 Brief 继续可读。
- 回滚方式：代码可回退到单路检索；保留新增列和表，不做破坏性 down migration。
- 监控信号：检索候选数、最终证据数、rerank fallback 次数、hard gate failures。

## 9. 风险与待确认项

| 风险/问题 | 影响 | 默认决策 | 最迟决策点 | 状态 |
|---|---|---|---|---|
| 无真实 reranker 时质量提升有限 | Stage 2 只能验证 fallback 与融合 | 先交付可替换边界和确定性 fallback | 阶段 3 前 | 接受 |
| Golden set 为合成问题 | 不能替代真实用户质量 | 作为回归基线，不宣称产品效果 | 试点前 | 接受 |
| 现有 API 同步执行查询 | SSE 不是真正流式 | 保持事件持久化，不伪装流式 token | 阶段 4 前 | 接受 |

## 10. 验证证据

- Red：已执行 `go test ./internal/planner ./internal/retrieval ./internal/query ./internal/eval ./internal/httpserver -count=1`。失败摘要：`domain.QueryIntent`、`Plan`、`RankedList`、`FuseRRF`、`Select`、`RerankOrFallback`、`buildBriefContent`、`ComputeRetrievalMetrics` 均未实现；合同测试报告缺少 `DELETE /api/v1/projects/:projectId`。这不包含凭据、私人全文或 Provider 响应。
- Green：已在本地 PostgreSQL/pgvector 与脱敏 fixture 上实跑。所有命令均未输出或持久化私人 query、私人全文或 Provider 响应正文。
  - `go test ./internal/planner ./internal/retrieval ./internal/query ./internal/eval ./internal/httpserver -count=1`：通过。
  - `go test ./... -count=1`：通过。
  - `RS_DATABASE_URL=... go test ./internal/integration -run 'TestIntegrationStage2' -count=1 -v`：`TestIntegrationStage2GroundedBrief`、`TestIntegrationStage2ScopeValidation`、`TestIntegrationStage2LifecycleRoutes`、`TestIntegrationStage2BriefFeedbackAndExport` 全部 PASS。
- Full verification：
  - `make eval-rag`：`hard_gate_failures=[]`、`retrieval_hit_rate=1`、`recall_at_40=0.75`、`recall_at_12=0.75`、`mrr_at_10=1`、`ndcg_at_10=0.8066`、`exact_match_at_3=1`、`citation_precision=0.75`、`claim_traceability_rate=1`、`unsupported_claim_rate=0`、`rerank_version=rrf-fallback-v1`；报告 `eval/reports/rag-eval-20261017T021539.318495000Z.json`。
  - `make eval-retrieval`、`make eval-brief`：通过，指标与 `make eval-rag` 一致，`kind` 分别为 `retrieval`、`brief`；报告 `eval/reports/rag-eval-20261017T021539.470648000Z.json`、`eval/reports/rag-eval-20261017T021539.609990000Z.json`。
  - `make verify`：通过（OpenAPI YAML 解析、单元测试、Integration 测试、Contract 测试）。
  - `make migrate`：通过；`0001_stage0`、`0002_stage1_sync_indexes`、`0003_stage2_rag_indexes` 全部 `skipped`（此前已应用），迁移保持幂等。
  - `make smoke-stage0`：`status=ok`、`metadata_only_evidence=0`、`claims=5`、`traceable_claims=5`、`citations=4`、`documents=3`、`chunks=4`。
  - `go vet ./...`：无告警。
- 手工/探索验证：`smoke-stage0` 端到端复跑确认 `metadata_only` 文档不进入 evidence；Stage 2 集成用例覆盖 scope 拒绝、会话 cancel/delete、feedback 只增不改 Brief、导出仅含当前 workspace citation。
- 已知限制：Stage 2 只实现 `my_library`、确定性生成 fallback 和本地 rerank fallback；S2、OpenAlex、Web Search、`gap_fill`、`landscape` 延后到阶段 3。Golden set 为合成问题且当前只有 2 个已标注问题（`abstention_precision=0` 源于尚无 unanswerable 样本），只能作为回归基线，不能作为产品效果结论。BibTeX/CSV 导出按 UTF-8 输出，非 ASCII 未做 LaTeX 转义。

## 11. 决策记录

| 日期 | 决策 | 原因 | 依据 |
|---|---|---|---|
| 2026-10-17 | Stage 2 先做 `my_library`，不做外部 Provider | 保持单一垂直切片，先完成可核验个人文库闭环 | 技术设计实施顺序 |
| 2026-10-17 | 确定性生成 fallback 替代 LLM | 当前仓库没有 LLM Provider 端口，质量 Gate 需离线可复现 | SDD 门禁 |

## 12. AC → 测试 → 证据追踪矩阵

| AC | 测试 | 验证命令 | 证据 |
|---|---|---|---|
| AC-001 | `TestPlannerRoutesAndLimits`、`TestPlannerPreservesOriginalAndFallsBack`、`TestPlannerRejectsOrCapsUnboundedInput` | `go test ./internal/planner -count=1` | 单元 PASS |
| AC-002 | `TestWeightedRRFMergesDuplicateChunks` | `go test ./internal/retrieval -count=1` | 单元 PASS |
| AC-003 | `TestRerankFallbackKeepsRRF`、`TestSourceStatusesRecordRerankFallback`、`TestSourceStatusesRecordRerankUse` | `go test ./internal/retrieval ./internal/query -count=1` | 单元 PASS |
| AC-004 | `TestSelectionCapsAndBudget` | `go test ./internal/retrieval -count=1` | 单元 PASS |
| AC-005 | `TestGroundingPolicy` | `go test ./internal/query -count=1` | abstract 只允许 `partial`；fulltext 才可 `verified` |
| AC-006 | `TestGroundingPolicy`、`TestIntegrationStage2GroundedBrief` | 单元 + `-run TestIntegrationStage2` | insufficient 明确、无伪 finding |
| AC-007 | `TestIntegrationStage2ScopeValidation` | `-run TestIntegrationStage2ScopeValidation` | 集成 PASS |
| AC-008 | `TestIntegrationStage2BriefFeedbackAndExport` | `-run TestIntegrationStage2BriefFeedbackAndExport` | 集成 PASS；Brief 不可变 |
| AC-009 | `TestIntegrationStage2LifecycleRoutes`、`TestContractStage2Routes` | `-run TestIntegrationStage2LifecycleRoutes`；`go test ./internal/httpserver -run Contract` | 集成 + 合同 PASS |
| AC-010 | `TestRetrievalMetrics`、`TestAnswerMetrics`、`TestRerankDelta`、`TestScoreQuestionComputesStage2Metrics` | `go test ./internal/eval -count=1`；`make eval-rag` | 单元 PASS；报告含全部版本字段与 hard gate |
| AC-011 | `TestIntegrationStage2BriefFeedbackAndExport` | `-run TestIntegrationStage2BriefFeedbackAndExport` | 集成 PASS；导出限当前 workspace |
