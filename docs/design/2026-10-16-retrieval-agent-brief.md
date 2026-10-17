# 检索、Agent 与简报设计 v1.1

| 项目 | 内容 |
|---|---|
| 状态 | 设计冻结；可直接实现，变更受 RAG 质量 Gate 约束 |
| 日期 | 2026-10-16 |
| 检索核心 | PostgreSQL FTS + pgvector + RRF + optional rerank |
| Agent | Go 有界状态机，不使用自由循环 |
| 输出 | 结构化 ResearchBrief + 可追溯 Evidence |

## 1. 模式与工具权限

| 模式 | 个人文库 | S2/OpenAlex | 原生联网搜索 | 默认目标 |
|---|:---:|:---:|:---:|---|
| `my_library` | 必须 | 禁止 | 禁止 | 回答个人文库中的问题 |
| `gap_fill` | 可选 | 必须 | 禁止 | 找个人文库缺失的工作 |
| `landscape` | 可选 | 必须 | 按需 | 形成带背景的全景简报 |

Planner 只能建议升级模式或补充 scope；用户未确认前不得调用扩大范围的工具。

## 2. Query Planner

Planner 输出结构化对象，不输出隐藏推理：

```json
{
  "language": "zh",
  "intent": "method_comparison",
  "route": "hybrid_library",
  "topics": ["side-channel", "mitigation"],
  "entities": ["AES", "cache"],
  "time_range": {"from": null, "to": null},
  "filters": {
    "year_from": null,
    "year_to": null,
    "venue": [],
    "authors": [],
    "item_types": [],
    "has_fulltext": true
  },
  "method_terms": ["constant-time", "masking"],
  "requires_fulltext": true,
  "recommended_mode": "gap_fill",
  "subqueries": [
    {"query": "side-channel mitigation cache", "language": "en", "purpose": "library", "weight": 1.0}
  ],
  "retrieval_weights": {"lexical": 1.0, "vector": 1.0, "rerank": 1.0}
}
```

固定查询意图：

```text
known_item | library_qa | method_comparison | gap_fill
| landscape | temporal | conflict_check | unanswerable
```

固定路由：

```text
known_item       -> metadata_exact_then_hybrid
library_qa       -> hybrid_library
method_comparison -> hybrid_library_fulltext_first
gap_fill         -> hybrid_library_then_discovery
landscape        -> hybrid_library_discovery_web
temporal         -> hybrid_library_with_time_filter
conflict_check   -> multi_source_evidence_compare
unanswerable     -> scoped_abstention
```

规则：

- 中文问题可生成英文检索词，但原始问题必须保留并用于最终简报。
- `known_item` 必须先按 DOI、标题、作者和年份做精确/模糊元数据匹配，再允许补充语义检索。
- 元数据过滤条件必须由服务端归一化并校验；模型不能生成 workspace、collection 或权限绕过条件。
- `requires_fulltext=true` 时，摘要级证据必须显示不确定性。
- Planner 生成 subquery 上限为 5；不得生成无限追问循环。
- Planner 失败时使用确定性关键词和原问题，不阻断 `my_library`。

## 3. 个人文库检索

### 3.1 过滤顺序

所有候选先应用：

1. `workspace_id`。
2. active `document_version_id`。
3. collection scope 展开结果。
4. `removed_at IS NULL` 和 lifecycle `active`。
5. tombstone、连接撤权和已清空索引过滤。
6. 证据等级和文档状态。

### 3.2 混合检索参数

默认参数如下，具体值必须由 [`RAG 质量与评测`](./2026-10-16-rag-quality-evaluation.md) 的 baseline 和消融实验校准：

```text
FTS candidates: 40
Vector candidates: 40
Fusion: weighted Reciprocal Rank Fusion, k=60
Rerank candidates: 30
Final evidence chunks: 12
Final documents: 6
Per-document evidence cap: 2
Context budget: approximately 12K tokens
```

FTS 主要检索英文内容和标题/摘要；中文原始 query 用于向量检索和 LLM 查询改写。`pg_trgm` 用于标题、作者、DOI 的模糊补召回，不作为最终证据评分。

检索执行规则：

