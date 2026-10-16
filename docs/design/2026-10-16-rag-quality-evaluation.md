# RAG 质量与评测设计 v1.1

| 项目 | 内容 |
|---|---|
| 状态 | 设计冻结；检索、Agent、Provider 或 Prompt 变更必须执行 |
| 日期 | 2026-10-16 |
| 目标 | 用版本化 golden set、阶段 trace 和可比较指标约束 RAG 优化 |
| 适用范围 | 个人文库检索、外部发现、证据选择、Brief 生成和引用验证 |
| 不适用范围 | UI 视觉、通用 LLM benchmark、模型训练和用户满意度研究 |

## 1. 目标与原则

- RAG 优化不能凭感觉调参；chunk、RRF、rerank、候选数和 Prompt 变更都必须有 baseline 对比。
- 评测必须按查询意图分层，不能用一个平均分掩盖已知文献、方法比较或找漏能力的退化。
- 评测集必须版本化、可复现，并与 `index_epoch`、Embedding 协议、检索配置和 Prompt 版本绑定。
- 默认 CI 只使用脱敏、合成的公开 fixture；真实课题组问题只能保存在本地或私有评测环境。
- Hard gate 与 quality gate 分开：引用越界、workspace 泄漏和 metadata-only 作为证据属于阻断错误；召回率和答案质量属于可迭代指标。

## 2. 查询意图分类

| 意图 | 说明 | 首选路径 | 主指标 |
|---|---|---|---|
| `known_item` | 已知标题、作者、DOI 或确切论文 | 元数据/精确匹配优先 | ExactMatch, MRR@10 |
| `library_qa` | 只基于个人文库回答概念、结论或证据问题 | 混合检索 | Recall@12, nDCG@10, citation precision |
| `method_comparison` | 比较方法、机制、假设或实验 | fulltext 优先的混合检索 | fulltext evidence recall, claim coverage |
| `gap_fill` | 判断个人文库漏掉了哪些工作 | 外部结构化发现 + 缺口规则 | missed-paper precision, gap precision |
| `landscape` | 进入陌生领域并生成全景简报 | 文库 + S2/OpenAlex + 可选网页 | coverage, diversity, citation precision |
| `temporal` | 跟踪最新进展或构建时间脉络 | 时间过滤器 + 外部发现 | chronology accuracy, recency precision |
| `conflict_check` | 检查来源之间是否存在冲突 | 多来源证据比较 | conflict recall, unsupported claim rate |
| `unanswerable` | 当前 scope 或证据层级不足 | 确定性拒答/降级 | abstention precision |

每个版本至少覆盖所有意图；`method_comparison`、`gap_fill` 和 `unanswerable` 每条至少 3 个问题。

## 3. Golden Set 格式

Golden set 以 YAML 或 JSON 保存，建议路径为 `eval/golden/<dataset-version>.yaml`。示例：

```yaml
version: "rag-golden-v1"
workspace_profile: synthetic
questions:
  - id: q001
    intent: known_item
    mode: my_library
    question: "找一下 Attention Is All You Need 这篇论文"
    scope:
      scope_mode: all_library
    relevant_document_ids: ["doc-001"]
    relevant_chunk_ids: ["chunk-001"]
    expected_evidence_types: ["personal_fulltext"]
    expected_claims:
      - "Transformer 使用自注意力替代循环结构"
    unanswerable: false
    notes: "验证精确标题路径和向量召回不能互相掩盖"
```

规则：

- 初始 v1 目标为 20–30 个问题，覆盖全部意图；后续按真实失败案例增补。
- 至少 30% 作为 holdout，不参与 chunk、RRF、rerank 或 Prompt 调参。
- 标注相关文档时同时标注证据层级和是否必须使用 fulltext。
- `unanswerable` 问题必须写明不可回答原因，例如 scope 为空、只有 metadata 或证据冲突。
- 原始真实问题必须去标识化；不得把私人全文、Zotero key、真实 API Key 或未公开研究内容提交到仓库。

## 4. 版本追踪

每次查询和评测运行必须记录：

```text
planner_version
retrieval_config_version
embedding_version
index_epoch_id
rerank_version
prompt_version
schema_version
source_status
```

其中 `embedding_version` 至少包含 provider、model、dimension、normalization、query instruction hash 和 passage instruction hash。没有版本追踪的评测结果不得用于性能结论。

## 5. 指标定义

### 5.1 检索指标

- `Recall@40`：FTS/向量预召回阶段是否包含相关 chunk。
- `Recall@12`：最终证据选择后相关 chunk 的召回率。
- `MRR@10`：第一个相关结果的倒数排名。
- `nDCG@10`：考虑排序位置的相关性质量。
- `Rerank Delta`：rerank 前后 nDCG@10 和 Recall@12 的变化。
- `ExactMatch`：已知文献查询中，标题/DOI/作者精确匹配是否进入前三。

### 5.2 证据与答案指标

- `Citation Precision`：被引用 evidence 中真正支持对应 claim 的比例。
- `Citation Recall`：需要证据支持的 claim 中，至少有一条有效 evidence 的比例。
- `Unsupported Claim Rate`：没有有效 evidence 或无法由 evidence 推出的 claim 比例。
- `Abstention Precision`：系统标记 insufficient/拒绝回答时，确实无法从当前证据得出的比例。
- `Conflict Recall`：存在冲突的来源中，被系统识别并展示的比例。

