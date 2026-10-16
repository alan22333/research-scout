# Research Scout 数据模型与生命周期 v1.1

| 项目 | 内容 |
|---|---|
| 状态 | 设计冻结；可直接用于 Goose migration |
| 日期 | 2026-10-16 |
| 数据库 | PostgreSQL 16 + pgvector + pg_trgm |
| 主键策略 | UUIDv7；高写入事件表可使用 bigint identity |
| 时间策略 | 所有持久化时间使用 UTC `timestamptz`，API 输出 RFC3339 |

## 1. 通用规则

- 所有用户业务表必须包含 `workspace_id`，检索和后台任务必须携带该边界。
- 逻辑删除使用 `deleted_at`、`removed_at` 或 `lifecycle_status`；清理任务使用 `purge_after`。
- 外部来源 ID、DOI、URL、获取时间和来源状态必须保留，便于引用与审计。
- Provider 原始返回不得直接覆盖领域字段；先经过归一化、校验和版本判断。
- 论文内容更新不覆盖旧版本，简报引用固定到 `document_version_id` 和 `chunk_id`。
- JSONB 只用于变化快、无需关系约束的扩展数据；scope、证据和引用关系仍使用关系表。

## 2. 身份与工作区

| 表 | 关键字段 | 约束与用途 |
|---|---|---|
| `actors` | `id`, `external_subject`, `display_name`, `created_at` | 本地适配器或未来 IdP 的 actor |
| `workspaces` | `id`, `name`, `created_at` | 数据隔离根 |
| `workspace_members` | `workspace_id`, `actor_id`, `role`, `created_at` | PK(`workspace_id`,`actor_id`)；角色为 `user/admin` |

`role=admin` 必须来自显式授权，不能根据教师身份推断。服务端从身份适配器注入 actor/workspace，拒绝信任普通请求体中的 tenant 字段。

## 3. Zotero 与文档

### 3.1 `zotero_connections`

关键字段：`id`, `workspace_id`, `actor_id`, `provider`, `library_type`, `library_id`, `encrypted_credential`, `credential_key_version`, `status`, `last_library_version`, `last_sync_at`, `last_success_at`, `coverage_jsonb`, `created_at`, `updated_at`, `revoked_at`, `purge_at`。

- `provider` v1 固定为 `web_api`。
- `library_type` v1 固定为 `user`。
- 唯一约束：`(workspace_id, provider, library_id)`。
- `encrypted_credential` 仅由服务端解密；日志、API 和前端永远不能返回明文。
- `coverage_jsonb` 是最近一次成功同步的可重建缓存，不是事实源。
- 状态：`healthy | degraded | disconnected | revoked`。

### 3.2 `zotero_sync_runs`

关键字段：`id`, `connection_id`, `workspace_id`, `mode`, `status`, `from_version`, `to_version`, `cursor_jsonb`, `counters_jsonb`, `error_code`, `error_message`, `started_at`, `finished_at`。

- `mode = full | incremental | retry`。
- `status = pending | running | partial | complete | failed | canceled`。
- 同一个连接只允许一个 `running` 同步运行；重复请求返回现有运行或 `conflict`。
- `cursor_jsonb` 保存当前页、attachment 集合和重试位置，支持断点续跑。

### 3.3 `collections`

关键字段：`id`, `workspace_id`, `connection_id`, `zotero_key`, `parent_key`, `name`, `version`, `is_deleted`, `updated_at`。

- 唯一约束：`(connection_id, zotero_key)`。
- `parent_key` 只表示关系，不作为外键；Zotero 树可能在多次同步间移动。
- 名称用于展示，不能作为 scope 或幂等键。

### 3.4 `documents`

关键字段：`id`, `workspace_id`, `connection_id`, `zotero_item_key`, `item_type`, `title`, `abstract`, `year`, `venue`, `doi_normalized`, `authors_json`, `identifiers_json`, `active_version_id`, `index_status`, `evidence_type`, `zotero_version`, `content_hash`, `last_seen_at`, `removed_at`, `purge_after`, `created_at`, `updated_at`。

- 唯一约束：`(connection_id, zotero_item_key)`。
- `doi_normalized` 使用小写、去除 URL/`doi:` 前缀的形式。
- `active_version_id` 只在文档完整发布后指向 `document_versions`。
- `index_status = pending | indexed | metadata_only | failed | skipped`。
- `evidence_type = personal_fulltext | structured_abstract | NULL`；`metadata_only` 不写入该字段。
- `removed_at` 后立即退出检索；`purge_after` 默认 30 天。