- FTS、向量检索和多个 subquery 可以在有界并发下并行执行，但结果必须在同一 `index_epoch_id` 上合并。
- 查询先用 `index_epoch_documents` 固定文档版本，并将 `chunks.index_epoch_id` 限定为同一 epoch，再应用 workspace、scope、tombstone、生命周期和证据状态过滤。
- `known_item` 的精确匹配结果具有强制保底；语义分数不能把精确标题/DOI 挤出最终结果。
- 多 subquery 使用带权重的 RRF：

```text
score(d) = Σ query_weight(q) * source_weight(s) / (60 + rank_s,q(d))
```

- 精确术语、标题、DOI、作者可使用更高 lexical 权重；开放语义问题可提高 vector 权重。
- 若 reranker 不可用，使用 RRF 分数和向量相似度组合；不得把 `metadata_only` 加入候选证据。

### 3.3 证据选择与上下文组织

最终 evidence 不是简单取 top 12：

- 先按文档聚合，使用 per-document cap 防止单篇论文占据上下文。
- 每个 subquery 至少保留一定配额，再用 MMR 或覆盖度策略补足多样性。
- 命中 chunk 后可扩展到父 section、相邻 chunk 或抽象层上下文，但不能越过 `index_epoch` 和文档版本。
- fulltext 命中时优先保留原文 span 和 locator；abstract 命中时只保留摘要级 claim。
- context 组装必须为结论、方法和时间脉络分别保留证据，不能只按分数堆叠 chunk。
- 12K token 是总预算，必须扣除系统提示、输出预算和引用元数据；预算不足时优先保留高价值 fulltext 和未覆盖 claim。

### 3.4 结果归一化

- DOI 优先，其次 S2/OpenAlex/PMID/OpenAlex ID，再以标题+年份做受控归一化。
- 同一 document 的多个 chunk 先合并到文档，再按文档上限选取代表性 chunk。
- 相同内容的 chunk 去重；冲突来源保留并标 `conflict=true`。
- 每条证据保存 citation locator、版本号、来源类型和得分。

## 4. 外部发现

### 4.1 Semantic Scholar

- 使用官方 Graph API 的论文搜索和详情接口。
- 默认 1 request/second，429 使用退避和断点续跑。
- 获取 DOI、paperId、title、abstract、year、venue、authors、citation count、reference/citation 信息（可用时）。
- S2 结果只能作为 `structured_abstract`；摘要缺失时作为发现项，不能伪造摘要。

### 4.2 OpenAlex

- 使用 works/search 与 works/{id}，带 `OPENALEX_EMAIL` 进入 polite pool。
- 获取 DOI、OpenAlex ID、title、abstract inverted index、year、venue、authors、cited_by_count、type。
- inverted abstract 必须无损还原，不能猜测缺失句。

### 4.3 引用图扩展（质量优化）

关键词搜索不能可靠完成“找漏”。在 baseline 证明需要后，`gap_fill` 和 `landscape` 应支持从库内高相关种子工作扩展：

- 读取种子工作的 references 和 citations。
- 结合 bibliographic coupling、共被引、作者/venue 和时间过滤做二次召回。
- 引用图扩展结果仍走统一的 DOI/ID 归一化、去重、证据层级和缺口判定。
- 引用数不能单独代表权威性；必须与综述类型、venue、时间和相关性共同判断。
- 引用图调用失败只降低发现覆盖，不得阻断 `my_library`。

### 4.4 外部工作归一化

`identity_key` 优先级：

```text
DOI -> S2 ID -> OpenAlex ID -> PMID -> title fingerprint + year
```

标题 fingerprint 只用于候选合并；没有年份、作者或稳定 ID 时不得自动合并。外部结果如果命中个人文库，必须显示“库内已有”而不是缺口。

## 5. Provider-native Web Search

### 5.1 研究结论

主流产品通常使用模型供应商自己的搜索 grounding，而不是统一依赖一个第三方搜索 API：

- OpenAI 使用 hosted `web_search` 和 URL citations。
- Gemini 使用 Google Search grounding 和 grounding metadata。
- Claude 使用 server-side web search tool 和 citations。
- DeepSeek 公开 API 当前明确支持函数工具，没有将服务端联网搜索作为稳定 API 前提。
- Qwen/DashScope 的 OpenAI 兼容 Chat Completions 支持 `enable_search`，并可返回 `search_info`、来源 URL 和时效参数。