### 5.3 缺口与覆盖指标

- `Gap Precision`：`canonical`、`related_method`、`latest_progress` 中经 reviewer 或用户确认为真实缺口的比例。
- `Missed-paper Precision`：用户确认“确实漏掉且值得阅读”的外部结果比例。
- `Fulltext Coverage`：个人文库中文档可用的 `personal_fulltext` 比例。
- `Evidence Depth Fit`：回答所需证据层级与实际使用证据层级一致的比例。

### 5.4 运行指标

- p50/p95 端到端延迟。
- 每个 turn 的 LLM、Embedding、Rerank 和 Web Search 费用。
- Provider 降级率、429 率和重试次数。
- 索引 epoch 发布时长和失败率。
- SSE 事件延迟、断线重连率和丢失事件数。

## 6. 阻断性 Hard Gate

以下条件任一失败，不允许发布对应检索/Agent 变更：

- 任意 `metadata_only` 进入科学结论 evidence。
- 任意 finding 或 summary claim 无法追溯到 `evidence_id`、`document_version_id` 或 `external_work_id`。
- 任意旧 brief 读取到新 `index_epoch` 或被删除后的文档内容。
- 任意跨 workspace 检索、缓存或审计泄漏。
- 任意检索内容成功触发未授权工具调用或改变 server-side tool policy。
- `personal_fulltext` 引用无法定位到 chunk/section locator。
- 评测结果缺少必要的版本字段，无法复现。

## 7. 质量 Gate

质量 Gate 采用“先建立 baseline，再禁止回退”的方式，不凭空设定跨领域绝对分。

首次 baseline 形成后，每个优化变更必须满足：

- 主指标不得相对下降超过 2%。
- 至少一个目标指标有统计上可解释的提升。
- 新增或变更的意图不能出现零覆盖或未评测。
- 延迟和成本变化必须显式记录；超出既定预算时必须标记为未批准。

建议的初始试点目标，待 baseline 校准：

- Citation Precision ≥ 0.90。
- Unsupported Claim Rate ≤ 5%。
- Abstention Precision ≥ 0.95。
- `known_item` ExactMatch@3 = 1.00。
- fulltext 缺失时不得生成需要 fulltext 才能支撑的确定性方法结论。

## 8. 消融与实验矩阵

至少支持以下可配置对照：

| 实验 | 对照 |
|---|---|
| 检索路径 | FTS-only / vector-only / hybrid / hybrid + rerank |
| Query Router | 关闭 / 开启 |
| 多 subquery 融合 | 单 query / 多 query / 加权 RRF |
| Chunk | 600 / 800 / 1000 token，重叠 80 / 120 / 160 |
| 候选池 | 20/20 / 40/40 / 80/80 |
| 证据选择 | top-k / per-document cap / MMR / subquery quota |
| 证据层级 | 只用 fulltext / fulltext + abstract / 全层级 |
| 外部发现 | 关键词搜索 / 引用图扩展搜索 |
| Rerank | disabled / provider rerank / RRF fallback |
| Index Epoch | 最新文档版本 / 固定 epoch snapshot |

每次实验只改变一个主要变量，保留原始配置、结果和失败案例。

## 9. 实施顺序

### 阶段 0

- 建立 golden set schema、评测报告 schema 和版本字段。
- 对脱敏 fixture 建立最小指标和 `make eval-rag` 入口。
- 验证 `metadata_only`、scope 和 index epoch 的 hard gate。

### 阶段 2

- 为 `my_library` 建立第一版真实 baseline：FTS-only、vector-only、hybrid、hybrid + rerank。
- 记录每个意图的 Recall@12、nDCG@10、Citation Precision 和 Unsupported Claim Rate。
- 根据 baseline 调整 chunk、候选池、RRF 权重和 per-document cap。

### 阶段 3

- 增加 `gap_fill`、`landscape`、时间型和冲突型评测。
- 对比关键词发现和引用图扩展发现。
- 只有质量指标改善后才扩展 Web Search 使用范围。

## 10. 报告格式

评测输出必须是机器可读 JSON，并至少包含：

```json
{
  "dataset_version": "rag-golden-v1",
  "run_id": "eval_...",
  "retrieval_config_version": "...",
  "embedding_version": "...",
  "index_epoch_id": "...",
  "metrics": {},
  "per_intent": {},
  "hard_gate_failures": [],
  "source_status": [],
  "started_at": "...",
  "finished_at": "..."
}
```

人工评审只补充失败案例、冲突判断和缺口真实性，不替代检索指标。

## 11. 验收命令与产物

后续实现必须提供稳定入口：

```text
make eval-rag
make eval-retrieval
make eval-brief
```

默认产物：

- 指标 JSON。
- 按意图分组的 Markdown 摘要。
- 失败问题清单和对应 evidence trace。
- 当前配置与上一 baseline 的对比。

评测产物不得包含私人全文、完整用户问题、真实 Key 或未脱敏 Provider 响应。