### 3.5 `document_versions`

关键字段：`id`, `document_id`, `workspace_id`, `version_no`, `source_kind`, `fulltext`, `abstract`, `content_hash`, `published_at`, `created_at`。

- 唯一约束：`(document_id, version_no)`。
- `source_kind = fulltext | abstract | metadata`。
- 内容 hash 变化才创建新版本；相同 hash 只更新 `last_seen_at`。
- 删除文档不删除已发布版本；清理任务按生命周期统一删除。

### 3.6 `document_collections`

结构：`document_id`, `collection_id`, `workspace_id`。

- PK：`(document_id, collection_id)`。
- collection 移动/重命名不复制文档，只更新关系。
- 查询 scope 通过该表和 collection 树展开，不能把 collection 名称直接拼入 SQL。

### 3.7 `index_epochs` 与 `index_epoch_documents`

`index_epochs` 是查询一致性快照，不用 `documents.active_version_id` 代替全局版本。

关键字段：`id`, `workspace_id`, `status`, `source_sync_run_id`, `embedding_version`, `retrieval_config_version`, `created_at`, `published_at`, `retired_at`。

- `status = building | active | retired`。
- 每个 workspace 同时最多一个 `active` epoch，必要时通过 partial unique index 约束。
- 一个 epoch 发布后不可变；新的同步或重建必须创建新的 `building` epoch。
- 只有全部文档成员、chunk、embedding 和校验完成后，才允许从 `building` 原子切换为 `active`。

`index_epoch_documents` 固定 epoch 内的文档版本：

结构：`epoch_id`, `workspace_id`, `document_id`, `document_version_id`, `included_at`。

- PK：`(epoch_id, document_id)`。
- 查询使用 `index_epoch_documents.document_version_id`，不能只依赖 `documents.active_version_id`。
- 删除或撤权文档不得出现在新 epoch；旧 epoch 仍可用于历史 brief 的可追溯展示，但内容按 tombstone 生命周期清理。
- 旧 brief、query turn 和 evidence 必须固定引用发布时的 epoch。

## 4. 分块与向量

### 4.1 `chunks`

关键字段：`id`, `index_epoch_id`, `document_version_id`, `document_id`, `workspace_id`, `ordinal`, `section`, `page_start`, `page_end`, `char_start`, `char_end`, `content`, `token_count`, `search_tsv`, `embedding`, `embedding_version`, `created_at`。

- 唯一约束：`(index_epoch_id, document_version_id, ordinal)`；chunk 是 epoch 内不可变行，不能只按 document version 唯一。
- `embedding` 为配置维度向量，v1 默认迁移为 `vector(1024)`；在阶段 0 通过真实 Provider 合同测试确认维度后才能冻结迁移。
- `embedding_version = provider + base_url + model + dimension + normalization_version + query_instruction_hash + passage_instruction_hash + truncation_policy_version` 的稳定 hash。
- `EmbeddingProvider` 必须能报告实际输出维度和是否支持可控 `dimensions`；启动时与 `EMBEDDING_DIM` 不一致必须 fail fast。
- 模型、维度、归一化或 instruction 变化时创建新 epoch；同一 document version 可在不同 epoch 拥有内容相同但 embedding 不同的 chunk 行，不原地覆盖旧向量。
- HNSW 索引：`embedding vector_cosine_ops`；FTS 使用 `GIN(search_tsv)`；查询必须同时限定 `index_epoch_id`。
- 为无法可靠定位的文件保留 `section` 和文档级 locator，不伪造页码。

### 4.2 分块规则

- 目标长度 800 token，重叠 120 token，单块最大 1500 token。
- 优先按标题、段落、列表和公式/代码边界切分，再退化到句子边界。
- 章节标题、页码/偏移、父文档和版本必须随 chunk 持久化。
- abstract 级文档允许只有一个 chunk，但仍使用相同检索接口。

## 5. 项目、会话与简报