因此 v1 不默认接 Tavily、Brave 或自建爬虫，而是冻结 `WebSearchProvider` 能力边界。

### 5.2 Qwen 实现

首选调用 OpenAI 兼容 Chat Completions：

```json
{
  "model": "qwen-plus",
  "messages": [{"role": "user", "content": "..."}],
  "enable_search": true,
  "search_options": {
    "enable_source": true,
    "search_strategy": "standard"
  }
}
```

适配器：

- 优先解析 `search_info`/tool output 中的标题、URL、时间和可用片段。
- `search_info` 缺失时，可从回答正文的 Markdown/裸 HTTP(S) 链接降级提取 URL；状态固定为 `degraded`，reason 为 `web_search_content_fallback`。
- 只有带 URL 的结果可以形成 `web_context` Evidence。
- 不把模型生成的自然语言总结冒充网页原文；它只能作为带引用的背景。
- 需要时效问题时允许设置受控 `freshness`；默认不设置，避免误删经典工作。
- 没有 Key、请求失败或没有 URL citation 时返回 `web_search_unavailable`。

### 5.3 证据扩展

如果联网结果包含 DOI、论文 URL 或清晰论文标题：

1. 先归一化 DOI/标题。
2. 调用 S2/OpenAlex 获取结构化摘要和元数据。
3. 新结果作为 `structured_abstract`；原网页仍保留为 `web_context`。
4. 不能用网页摘要直接替换 `personal_fulltext` 或 `structured_abstract`。

## 6. Agent 状态机

```text
accepted
  -> planning
  -> retrieving_library
  -> retrieving_scholarly
  -> retrieving_web
  -> ranking
  -> drafting
  -> verifying
  -> completed
```

条件跳过：

- `my_library` 跳过 scholarly/web。
- `gap_fill` 跳过 web。
- `landscape` 在结构化来源已足够时仍可跳过 web，但必须记录跳过原因。
- 任一步失败进入 `degraded` 或 `failed`；局部来源失败不把其他来源结果丢弃。

固定限制：

- 单个 turn 最多 8 个工具步骤。
- 120 秒软超时；超时后保留已完成阶段，返回 partial brief。
- 最多 5 个 subquery、40 个外部候选、20 个网页来源。
- 每个工具调用和重试都写 `query_events`。

工具权限由服务端根据 mode 强制，不能只依赖 Planner 的 JSON。

## 7. 证据充分性与冲突

### 7.1 支持等级

- `sufficient`：关键 claim 至少有一个 `personal_fulltext` chunk，或两个独立 `structured_abstract` 来源，且没有未解释冲突。
- `partial`：只有单一摘要、摘要覆盖不完整、仅方法线索或外部来源有缺项。
- `insufficient`：只有 `web_context`、metadata-only、来源失效或存在无法解释的冲突。

### 7.2 Claim 数据契约

Brief 中每个实质性 claim，包括 summary、finding、method 和 chronology，必须持久化为：

```json
{
  "claim_id": "c1",
  "claim_type": "finding",
  "text": "可核验结论",
  "support_level": "sufficient",
  "confidence": 0.86,
  "evidence_spans": [
    {
      "evidence_id": "uuid",
      "stance": "supports",
      "quote": "支持该 claim 的最小原文片段",
      "locator": {"chunk_id": "uuid", "section": "4.2"}
    }
  ]
}
```

验证规则：

- `personal_fulltext` 必须能回到 document/version/chunk locator。
- `structured_abstract` 必须能回到 S2/OpenAlex 元数据或摘要来源。
- `web_context` 必须带 URL 和获取时间，且不能单独承担科学事实。
- 引用存在不代表引用支持 claim；至少一条 evidence 必须与 claim 的语义方向一致。
- `summary` 中的实质性句子必须映射到 `claim_type=summary` 的记录。
- 发现冲突时输出多个 claim 或 `open_questions`，不能用一个综合句掩盖。
- `refutes` evidence 必须保留并触发冲突展示；不能只保留支持证据。

### 7.3 证据冲突判定

