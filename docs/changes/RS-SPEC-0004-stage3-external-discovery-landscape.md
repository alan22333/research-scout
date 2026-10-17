# RS-SPEC-0004：阶段 3 外部发现、缺口判定与全景模式

| 项目 | 内容 |
|---|---|
| 状态 | Verified |
| 日期 | 2026-10-17 |
| 负责人 | Codex |
| 上游依据 | [PRD v1.4](../../docs/prd/2026-10-16-zotero-research-assistant-prd.md)；[架构基线 v1.1](../../docs/architecture/2026-10-16-tech-stack.md)；[技术设计包 v1.1](../../docs/design/README.md)；[ADR-015 Provider-native Web Search](../../docs/architecture/adr/ADR-015-provider-native-web-search.md)；OpenAPI 1.1.0 |
| 前置规格 | [RS-SPEC-0003（Verified）](./RS-SPEC-0003-stage2-personal-library-rag.md) |

## 1. 问题与目标

阶段 2 只交付 `my_library`：检索全部证据来自个人文库。用户可以问“我的文库支持什么结论”，但不能回答产品定义中的另外两个核心问题——**我的文库漏掉了哪些工作**（`gap_fill`）和**这个领域整体长什么样**（`landscape`）。没有外部来源时，系统也无法判定某篇经典工作、某个方法或最新进展“缺失于个人文库”。

本切片交付**外部发现与全景模式的完整垂直闭环**：接入 Semantic Scholar 与 OpenAlex 两个结构化学术来源，实现外部作品归一化、库内外去重、三类缺口判定，接入 Qwen/DashScope Provider-native Web Search，并把 `gap_fill`、`landscape`、时间型和冲突型评测纳入质量 Gate。

本切片交付后，PRD 中三种检索模式全部可用，产品从“个人文库问答”升级为“以个人文库为中心、可核验库外发现的研究助手”。

## 2. 范围

### 做

- 实现 `ScholarlyDiscoverySource` 领域接口及两个实现：`SemanticScholarSource`、`OpenAlexSource`。
- 实现外部作品统一模型 `ExternalWork` 与 `identity_key` 归一化。
- 实现库内外去重：命中个人文库的库外结果标记为既有作品，不计入缺口。
- 实现三类 Gap 判定：`canonical`、`related_method`、`latest_progress`，并区分 `personal_library | current_scope | external_only`。
- 实现 `gap_fill` 检索模式：文库混合检索 + 外部结构化发现 + 缺口判定。
- 实现 `landscape` 检索模式：在 `gap_fill` 基础上按需执行 Qwen/DashScope 原生联网搜索。
- 实现 `WebSearchProvider` 边界与 `QwenNativeWebSearch`、`DisabledWebSearchProvider` 两个实现。
- 实现来源状态与显式降级：`semantic_scholar`、`openalex`、`qwen_web_search` 各自独立 `SourceStatus`。
- 实现限流、退避、超时、重试和部分成功：单个外部来源失败不得阻断 `my_library` 或整体 turn。
- 实现冲突展示：库内工作与外部工作在同一科学问题上立场冲突时，Brief 必须同时展示支持与反驳证据。
- 实现外部工作的只读查询：`/discovery/search`、`/discovery/works/{id}`、`/discovery/works/{id}/citations`。
- 扩展 golden set 与评测：新增 `gap_fill`、`landscape`、`temporal`、`conflict_check` 意图和对应指标。

### 不做

- 不实现 LLM 生成层替换：Brief 仍使用确定性证据抽取，但缺口上下文与冲突分组必须结构化输出。
- 不实现引用图扩展（references/citations 二次召回）；`/discovery/works/{id}/citations` 只做只读透传。
- 不实现 Connector、Local API、OAuth、group library、PDF 上传/解析、批注/笔记或 Zotero 写回。
- 不接入 Tavily、Brave、自建爬虫或任何第三方搜索 API。
- 不让 Web Search 结果进入 `my_library` 或 `gap_fill`。
- 不把 Web Search 结果单独作为科学结论证据。
- 不实现 dblp、PubMed 或其他结构化来源；只保留 `ScholarlyDiscoverySource` 扩展边界。
- 不把外部发现的全文入库；`external_works` 只存标准元数据与摘要。

