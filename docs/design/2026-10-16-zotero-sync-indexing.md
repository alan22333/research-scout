# Zotero 同步与索引设计 v1.1

| 项目 | 内容 |
|---|---|
| 状态 | 设计冻结；可直接实现 `WebAPISource`，索引发布使用不可变 `index_epoch` |
| 日期 | 2026-10-16 |
| 接入范围 | Zotero Web API，个人 My Library |
| 原文策略 | 不保存 PDF，不解析 PDF，只保存 Web API 可用的派生文本 |

## 1. 领域接口

### `ZoteroSource`

```go
type ZoteroSource interface {
    Validate(ctx context.Context, credential Credential) (ConnectionInfo, error)
    ListCollections(ctx context.Context, cursor CollectionCursor) (CollectionPage, error)
    GetLibraryVersion(ctx context.Context) (int64, error)
    ChangesSince(ctx context.Context, version int64) (ChangePage, error)
    GetItem(ctx context.Context, itemKey string) (Item, error)
    GetFulltext(ctx context.Context, attachmentKey string) (Fulltext, error)
}
```

约束：

- Source 只返回规范化领域对象和 `SourceError`，不把 HTTP 状态码、SDK 类型或原始 JSON 泄漏给上层。
- `ChangesSince` 返回 `deleted` 集合、`last_modified_version` 和下一页 cursor。
- `GetFulltext` 只在 Zotero Web API 实际返回文本时返回 `Available=true`；空内容不能伪装成全文。
- `Credential` 只在调用边界短暂解密；不得写入日志或 trace attribute。

### `CredentialStore`

```go
type CredentialStore interface {
    Put(ctx context.Context, workspaceID, connectionID uuid.UUID, secret []byte) error
    Get(ctx context.Context, workspaceID, connectionID uuid.UUID) ([]byte, error)
    Delete(ctx context.Context, workspaceID, connectionID uuid.UUID) error
}
```

v1 实现使用服务端 `SECRET_KEY` 进行信封加密。OAuth 1.0a 不在本轮设计；未来只替换 `CredentialSource` 和授权流程。

## 2. Web API 请求规则

- 基址：`ZOTERO_API_BASE`，默认 `https://api.zotero.org`。
- 请求头：`Zotero-API-Key`、`Zotero-API-Version: 3`、稳定的 `User-Agent`。
- 首次同步读取用户 library 的 collection 树和 item 列表，再读取 attachment 元数据。
- 后续同步读取 library version，使用 `?since=<version>` 和版本/删除响应发现变化。
- 所有分页按响应 cursor/`start` 继续，不假定单页包含全部内容。
- 并发上限固定为 4；不得以增大并发绕过 429。
- 429 优先使用 `Retry-After`，缺失时使用指数退避；幂等读请求最多重试 5 次。
- 连接凭据失效、API 版本不兼容和权限不足属于终止型错误，不进入无限重试。

## 3. 同步状态机

```text
pending
  -> running_metadata
  -> metadata_synced
  -> fulltext_indexing
  -> indexed
```

分支：

```text
metadata_synced -> metadata_only
fulltext_indexing -> failed -> pending_retry
metadata_only -> indexed（后续补齐摘要/全文）
```

`zotero_sync_runs.status` 与文档状态分开记录：

- Sync run：`pending | running | partial | complete | failed | canceled`。
- Document：`pending | indexed | metadata_only | failed | skipped`。
- Evidence：`personal_fulltext | structured_abstract | NULL`。

不能把“Web API 没有返回 fulltext”标记为同步失败。只有请求失败、响应结构异常或超时才进入 `failed`。

## 4. 全量与增量算法

### 4.1 全量同步

1. 解密凭据并调用 `Validate`。
2. 创建 `full` sync run，记录起始 library version。
3. 拉取 collection 分页并 upsert 稳定 key。
4. 拉取普通 item 分页，规范化标题、作者、年份、venue、DOI 和类型。
5. 从 item 的 attachment 关系定位 PDF/文本附件；只对 Web API 可读附件调用 fulltext。
6. 对每个 item 计算 metadata version 和 content hash。
7. 从当前 active epoch 或空快照创建 `building` epoch；不从查询可见数据原地修改。
8. 发布完整 `document_version`，在 building epoch 中写入成员关系，并创建带 `index_epoch_id` 的 chunk/embedding。
9. 校验 epoch 文档成员、chunk 完整性、实际 embedding 维度和 content hash。
10. 原子切换 epoch 为 `active`，再更新 coverage counters、library version 和 sync run 状态。

### 4.2 增量同步

1. 读取当前 library version。
2. 如果版本未变化且无未完成 attachment，直接标记 `complete`。
3. 使用 `since` 获取变化 item、collection 和 deleted key。
4. 先处理创建/更新，再处理删除和 collection 关系。
5. 对于内容 hash 未变化的文档，不重新分块或 embedding。
6. 以 active epoch 为基线创建新的 `building` epoch，并只替换发生变化的文档版本。
7. 所有必要步骤成功并校验完成后原子发布新 epoch；任何失败都保留旧 active epoch。
8. 写入新的 library version，只有新 epoch 发布且事务提交后才推进游标。

即使重复运行同一段版本，`(connection_id, zotero_item_key)`、`(document_id, version_no)` 和 `(index_epoch_id, document_version_id, ordinal)` 也必须保证幂等。

## 5. 文档归一化

标准化字段：

