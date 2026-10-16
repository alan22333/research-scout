# RS-SPEC-0002：阶段 1 Zotero 同步与索引闭环

| 项目 | 内容 |
|---|---|
| 状态 | Ready |
| 日期 | 2026-10-17 |
| 负责人 | Codex |
| 上游依据 | PRD v1.4；架构基线 v1.1；技术设计包 v1.1；OpenAPI 1.1.0 |
| 前置规格 | RS-SPEC-0001（Verified） |

## 1. 问题与目标

阶段 0 只用脱敏 YAML fixture 证明了索引和查询闭环。阶段 1 必须把数据来源替换为真实的只读 Zotero Web API，同时保持阶段 0 的原子 `index_epoch`、版本化文档和 claim 可追溯语义。

本切片交付一个可在本地运行、可重复、可断点续跑的同步与索引闭环。真实 API Key 只允许通过环境变量或权限受限的本机配置文件注入。CLI 输出不得回显 Key、私人全文或未脱敏响应。

## 2. 范围

### 做

- 实现只读 `WebAPISource`：collection、item、top-level、since、分页、attachment、fulltext、429/Retry-After。
- 实现服务端凭据 AES-256-GCM 加密存取；明文仅存在于调用栈和进程内存中。
- 实现 full/incremental 同步：从 Zotero library version 判断是否需要同步，使用 `since` 获取变化，幂等写入文档和版本。
- 实现文档状态：`indexed/personal_fulltext`、`indexed/structured_abstract`、`metadata_only`、`failed/skipped`。
- 实现 building epoch 的完整构建、校验和原子发布；失败保留旧 active epoch。
- 实现 collection 关系、覆盖统计、删除 tombstone、30 天清理截止时间和手动清理入口。
- 增加 worker 命令 `sync-zotero`、`sync-status`、`purge-lifecycle` 和本地集成 smoke。
- 使用脱敏 HTTP fixture 和 fake source 验证，不访问真实 Provider。

### 不做

- 不实现 OAuth、Connector、Local API、group library、PDF 保存/解析、批注/笔记、Zotero 写回。
- 不实现外部发现、联网搜索、前端或完整 REST 控制面；OpenAPI 中已冻结的连接路由在阶段 2 接入。
- 不把真实 Key、私人 fulltext、真实响应写入仓库、日志、测试或 fixture。
- 不把 `metadata_only` 作为科学结论 evidence。

## 3. 验收条件与测试映射

| AC | 可观察行为 | 测试或证据 |
|---|---|---|
| AC1 | Web API collection/item 分页使用 start/cursor，不截断；解析标题、年份、venue、DOI、authors、collections | `TestWebAPISource_PaginatesCollectionsAndItems` |
| AC2 | since 响应返回变化项、删除项和最后版本；重复版本同步不新增 epoch | `TestWebAPISource_ChangesSinceReturnsDeletedKeys`、`TestSyncFullIsIdempotent` |
| AC3 | attachment 只通过附件 key 读取 fulltext；404/空文本降级为 abstract/metadata | `TestWebAPISource_FulltextUnavailableFallsBack`、`TestSyncDocumentStateFallsBack` |
| AC4 | 429 按 Retry-After 重试，超过上限返回 `rate_limited`，不无限重试 | `TestWebAPISource_Retries429WithRetryAfter` |
| AC5 | Key 加密后数据库中不含明文，正确密钥可读取，错误密钥失败 | `TestCredentialCipherRoundTripAndRejectsWrongKey` |
| AC6 | full sync 发布 active epoch；重复 full sync 不增加 document/version/chunk/epoch | `TestSyncFullPublishesIndexAndIsIdempotent` |
| AC7 | incremental 只请求 since；更新内容产生新 document_version，删除项立即退出检索并写 tombstone | `TestSyncIncrementalUpdatesAndTombstones` |
| AC8 | building epoch 失败不改变旧 active epoch；查询仍固定旧 epoch | `TestSyncFailurePreservesActiveEpoch` |
| AC9 | coverage 包含 fulltext/abstract/metadata_only/failed 数量；连接游标只在成功后推进 | `TestSyncCoverageAndCursorAdvanceAfterPublish` |
| AC10 | tombstone `purge_after` 为 30 天；到期清理删除派生 chunks/versions 并标记 purged | `TestPurgeDueLifecycle` |