| 表 | 关键字段 | 说明 |
|---|---|---|
| `projects` | `id`, `workspace_id`, `name`, `description`, `default_mode`, `scope_mode`, `created_by_actor_id`, `created_at`, `updated_at`, `deleted_at` | 研究项目工作对象 |
| `project_scopes` | `id`, `project_id`, `workspace_id`, `scope_mode`, `version`, `created_at` | `all_library` 或 `selected_collections` 的版本快照 |
| `project_scope_collections` | `project_scope_id`, `collection_id`, `workspace_id` | selected scope 的 collection 集合 |
| `query_sessions` | `id`, `project_id`, `workspace_id`, `mode`, `status`, `phase`, `plan_json`, `source_status_json`, `current_turn_id`, `created_at`, `completed_at`, `deleted_at`, `purge_after` | 一次调研会话 |
| `query_turns` | `id`, `session_id`, `workspace_id`, `turn_no`, `question`, `mode`, `scope_snapshot_json`, `index_epoch_id`, `planner_version`, `retrieval_config_version`, `prompt_version`, `status`, `phase`, `created_at`, `completed_at` | 首次提问和每个追问；必须固定检索快照和配置版本 |
| `query_events` | `id`, `session_id`, `workspace_id`, `seq`, `type`, `payload_json`, `created_at` | SSE 持久化事件 |
| `briefs` | `id`, `session_id`, `turn_id`, `workspace_id`, `schema_version`, `summary`, `findings_json`, `method_timeline_json`, `chronology_json`, `open_questions_json`, `source_status_json`, `index_epoch_id`, `retrieval_config_version`, `prompt_version`, `created_at` | 只读版本化简报；`findings_json` 是 API 读模型，claim 事实源见 `claims` |
| `evidence` | `id`, `brief_id`, `workspace_id`, `index_epoch_id`, `evidence_type`, `document_id`, `document_version_id`, `chunk_id`, `external_work_id`, `title`, `url`, `quote`, `locator_json`, `score`, `retrieved_at`, `source_meta_json` | 简报引用的最小证据单元 |
| `claims` | `id`, `brief_id`, `workspace_id`, `claim_type`, `claim_no`, `text`, `support_level`, `confidence`, `verification_status`, `created_at` | summary、finding、method、chronology 等 claim 的事实源 |
| `claim_evidence` | `claim_id`, `evidence_id`, `workspace_id`, `stance`, `support_score`, `evidence_span`, `locator_json`, `created_at` | claim 与 evidence 的支持、反驳或背景关系 |
| `gaps` | `id`, `brief_id`, `gap_type`, `missing_relative_to`, `external_work_id`, `document_id`, `explanation`, `score`, `created_at` | 三类缺口；可多项归组 |

约束：

- `query_events` 唯一约束为 `(session_id, seq)`；seq 在事务内单调递增。
- `briefs` 不覆盖旧版本；追问创建新 turn 和新 brief。
- `evidence` 必须引用至少一个 `chunk_id`、`document_version_id` 或 `external_work_id`。
- `metadata_only` 文档可出现在缺口或 scope 说明中，但不得写入 `evidence`。
- `claims` 的 `claim_type = summary | finding | method | chronology`；`verification_status = pending | verified | partial | rejected`。
- 每个 `verified` 或 `partial` claim 至少有一条 `claim_evidence`；没有证据的句子只能作为 `open_questions` 或明确背景文本。
- `claim_evidence.stance = supports | refutes | background`；`evidence_span` 必须能回到 evidence 的 quote 或 locator。
- `gaps.missing_relative_to = personal_library | current_scope | external_only`。

## 6. 外部工作

### 6.1 `external_works`

关键字段：`id`, `workspace_id`, `identity_key`, `doi_normalized`, `s2_id`, `openalex_id`, `pmid`, `title`, `abstract`, `year`, `venue`, `citation_count`, `type`, `source_json`, `first_seen_at`, `updated_at`。

- 唯一约束：`(workspace_id, identity_key)`。
- `identity_key` 的优先级：DOI → S2 ID → OpenAlex ID → PMID → 标题 fingerprint + 年份。
- 标题匹配必须有年份、作者或稳定 ID 辅助，不能仅凭短标题自动合并。
- S2/OpenAlex 的原始 ID、URL 和响应时间保留在 `source_json`。
- 外部工作可以缓存，但不能被当作个人全文证据；进入简报时仍映射为 `structured_abstract` 或 `web_context`。

## 7. 审计与清理

### 7.1 `audit_events`