## 3. 行为规格与约束

### 3.1 模式与来源矩阵

| 模式 | 个人文库 | S2/OpenAlex | Web Search |
|---|---|---|---|
| `my_library` | 必须 | 禁止 | 禁止 |
| `gap_fill` | 必须 | 必须 | 禁止 |
| `landscape` | 必须 | 必须 | 按需 |

- 用户显式选择模式；系统不根据问题文本自动切换到 `gap_fill` 或 `landscape`。
- `gap_fill` 与 `landscape` 仍先执行个人文库检索，缺口判定必须相对于个人文库结果，而不是相对于整个互联网。
- `landscape` 只有在结构化来源已检索后仍存在覆盖缺口时才调用 Web Search。

### 3.2 外部作品归一化与去重

- `identity_key` 优先级固定为：`DOI -> S2 ID -> OpenAlex ID -> PMID -> title fingerprint + year`。
- DOI 比较前必须归一化：小写、去除 `https://doi.org/` 与 `doi:` 前缀、去除首尾空白。
- 标题 fingerprint：小写、Unicode NFKC、去标点与多余空白、压缩空格；仅用于候选合并。
- 缺少年份、作者或稳定 ID 时，标题 fingerprint 不得自动合并两条记录。
- 同一次检索内，同一 `identity_key` 只保留一条最优记录；最优判定顺序为：来源完整度、摘要非空、引用数、`retrieved_at` 新。
- 外部结果命中个人文库（DOI 或有年份的标题 fingerprint）时，标记 `already_in_library=true`，从缺口候选中排除，但仍可在结果列表展示“库内已有”。
- 归一化与去重必须在同一 workspace 内进行；不得跨 workspace 复用 `external_works` 行。

### 3.3 缺口判定

- `canonical`：库内高相关工作命中的综述型工作，或引用数位于该主题前分位、且个人文库完全缺失的工作。
- `related_method`：与库内种子工作存在方法词重叠和高相关性，但 `identity_key` 不在个人文库中的工作。
- `latest_progress`：最近两年内发表、相对时间分位靠前，且个人文库缺失的工作。
- 每条 Gap 必须带 `gap_type`、`missing_relative_to`、`rationale`、理由字段（分位、方法词或时间窗）和至少一个外部 work 引用。
- `missing_relative_to` 判定：`personal_library`（整个 My Library 缺失）、`current_scope`（库内有但不在当前 scope）、`external_only`（仅外部来源发现，尚未确认库内状态）。
- 引用数不能单独代表权威性；缺少综述类型、venue、时间或相关性信号时不得判为 `canonical`。
- 库内已有工作不得出现在缺口列表；只允许出现在“库内已有”对照。
- 外部来源不可用时，缺口列表为空并显式记录 `source_status`，不得用“无缺口”伪装成功。

### 3.4 Provider 行为

- `SemanticScholarSource`：`S2_API_KEY` 认证；默认 1 req/s；429 读 `Retry-After`，否则指数退避；最多重试 4 次；超时 20 秒；解析时清理非法控制字符。
- `OpenAlexSource`：带 `OPENALEX_EMAIL` 进入 polite pool；默认并发 2；cursor pagination；超时 20 秒；abstract inverted index 还原失败时 `abstract=NULL` 并标记 `invalid_response`。
- `QwenNativeWebSearch`：OpenAI 兼容 Chat Completions + `enable_search=true` + `search_options.enable_source=true`；缺失 Key 时返回 `web_search_unavailable` 且不重试。
- Web Search 结果必须带 URL 与 `retrieved_at`；缺少 URL citation 的结果必须丢弃并标记 `invalid_response`。
- 不得把完整个人全文或私人问题拼进 Web Search query；只传去标识化研究主题和必要上下文。
- Provider 错误一律映射为 `unavailable | rate_limited | unauthorized | invalid_response | timeout | timeout_budget`，不得把 SDK/HTTP 类型泄漏到领域层。
- 所有外部调用必须显式设置超时、重试上限、取消传播和 `Retry-After` 遵守；禁止无限重试。
- 日志只记录 duration、status、retry count、provider、model、数量与 query fingerprint；不得记录完整 query、私人全文或密钥。