- `title`：去除控制字符和多余空白，保留原始标题用于展示。
- `authors_json`：姓名、顺序、ORCID（存在时）。
- `doi_normalized`：小写，去除 `doi:` 和 URL 前缀。
- `year`、`venue`：保留来源字段和标准化字段。
- `identifiers_json`：Zotero、DOI、S2、OpenAlex、PMID、URL。
- `item_type`：保留 Zotero 类型，不把 attachment 当论文实体。

同一论文在多个 collection 中只创建一个 document；不同 collection 通过 `document_collections` 建关系。若同一 item 更新后 DOI 或稳定标识变化，优先创建新版本并保留旧引用关系。

## 6. Fulltext 降级

```text
Web API fulltext available
  -> document_version.source_kind = fulltext
  -> evidence_type = personal_fulltext

Web API fulltext unavailable + abstract non-empty
  -> document_version.source_kind = abstract
  -> evidence_type = structured_abstract

No fulltext and no abstract
  -> index_status = metadata_only
  -> evidence_type = NULL
```

规则：

- `structured_abstract` 只能使用 Zotero 元数据、S2 或 OpenAlex 实际返回的摘要，不能由标题或模型推断。
- 全文缺失不阻断元数据同步，不影响 collection 关系和覆盖统计。
- 覆盖状态必须显示 fulltext、摘要、仅元数据和失败四个 countable bucket。
- 本地-only 内容无法通过 Web API 枚举，只显示覆盖警告，不伪造精确数量。

## 7. Chunk、Embedding 与 Index Epoch

1. 只对已发布 `document_version` 分块。
2. 目标 800 token，重叠 120 token，最大 1500 token。
3. 优先按章节、段落、列表和公式/代码边界切分。
4. 每个 chunk 持久化 `ordinal`、`section`、偏移、`token_count` 和 locator。
5. 以 batch size 32 调用 `EmbeddingProvider`；失败重试 3 次后标记该版本失败。
6. `embedding_version = provider + base_url + model + dimension + normalization_version + query_instruction_hash + passage_instruction_hash + truncation_policy_version` 的稳定 hash。
7. `EmbeddingProvider` 必须报告实际输出维度与能力；实际维度不等于 `EMBEDDING_DIM` 时启动或建索引直接 fail fast，不能截断、补零或静默改列。
8. 查询侧和文档侧必须使用各自 instruction；instruction、归一化、截断策略或模型变化都视为新 embedding 协议，不能与旧向量混检。
9. 所有文档成员、chunk 和向量完整写入并校验后，才允许将 `building` epoch 原子切换为 `active`。`active_version_id` 只是文档最新版本指针，不能替代 epoch 级查询快照。
10. 旧 active epoch 在新 epoch 发布后转为 `retired`，但保留到生命周期清理，保证旧 Brief 的 Evidence 仍可追溯。
11. 模型、维度、instruction 或 chunk 协议变化时创建新 epoch；同一文档版本可在新 epoch 生成新 chunk 行，不得原地覆盖旧 epoch 的向量。

## 8. 失败、重试和恢复

| 失败 | 处理 |
|---|---|
| 401/403 凭据失效 | 连接 `revoked/degraded`，停止同步，要求重新授权 |
| 404 item/attachment | 标记 skipped，不影响其他条目 |
| 429 | 按照 `Retry-After` 或指数退避重试，保留 cursor |
| 5xx/网络超时 | 有限重试，写 `partial` 和失败清单 |
| fulltext 空 | 正常降级，不标记 failed |
| embedding 部分失败或维度不符 | 不发布新 epoch，保留旧 active epoch，失败项进入重试 |
| epoch 发布中断 | 新 epoch 保持 `building` 或标记失败，active epoch 不变，重启后校验或重建 |
| 同步/重建过程中查询 | 查询固定发布时 active epoch，不混读 building epoch 或新旧向量 |
| 进程重启 | River 恢复任务，从 `cursor_jsonb`、building epoch 和版本状态续跑 |

任务幂等键：

- 同步：`connection_id + from_version + mode`。
- 文档索引：`document_version_id + content_hash`。
- 清理：`tombstone_id`。
- 覆盖统计更新：`sync_run_id`。

## 9. Tombstone 与清理

删除事件立即：

1. 写 `documents.removed_at` 和 `lifecycle_tombstones`。
2. 将 document 从 FTS/vector 查询过滤条件中排除。
3. 保留已生成的 brief 引用，但后续查询不可用。
4. 在 30 天后删除 fulltext、chunk、embedding、scope 关系和缓存。

撤权/断开连接时对连接下所有 active document 执行同样的移出检索逻辑，但保留连接审计记录。主动清空索引跳过回收期，执行立即清理。

## 10. 合同测试 fixture

必须覆盖以下脱敏响应：

- collection 分页和嵌套关系。
- item 分页、版本头、`since` 变化和 deleted key。
- attachment 与 parent item 关系。
- fulltext 可用、fulltext 为空、只返回 abstract、只有 metadata。
- 429 + `Retry-After`、超时、5xx、401。
- 同一 item 重复同步、内容 hash 变化、collection 移动。
- embedding 实际维度与 `EMBEDDING_DIM` 不符时拒绝发布。
- query/passage instruction、归一化或截断策略变化时生成新 `embedding_version`。
- epoch 发布中断、进程重启和重复发布保持幂等，active epoch 不出现部分内容。
- 同步构建新 epoch 期间，旧 epoch 查询结果保持一致且不混读新向量。
- 本地-only 内容只能进入警告状态，不能通过 fixture 伪造为 Web API 条目。

禁止将真实用户 PDF、私人 fulltext、真实 API Key 或未经脱敏的 Zotero 响应提交到仓库。