关键字段：`id`, `workspace_id`, `actor_id`, `action`, `target_type`, `target_id`, `reason`, `request_id`, `metadata_json`, `created_at`。

- 只追加，不更新、不逻辑删除。
- 管理员访问派生内容前必须重新认证；认证和写入审计失败时拒绝访问。
- 普通用户只能读取与自己 workspace 相关的审计记录。

### 7.2 `lifecycle_tombstones`

关键字段：`id`, `workspace_id`, `object_type`, `object_id`, `reason`, `effective_at`, `purge_after`, `status`, `last_error`, `completed_at`。

- 对象类型：`zotero_connection | document | project | query_session | account`。
- `status = active | purging | purged | retry`。
- 恢复备份后必须重放 tombstone，确保恢复不能绕过删除承诺。
- 清理范围包含 fulltext、chunk、embedding、scope 关系、query/event/brief 关联和缓存。

## 8. 索引与查询计划

必须存在的索引：

- `zotero_connections(workspace_id, status)`。
- `zotero_sync_runs(connection_id, started_at desc)`。
- `collections(connection_id, parent_key)` 和 `(connection_id, is_deleted)`。
- `documents(workspace_id, index_status)`、`(workspace_id, doi_normalized)`、`(workspace_id, removed_at, purge_after)`。
- `document_collections(collection_id, document_id)`。
- `chunks(index_epoch_id, document_version_id, ordinal)`、`GIN(search_tsv)`、HNSW(`embedding vector_cosine_ops`)。
- `index_epochs(workspace_id, status)`，并保证每个 workspace 最多一个 `active` epoch。
- `index_epoch_documents(epoch_id, document_id)`、`(document_id, epoch_id)`。
- `query_sessions(workspace_id, deleted_at, created_at desc)`。
- `query_events(session_id, seq)`。
- `claims(brief_id, claim_type, claim_no)`、`claim_evidence(claim_id, evidence_id)`。
- `evidence(brief_id, evidence_type, score desc)`。
- `gaps(brief_id, gap_type)`。
- `lifecycle_tombstones(status, purge_after)`。

检索语句必须在同一条 SQL/事务链中应用 workspace、scope、active version、tombstone 和文档状态过滤，不能先查全库再在应用层过滤。

## 9. 备份与恢复

- PostgreSQL 每日加密备份，保留 14 天；具体 RPO/RTO 在生产部署阶段复核。
- v1 备份排除 `document_versions.fulltext`、`chunks.content` 和 embedding，避免备份绕过用户清理。
- 收藏 metadata、scope、项目、tombstone、index epoch 元数据、Provider 配置引用和任务状态；恢复后重新从 Zotero 拉取 fulltext 并重建 chunk/embedding。
- 恢复演练必须验证：tombstone 重放、连接状态、scope、引用可追溯、删除对象不可检索。
- 真实 PDF 不进入服务端，因此不存在 PDF 备份责任。

## 10. 生命周期状态

```text
LifecycleStatus = active | removed_pending_purge | purged
IndexStatus = pending | indexed | metadata_only | failed | skipped
IndexEpochStatus = building | active | retired
EvidenceType = personal_fulltext | structured_abstract | web_context
ConnectionStatus = healthy | degraded | disconnected | revoked
SyncRunStatus = pending | running | partial | complete | failed | canceled
SourceStatus = available | degraded | unavailable | skipped
QueryPhase = accepted | planning | retrieving_library | retrieving_scholarly | retrieving_web | ranking | drafting | verifying | completed
QueryIntent = known_item | library_qa | method_comparison | gap_fill | landscape | temporal | conflict_check | unanswerable
RetrievalMode = my_library | gap_fill | landscape
ClaimType = summary | finding | method | chronology
ClaimStance = supports | refutes | background
VerificationStatus = pending | verified | partial | rejected
GapType = canonical | related_method | latest_progress
MissingRelativeTo = personal_library | current_scope | external_only
```

- Zotero 条目删除：立即 `removed_pending_purge`，30 天后 `purged`。
- 撤权/断开连接：立即停止同步并将相关文档移出检索；30 天后清除派生数据。
- 主动清空索引：确认后用同步清理事务立即删除。
- 删除项目/会话：立即不可见，30 天后删除关联内容。
- 删除账户：撤销会话并清除所有派生数据；恢复备份后仍按 tombstone 执行。