### 3.5 证据与冲突规则

- 证据优先级保持 `personal_fulltext > structured_abstract > web_context`。
- `web_context` 只能提供背景与线索；由 Web Search 发现的 DOI/论文链接必须回查 S2/OpenAlex 后才可进入科学结论证据。
- 没有结构化元数据的 Web 结果只能作为 `web_context`，并将引用标记为低置信层级。
- 冲突判定（确定性启发式）：同一主题簇（共享方法词或相同归一化主题标记）下，两组证据的文本在否定标记上相反（一方命中 `not | no | without | fail | does not | 不 | 无法 | 未能`，另一方未命中），且两组证据类型均不低于 `structured_abstract` 时，判为冲突。
- 冲突必须同时保留双方证据与 stance；不得用引用数、来源数量或投票裁定胜负。
- 冲突结论不得通过投票或引用数多寡裁定；必须展示双方的证据类型与来源。
- 外部作品的摘要只进入 `structured_abstract`，最多支撑 `partial`；外部工作永远不能产生 `verified`。
- `my_library` 模式的行为、指标和 source status 不得因本切片退化。

### 3.6 来源状态

- 每个来源独立上报 `available | degraded | unavailable | skipped`。
- `gap_fill` 中任一结构化来源不可用时：其余来源继续，Brief 记录降级，缺口标注覆盖不完整。
- `landscape` 中 Web Search 不可用时：`web_search_unavailable`，其余结果照常返回，不阻断 turn。
- 部分成功必须体现在 `source_status`、Brief `source_status` 字段和 SSE 事件中。
- 所有降级必须用户可见；禁止静默吞掉失败并输出“完整覆盖”。

## 4. 接口与数据影响

- API/OpenAPI：实现已冻结的 `GET /discovery/search`、`GET /discovery/works/{externalWorkId}`、`GET /discovery/works/{externalWorkId}/citations`、`GET /briefs/{briefId}/gaps`；不改变请求/响应语义。`my_library` 现有契约不得破坏。
- 数据库/迁移：新增 `migrations/0004_stage3_external_discovery.sql`；`external_works` 由阶段 0 建表，阶段 3 只允许 `ADD COLUMN IF NOT EXISTS`、补充索引和约束，不改写既有数据。
- Provider：新增 `ScholarlyDiscoverySource`、`WebSearchProvider` 边界与 S2、OpenAlex、Qwen 实现；默认测试使用 stub/fixture，不访问网络。
- 配置：新增 `S2_API_KEY`、`OPENALEX_EMAIL`、`DASHSCOPE_API_KEY`、`QWEN_SEARCH_MODEL`、`DISCOVERY_TIMEOUT`、`S2_REQUESTS_PER_SECOND`；全部通过环境变量或本地忽略配置注入。
- 前端：不实现；只保证契约与数据形状可供后续 UI 消费。
- 文档：新增本 Change Spec；如实现与设计冲突，先修文档再继续。
- 观测与日志：新增 discovery 调用计数、失败分类、限流等待、去重命中、缺口数量、Web Search 跳过原因；不得新增私人数据字段。

## 5. 验收条件