## 4. 外部接口与类型

阶段 1 固定以下内部边界，不改变 OpenAPI：

- `ZoteroSource`：`Validate`、`ListCollections`、`GetLibraryVersion`、`ChangesSince`、`GetItem`、`ListAttachments`、`GetFulltext`。
- `CredentialStore`：`Put`、`Get`、`Delete`；实现使用 `SECRET_KEY` 派生的 AES-256-GCM。
- `Syncer`：`Sync(ctx, workspaceID, actorID, Credential, mode)`，生成 `SyncResult`。
- `LifecyclePurger`：按 `purge_after` 清理 tombstone 对应的派生数据。

HTTP source 的请求头、分页、错误分类和凭据处理遵循 `docs/design/2026-10-16-zotero-sync-indexing.md`。源错误必须转为稳定错误类型，不向上泄漏原始响应正文。

## 5. 数据库和迁移

阶段 1 不改变产品领域对象，只新增阶段 1 索引和约束：

- `documents(workspace_id, removed_at, purge_after)`。
- `collections(connection_id, is_deleted)`。
- `zotero_connections` 的凭据字段已有；不新增明文列。
- `lifecycle_tombstones` 的 `object_type=document` 已有。

迁移文件：`migrations/0002_stage1_sync_indexes.sql`。迁移必须幂等，不删除或重写阶段 0 数据。

## 6. 同步状态机

```text
pending -> metadata_synced -> fulltext_indexing -> indexed
                         \-> metadata_only
fulltext_indexing -> failed -> pending_retry
```

- Sync run 使用已有 `zotero_sync_runs` 状态。
- 任一外部请求或持久化失败时，run 标记 `failed`/`partial`，旧 active epoch 不变。
- Web API 无 fulltext 不是失败；按 abstract 或 metadata_only 降级。
- `last_library_version` 只在 epoch 发布成功后更新。

## 7. 生命周期与清理

- 删除 item：立即写 `documents.removed_at` 和 `lifecycle_tombstones`，`purge_after = now + 30 days`。
- 到期清理：删除该文档的 `claim_evidence`/`evidence` 派生引用、`chunks`、`document_versions`、`document_collections`，保留 tombstone 和最小审计元数据。
- 清理幂等；重复执行不报错。
- 旧 Brief 的不可追溯状态由 `verification_status=partial` 或既有证据快照字段解释，不伪造成成功引用。

## 8. Red / Green 证据要求

每个 AC 先新增最小失败测试并运行。Spec 必须记录可复现的 Red 命令和真实失败摘要；不得伪造日志。实现完成后记录 Green 命令、数据库计数和真实 Key 本地手工验证的脱敏聚合结果。

## 9. 已知偏差与决策

- 阶段 0 使用 `pgx + 自建迁移器`，阶段 1 不切换 GORM/Goose；见 `ADR-016`。
- 阶段 1 采用 PostgreSQL 持久任务边界同步执行，River 的 worker 运行时接入留给后续切片；不得把未实现的任务运行时标记为已验证。
- 真实 Zotero 手工验证只使用本机权限受限凭据文件，不把凭据写入仓库。

## 10. Red / Green 证据

### Red

2026-10-17 在实现前实际运行：

```text
$ go test ./internal/zotero ./internal/credentials -count=1
internal/credentials/cipher_test.go:10:12: undefined: NewCipher
internal/zotero/source_test.go:56:9: undefined: NewWebAPISource
internal/zotero/source_test.go:57:10: undefined: Credential
FAIL research-scout/internal/zotero [build failed]
FAIL research-scout/internal/credentials [build failed]

$ RS_DATABASE_URL='postgres://research:research@localhost:55433/research_scout?sslmode=disable' go test ./internal/integration -run TestSync -count=1
internal/integration/stage1_integration_test.go:50:26: undefined: zotero.Collection
internal/integration/stage1_integration_test.go:100:68: undefined: zotero.Credential
FAIL research-scout/internal/integration [build failed]
```

Red 说明阶段 1 的 Web API source、凭据加密、同步器和生命周期接口均尚不存在。

### Green

待实现完成后填写真实命令、脱敏计数和真实 Key 本地验证结果。