- 优先比较 claim、方法、数据集、年份和结论方向，不把术语差异当成实质冲突。
- 同一工作不同版本或同一试验的不同报告应先归入 work cluster，再判断是否冲突。
- 无法自动解释的冲突必须将 claim 标记为 `partial` 或 `insufficient`，并写入 `open_questions`。
- 冲突判定结果进入评测集的 `conflict_check` 意图，不能只靠模型自由判断。

### 7.4 检索内容信任边界

论文正文、摘要和网页内容一律视为不可信数据：

- 检索内容不得作为 system/developer instruction，不得改变工具权限或输出 schema。
- 只有服务端校验过的 QueryPlan 可以触发工具调用。
- 网页内容不得自动触发新的 URL 抓取、文件访问或凭据操作。
- Prompt 中使用明确的数据分隔和来源标记；URL、标题和 snippet 都不能执行指令。
- 发现 prompt injection 迹象时保留来源状态，但不得让注入内容进入 findings。
- 对应失败场景必须进入安全测试和 RAG hard gate。

## 8. 缺口判定

外部候选池先按相关性排序，默认保留 top 50。三类缺口可并列，不强制互斥：

- `canonical`：review/survey 类型，或引用数达到候选集 P90 且至少 50 次引用；候选集小于 10 时使用引用数至少 50 的保守规则。
- `latest_progress`：年份在当前年份前 2 年内，并且相关性位于候选集前 30%。
- `related_method`：相关性和方法词重叠位于前 30%，但不属于上述两类，或额外提供方法链线索。

`missing_relative_to`：

- `personal_library`：外部工作没有匹配到任何个人 document。
- `current_scope`：个人文库存在，但落在当前 collection scope 之外。
- `external_only`：只在外部来源发现，无法确认是否属于用户已同步文库。

缺口解释必须指向“当前范围未覆盖”，不能写成“领域不存在”。

## 9. Brief 生成

生成输入只包含已校验的证据、scope snapshot、`index_epoch_id`、模式和来源状态。输出固定字段：

- `summary`
- `summary_claims`
- `findings`
- `method_timeline`
- `chronology`
- `gaps`
- `open_questions`
- `citations`
- `source_status`

生成后执行一次独立校验：

- 每个 citation ID 存在且可追溯。
- 每个 summary/finding/method/chronology claim 有适合其证据层级的 support relation。
- `metadata_only` 没有进入 claims 或 findings。
- `web_context` 没有单独支撑科学事实。
- 来源失败、scope 限制和认证失败都保留在来源状态与未决问题中。
- 检索内容没有改变工具策略、系统指令或输出 schema。
- 验证失败时降级为 partial brief，不重试无限次。

## 10. 多轮追问

- 追问继承当前项目、默认 scope 和 mode，但请求可以显式覆盖。
- 每个 turn 保存自己的 mode、scope snapshot、来源状态和证据版本。
- 追问可以只聚焦某个 `evidence_id`、文档、缺口或时间段。
- 新证据追加到新 brief，不覆盖此前引用。
- 用户改变 scope 时 UI 和 Brief 均标记“范围已变化”。

## 11. 关键测试

- FTS、向量、RRF 和 rerank 的候选数量与排序稳定性。
- `known_item` 对标题、作者和 DOI 的精确匹配保底。
- 中文问题改写为英文检索词后仍保留原问题。
- `my_library` 在 S2、OpenAlex 和联网搜索全部断开时仍可用。
- fulltext 缺失时摘要降级并降低确定性措辞。
- `metadata_only` 不能进入 evidence。
- S2/OpenAlex 限流时显示来源状态而非零结果。
- 联网搜索无引用时标记 `web_search_unavailable`。
- 外部发现结果与个人文档的去重及 `missing_relative_to` 分类。
- citation graph expansion 的召回提升和失败降级。
- 每个 summary/finding claim 的支持关系、反驳关系和冲突展示。
- 论文或网页中的 prompt injection 不会触发工具调用或改变输出 schema。
- 查询固定 `index_epoch_id`，同步中不会混读新旧文档版本。
- SSE 断线重连、乱序事件和 `Last-Event-ID`。
- 删除/撤权后旧 brief 引用可见但内容不可再次检索。
- RAG 质量指标遵守 [`RAG 质量与评测`](./2026-10-16-rag-quality-evaluation.md) 的 hard gate 和 quality gate。