- AC-001：Given 一个 `gap_fill` 请求，When 检索执行，Then 先返回个人文库证据，再调用 S2/OpenAlex，且不调用 Web Search。
- AC-002：Given 一个 `landscape` 请求，When 结构化来源已覆盖主题，Then 可跳过 Web Search 并记录跳过原因；When 仍存在缺口，Then 调用 Web Search 且结果只标记 `web_context`。
- AC-003：Given `my_library` 请求，When 检索执行，Then 不产生任何外部来源调用，且阶段 2 指标不退化。
- AC-004：Given 同一工作的 S2 与 OpenAlex 记录，When 归一化，Then 合并为一条并保留最完整字段。
- AC-005：Given DOI 大小写、`https://doi.org/` 前缀或尾随空白不同，When 归一化，Then 生成相同 `identity_key`。
- AC-006：Given 缺少年份的 title fingerprint 候选，When 去重，Then 不自动合并并保留两条候选。
- AC-007：Given 外部结果与个人文库同一 DOI，When 缺口判定，Then `already_in_library=true` 且该工作不出现在 `gaps`。
- AC-008：Given 库内已有工作但不在当前 scope，When 缺口判定，Then `missing_relative_to=current_scope`。
- AC-009：Given 综述型或高引用分位工作缺失，When 缺口判定，Then 产生 `canonical` gap 并带分位或类型理由。
- AC-010：Given 与库内种子工作方法词重叠的外部工作，When 缺口判定，Then 产生 `related_method` gap 并带方法词理由。
- AC-011：Given 近两年发表且个人文库缺失的工作，When 缺口判定，Then 产生 `latest_progress` gap 并带时间窗理由。
- AC-012：Given 外部来源返回 429 或超时，When 检索执行，Then 按退避/上限重试，最终降级但不阻断 turn，`source_status` 反映真实状态。
- AC-013：Given Web Search Key 缺失，When `landscape` 执行，Then 返回 `web_search_unavailable`，其余结果与 Brief 正常返回。
- AC-014：Given Web Search 结果缺少 URL citation，When 归一化，Then 丢弃该结果并标记 `invalid_response`。
- AC-015：Given 库内与外部工作结论冲突，When 生成 Brief，Then 同时展示支持与反驳证据并给出冲突未决问题。
- AC-016：Given 只有 `web_context`，When 生成 Brief，Then 不得产生 `verified` 或科学 finding，只能作为背景线索。
- AC-017：Given 一个外部 work id，When 读取详情或引用关系，Then 返回结果只属于当前 workspace，越权返回 `not_found` 或 `forbidden`。
- AC-018：Given `gap_fill`、`landscape`、`temporal`、`conflict_check` golden 问题，When eval 运行，Then 报告包含对应意图指标、缺口精度与全部版本字段。

## 6. 测试计划

| AC | 测试层级 | 测试标识/文件 | Red 证据 | Green 证据 |
|---|---|---|---|---|
| AC-001/002/003 | Unit + Contract | `discovery.TestModeSourceMatrix`；`discovery.TestDiscoverMyLibraryNeverCallsExternalSources`；`discovery.TestDiscoverGapFillCallsStructuredButNotWeb`；`discovery.TestDiscoverLandscapeCallsWebWhenGapsRemain`；`query.TestModeSourceDispatch` | 先写矩阵断言，再接线 Service（Red） | `go test ./internal/discovery ./internal/query` 全绿 |
| AC-004/005/006 | Unit | `discovery.TestIdentityKeyPriorityAndStability`；`discovery.TestNormalizeDOIEquivalentForms`；`discovery.TestTitleFingerprintRequiresYearToMerge`；`discovery.TestMergeSuggestAndLibraryDedup` | 归一化/去重函数未实现（Red） | `go test ./internal/discovery` 全绿 |
| AC-007/008 | Unit + Integration | `discovery.TestMergeSuggestAndLibraryDedup`；`discovery.TestGapRelativeToScope`；`discovery.TestGapExcludesWorkAlreadyInScope`；`integration.TestIntegrationStage3GapRelativeToScope` | 缺口判定未区分 scope（Red） | `go test ./internal/discovery ./internal/integration` 全绿 |
| AC-009/010/011 | Unit | `discovery.TestDetectGapsCanonicalRelatedAndLatest`；`discovery.TestCanonicalRequiresLibraryWideAbsence` | 三类缺口未实现（Red） | `go test ./internal/discovery` 全绿 |
| AC-012 | Contract | `discovery.TestSemanticScholarRetriesAfterRateLimit`；`discovery.TestSemanticScholarGivesUpAfterMaxRetries`；`discovery.TestOpenAlexDegradesOnInvalidAbstractWithoutLosingWork` | 无退避/降级（Red） | `go test ./internal/discovery` 全绿 |
| AC-013/014 | Contract | `discovery.TestQwenWebSearchWithoutKeyIsUnavailable`；`discovery.TestQwenWebSearchRequiresURLCitation`；`discovery.TestDisabledWebSearchSkips` | Web Search 无降级/URL 校验（Red） | `go test ./internal/discovery` 全绿 |
| AC-015/016 | Unit + Integration | `query.TestConflictGrouping`；`query.TestWebContextNeverVerified`；`integration.TestIntegrationStage3ConflictBrief`；`integration.TestIntegrationStage3EvidenceDegradation` | 冲突/证据层级未实现（Red） | `go test ./internal/query ./internal/integration` 全绿 |
| AC-017 | Integration + Contract | `integration.TestIntegrationStage3WorkspaceIsolation`；`httpserver.TestContractDiscoveryRoutes` | 跨 workspace 可读（Red） | `go test ./internal/integration ./internal/httpserver` 全绿 |
| AC-018 | Unit | `eval.TestStage3Metrics`；`eval.TestScoreQuestionStage3Intents`；`eval.TestEvalModeForDefaultsToMyLibrary` | gap_precision / 意图指标缺失（Red） | `go test ./internal/eval` 全绿 |

### 6.1 真实 Provider 补充验证

- 解析健壮性：`discovery.TestSemanticScholarDecodesMixedExternalIDs`（`externalIds` 混合字符串与数字）、`discovery.TestSemanticScholarCleansControlCharactersBeforeDecode`（字符串内非法控制字符）。
- 成本受控手工验证：`worker discovery-smoke`（每次运行对每个来源最多 1 次请求，只输出状态与公开计数/标题，不落库、不打印密钥）。

## 7. 安全、隐私与生命周期

- workspace/scope：`external_works`、citation 查询、gap 与 discovery 响应全部显式带 `workspace_id`；跨 workspace 返回 `not_found` 或 `forbidden`。
- 凭据与日志：`S2_API_KEY`、`DASHSCOPE_API_KEY` 只从环境变量或权限受限本地配置读取；不写入仓库、日志、fixture 或 Brief。
- 隐私出站：Web Search query 必须去标识化，不得包含个人 fulltext、私人问题原文或私人元数据。
- 删除与保留：`external_works` 随 workspace 生命周期清理，不参与 30 天个人文档 purge 承诺；主动清空索引后，新查询不得复用过期的库内去重结果。
- Provider 降级：任何外部来源失败必须显式体现为 `source_status` 与用户可见降级说明，不得静默漏答。
- 审计：记录来源、provider、model、retry、降级和 query fingerprint，不记录完整 query 或私人内容。

## 8. 迁移、发布与回滚

- 迁移步骤：新增 `0004_stage3_external_discovery.sql`，使用 `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` 与 `CREATE INDEX IF NOT EXISTS`；不得修改 `0001`、`0002`、`0003`。
- 兼容窗口：`external_works` 旧行为空或仅测试数据；旧 Brief 与 `gaps` 记录继续可读。
- 回滚方式：关闭 `DISCOVERY_ENABLED` 与 `WEB_SEARCH_ENABLED` 配置即可退回阶段 2 行为；保留新增列、索引与外部数据，不做破坏性 down migration。
- 监控信号：discovery 调用成功率、按来源的限流/超时计数、去重命中率、缺口数量、Web Search 跳过与降级次数、`gap_fill`/`landscape` hard gate failures。

## 9. 风险与待确认项

| 风险/问题 | 影响 | 默认决策 | 最迟决策点 | 状态 |
|---|---|---|---|---|
| 无真实 Provider Key 时只能验证 stub/fixture | 无法证明真实覆盖与限流表现 | stub/fixture 进 CI；真实调用只在用户明确授权后用本地忽略凭据手工验证 | 试点前 | 接受 |
| `canonical` 分位阈值缺少真实数据校准 | 可能误判经典工作或漏判 | 先用可配置分位与综述类型规则，记录阈值版本 | 阶段 3 收口前 | 待校准 |
| 冲突判定为确定性启发式 | 冲突识别的准确率有限 | 只在同方法/主题且结论相反时标记，保守降级 | 阶段 4 前 | 接受 |
| Web Search 成本与配额不可控 | 可能触发费用或限流 | 只在 `landscape` 且结构化不足时调用，设置超时与次数上限 | 试点前 | 接受 |
| 外部来源字段缺失（无摘要/无年份） | 影响归一化与缺口判定 | 缺失即降级，不猜测、不补全 | 实现前 | 接受 |
| 现有 S2 key 返回 403（无效/过期） | 认证受限，只能走无 key 的严格限流公共池 | 请求新 key；无 key 时由 429 退避兜底，不阻断 turn | 试点前 | 待用户提供新 key |
| 现有 DashScope key 返回 401，且旧 base_url 为自建 MaaS rerank 端点 | `landscape` 的 Web Search 不可用（显式降级） | 请求支持 `enable_search` 的百炼 key/base_url/模型 | 试点前 | 待用户提供 |

## 10. 验证证据

- Red：阶段 3 规格冻结后，先落 `docs/changes/RS-SPEC-0004`、OpenAPI discovery 路由与各 AC 对应测试标识，再实现 `internal/discovery`、`internal/query`、`internal/storage/discovery.go`、`migrations/0004_stage3_external_discovery.sql`。
- Green（离线，全绿）：
  - `go build ./...`；`go vet ./...` 通过。
  - `make verify`（= `openapi-lint` + 单测 + 集成 + 契约）通过。
  - `go test ./internal/discovery ./internal/query -race -count=1` 通过。
  - 新增 `internal/integration/stage3_integration_test.go`：`TestIntegrationStage3WorkspaceIsolation`、`TestIntegrationStage3GapRelativeToScope`、`TestIntegrationStage3EvidenceDegradation`、`TestIntegrationStage3ConflictBrief` 全部通过。
- 手工/探索验证（真实来源，成本受控，2026-10-17）：
  - `worker discovery-smoke`（无 key S2）→ `semantic_scholar: available`，一次 bulk 查询返回 129 条含摘要+DOI 结果。
  - `worker discovery-smoke`（OpenAlex）→ `openalex: available`，5 条结果，inverted abstract 还原成功、DOI 齐全。
  - `worker discovery-smoke -web`（旧 DashScope key）→ `qwen_web_search: unavailable`，`web_search_http_401`，其余来源不受影响。
  - 真实调用仅在人工执行 smoke 时发生；CI 与默认运行保持 `DISCOVERY_ENABLED`/`WEB_SEARCH_ENABLED` 关闭。
- 已知限制：
  - 旧 S2 key 无效（403），当前真实 S2 验证走无 key 公共池；生产/试点需替换为有效 key。
  - 旧 DashScope 凭据仅覆盖自建 rerank 端点，`landscape` 的真实联网搜索尚未联通，需支持 `enable_search` 的凭据。
  - `canonical` 分位阈值尚未用真实数据校准，按规格先用可配置规则并记录阈值版本。
  - S2 bulk 端点的 `limit` 参数不严格生效（返回整页），分页以返回 `token`/`next` 为准，单页上限 1000。

## 11. 决策记录

| 日期 | 决策 | 原因 | 依据 |
|---|---|---|---|
| 2026-10-17 | Web Search 只在 `landscape` 按需启用，且结果只作 `web_context` | 科学结论必须由个人全文或结构化摘要支撑，避免低质量网页污染结论 | PRD 证据规则；ADR-015 |
| 2026-10-17 | S2 与 OpenAlex 并列接入，不先只做一个 | 两个来源互相补全覆盖，归一化与去重逻辑需要同时成立 | 技术设计 Provider 章节 |
| 2026-10-17 | 引用图扩展延后 | 先验证关键词+结构化发现 baseline，避免为未证明收益增加复杂度和配额消耗 | 技术设计质量优化章节 |
| 2026-10-17 | 缺口必须区分 `personal_library` / `current_scope` / `external_only` | “文献不在库里”和“不在当前 scope 里”对用户是完全不同的问题 | PRD 缺口规则 |
