# Research Scout API 与事件契约 v1.1

| 项目 | 内容 |
|---|---|
| 状态 | 设计冻结；OpenAPI 骨架 v1.1 已建立，RAG 质量 Gate 约束变更 |
| 协议 | REST + JSON + SSE |
| 版本 | `/api/v1` |
| 时间 | RFC3339 UTC |
| 分页 | cursor-based；响应包含 `next_cursor` |

## 1. 通用约定

- 除 health 和静态资源外，所有接口必须经过身份适配器。
- `workspace_id` 和 `actor_id` 从服务端身份上下文注入，不从请求体或普通查询参数接受。
- 资源创建接口支持 `Idempotency-Key`；同一 key、同一 actor、同一 scope 的重复请求返回第一次结果。
- 列表接口默认按 `created_at desc`，必须限制 `limit` 最大 100。
- 资源表示使用资源对象本身，不用全局 `data` 包装；错误使用 [统一错误模型](./2026-10-16-implementation-blueprint.md#5-统一错误模型)。
- 请求和响应不得包含 `encrypted_credential`、API Key、OAuth token、完整私有全文或隐藏推理链。
- Zotero fulltext、外部摘要和网页内容都是不可信数据；只能作为 evidence，不能改变系统指令、工具权限、输出 schema 或租户 scope。
- 删除接口返回 `202 Accepted` 时，响应必须包含生命周期状态和 `purge_after`。

## 2. 身份与健康

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/me` | 当前 actor、workspace、角色和适配器类型 |
| `GET` | `/healthz` | 进程存活 |
| `GET` | `/readyz` | 数据库、迁移和必要 Provider 状态 |
| `GET` | `/metrics` | Prometheus 指标；不暴露私人内容 |

`GET /api/v1/me` 返回：

```json
{
  "actor": {"id": "uuid", "display_name": "Local User"},
  "workspace": {"id": "uuid", "name": "Local Workspace"},
  "role": "user",
  "identity_provider": "local"
}
```

## 3. Zotero 连接与同步

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/zotero/connections` | 当前 workspace 的连接 |
| `POST` | `/api/v1/zotero/connections` | 提交只读 API Key，创建连接 |
| `GET` | `/api/v1/zotero/connections/{connectionId}` | 查看脱敏连接状态 |
| `POST` | `/api/v1/zotero/connections/{connectionId}/validate` | 验证凭据和访问范围 |
| `POST` | `/api/v1/zotero/connections/{connectionId}/sync` | 触发全量或增量同步 |
| `POST` | `/api/v1/zotero/connections/{connectionId}/revoke` | 撤权并启动生命周期流程 |
| `GET` | `/api/v1/zotero/connections/{connectionId}/coverage` | 覆盖数量和缺口摘要 |
| `GET` | `/api/v1/zotero/collections` | 当前连接的 collection 树 |

创建连接请求只接受连接凭据、library type 和可选显示名；服务端调用 Web API 验证，不把 Key 回显。

```json
{
  "library_type": "user",
  "library_id": "123456",
  "api_key": "write-only-field",
  "display_name": "My Library"
}
```

覆盖状态固定返回：

```json
{
  "visible_items": 1200,
  "indexed_documents": 1170,
  "personal_fulltext": 510,
  "structured_abstract": 530,
  "metadata_only": 130,
  "failed": 3,
  "last_success_at": "2026-10-16T04:00:00Z",
  "local_only_warning": true
}
```

`local_only_warning` 只表示 Web API 无法证明本地-only 内容是否缺失，不返回虚构的本地数量。

同步请求：

```json
{
  "mode": "incremental",
  "force_full": false
}
```

若已有 `running` 运行，返回 `409 conflict` 和当前 `sync_run_id`，不重复启动。

## 4. 项目与 Scope

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/projects` | 当前 workspace 项目列表 |
| `POST` | `/api/v1/projects` | 创建项目 |
| `GET` | `/api/v1/projects/{projectId}` | 项目详情 |
| `PATCH` | `/api/v1/projects/{projectId}` | 修改名称、描述和默认模式 |
| `DELETE` | `/api/v1/projects/{projectId}` | 软删除并启动 30 天清理 |
| `GET` | `/api/v1/projects/{projectId}/scope` | 当前 scope 快照 |
| `PUT` | `/api/v1/projects/{projectId}/scope` | 保存常用 scope |

Scope 请求：

```json
{
  "scope_mode": "selected_collections",
  "collection_ids": ["uuid-a", "uuid-b"],
  "version": 3
}
```

规则：

- `all_library` 不接受 `collection_ids`。
- `selected_collections` 必须提供至少一个当前连接下可访问的 collection。
- 保存时创建不可变 `scope_version`；历史会话继续引用旧快照。
- 查询时可以临时覆盖 scope，但必须写入该 turn 的 snapshot，不能静默扩大项目默认范围。

## 5. 查询会话、追问与 SSE

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/projects/{projectId}/sessions` | 项目会话列表 |
| `POST` | `/api/v1/projects/{projectId}/sessions` | 创建首次查询 |
| `GET` | `/api/v1/sessions/{sessionId}` | 会话和当前阶段 |
| `GET` | `/api/v1/sessions/{sessionId}/turns` | turn 列表 |
| `POST` | `/api/v1/sessions/{sessionId}/turns` | 发起追问 |
| `GET` | `/api/v1/sessions/{sessionId}/events` | SSE 事件流 |
| `GET` | `/api/v1/sessions/{sessionId}/brief` | 当前完整简报 |
| `POST` | `/api/v1/sessions/{sessionId}/cancel` | 取消未完成任务 |
| `DELETE` | `/api/v1/sessions/{sessionId}` | 删除会话并启动清理 |

创建查询：

```json
{
  "question": "这些论文中，针对 side-channel mitigation 的方法分别有哪些？",
  "mode": "my_library",
  "scope": {
    "scope_mode": "selected_collections",
    "collection_ids": ["uuid-a"]
  }
}
```

响应返回 `session_id`、`turn_id`、`status=accepted`、`phase=accepted` 和 `events_url`。长任务不阻塞 HTTP 请求。

### SSE 事件

每个事件格式：

```text
id: 42
event: phase_changed
data: {"id":"evt_42","seq":42,"type":"phase_changed","at":"2026-10-16T04:00:00Z","data":{"phase":"retrieving_library"}}
```

固定事件类型：

| type | data 核心字段 |
|---|---|
| `session_accepted` | `session_id`, `turn_id` |
| `plan_updated` | `mode`, `scope`, `intent`, `route`, `filters`, `planned_tools` |
| `phase_changed` | `phase`, `message` |
| `tool_started` | `tool`, `source`, `request_id` |
| `tool_completed` | `tool`, `source`, `status`, `count` |
| `source_status` | `source`, `status`, `degraded_reason` |
| `evidence_ready` | `index_epoch_id`, `count_by_type`, `citation_count`, `claim_count`, `injection_flag_count` |
| `brief_ready` | `brief_id`, `schema_version`, `index_epoch_id`, `retrieval_config_version`, `prompt_version` |
| `error` | `error_code`, `retryable`, `message` |
| `heartbeat` | `at` |

客户端重连时发送 `Last-Event-ID`。服务端从 `query_events.seq > last_seq` 重放；无法重放的历史事件返回 `410 Gone`，客户端重新获取 session 当前状态。

SSE 只显示阶段、工具、来源、降级和结果，不返回模型的隐藏推理、系统提示词或完整内部 SQL。

- `evidence_ready.injection_flag_count` 只报告被标记的可疑内容数量，不回显攻击载荷。
- 检测到检索内容要求忽略指令、调用工具或泄露系统信息时，保留来源和降级状态，但不允许其进入 scientific claims 或改变执行路径。
- `brief_ready` 必须返回创建时的 `index_epoch_id`、`retrieval_config_version` 和 `prompt_version`；后续查询不会改写旧事件。

- `source_status` 必须覆盖 Zotero、rerank、结构化发现和联网搜索的实际使用与降级状态；rerank 缺失或失败时返回 `rerank` 来源，状态为 `degraded`，不得静默省略。

## 6. 简报、证据与缺口

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/briefs/{briefId}` | 结构化简报 |
| `GET` | `/api/v1/briefs/{briefId}/evidence` | 证据列表 |
| `GET` | `/api/v1/briefs/{briefId}/gaps` | 缺口列表 |
| `GET` | `/api/v1/evidence/{evidenceId}` | 单条证据与来源追溯 |
| `POST` | `/api/v1/briefs/{briefId}/feedback` | 反馈 |

简报结构固定为：

```json
{
  "id": "uuid",
  "session_id": "uuid",
  "turn_id": "uuid",
  "schema_version": "1.1",
  "index_epoch_id": "uuid",
  "retrieval_config_version": "retrieval-v1",
  "prompt_version": "brief-v1",
  "summary": "结论摘要",
  "summary_claims": [
    {
      "claim_id": "uuid",
      "claim_type": "summary",
      "text": "可核验的总结性结论",
      "support_level": "sufficient",
      "confidence": 0.86,
      "verification_status": "verified",
      "claim_evidence": [
        {
          "evidence_id": "uuid",
          "stance": "supports",
          "support_score": 0.91,
          "evidence_span": "原文中的支持片段",
          "locator": {"section": "Results", "page": 7}
        }
      ]
    }
  ],
  "findings": [
    {
      "claim_id": "uuid",
      "claim_type": "finding",
      "claim": "可核验结论",
      "support_level": "sufficient",
      "confidence": 0.84,
      "verification_status": "verified",
      "evidence_ids": ["uuid"],
      "claim_evidence": [
        {
          "evidence_id": "uuid",
          "stance": "supports",
          "support_score": 0.91,
          "evidence_span": "原文中的支持片段",
          "locator": {"section": "Results", "page": 7}
        }
      ]
    }
  ],
  "method_timeline": [],
  "chronology": [],
  "gaps": [
    {
      "type": "canonical",
      "missing_relative_to": "personal_library",
      "explanation": "外部发现的工作未出现在个人文库"
    }
  ],
  "open_questions": [],
  "citations": [
    {
      "id": "uuid",
      "brief_id": "uuid",
      "index_epoch_id": "uuid",
      "evidence_type": "personal_fulltext",
      "title": "来源标题",
      "quote": "引用片段",
      "score": 0.91,
      "retrieved_at": "2026-10-16T04:00:00Z"
    }
  ],
  "source_status": [
    {"source": "semantic_scholar", "status": "available", "degraded_reason": null}
  ]
}
```

`support_level = sufficient | partial | insufficient`，`claim_type = summary | finding | method | chronology`，`stance = supports | refutes | background`，`verification_status = pending | verified | partial | rejected`。

claim-level grounding 规则：

- 每个 finding 和 summary claim 必须拥有稳定 `claim_id`，并能通过 `claim_evidence` 追溯到 Evidence、文档版本和 locator。
- `evidence_ids` 是兼容和快速索引字段；事实来源以 `claim_evidence` 为准。每个 `supports` 关系必须有 `evidence_span` 或外部摘要定位。
- `verification_status = verified` 至少需要一条 `supports` evidence；只有 background 证据时不得标记 verified。
- 冲突来源必须创建 `refutes` 或独立反证 claim；证据无法判定时标记 `partial/rejected` 并进入 `open_questions`。
- `metadata_only` 永远不能成为 claim evidence。`web_context` 只能支持背景、来源线索或外部发现，不能单独支撑论文中的科学事实。
- 检索内容中的 prompt injection 不得进入 claim 文本、引用或 open question；只保留来源状态和安全审计。
- Brief 发布后不可变，固定其 `index_epoch_id`、检索配置和 Prompt 版本；旧 Brief 不回读新 epoch，不因后续同步而改写。

## 7. 外部发现与只读导出

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/discovery/search` | 查询外部结构化工作 |
| `GET` | `/api/v1/discovery/works/{externalWorkId}` | 外部工作元数据 |
| `GET` | `/api/v1/discovery/works/{externalWorkId}/citations` | 引用关系或替代来源 |
| `GET` | `/api/v1/briefs/{briefId}/export` | BibTeX/CSV/JSON 导出，默认只读复制 |

Discovery 响应必须包含 Provider、检索时间、DOI/稳定 ID、摘要来源和 `degraded_reason`。外部结果不自动写入 Zotero，也不改变个人 library 索引。

## 8. 生命周期与审计

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/lifecycle/clear-index` | 主动清空派生索引 |
| `GET` | `/api/v1/lifecycle/tombstones` | 查看待清理对象和截止时间 |
| `GET` | `/api/v1/audit-events` | 当前 workspace 的审计记录 |
| `POST` | `/api/v1/admin/audit-access` | 管理员重新认证并访问派生内容 |
| `DELETE` | `/api/v1/account` | 删除账户并启动清理 |

管理员访问接口必须要求新的认证证明；认证或审计写入失败时返回 `403 forbidden` 或 `503 source_unavailable`，且不能返回内容。

## 9. 幂等与并发

- `POST` 连接、项目、session、turn、sync、clear-index 支持 `Idempotency-Key`。
- 数据库在事务内写入资源、幂等记录和 River job；任务必须能在进程重启后恢复。
- 文档同步使用 Zotero key + library version；索引使用 document version + content hash；清理使用 tombstone ID 做幂等键。
- 重复提交不应产生重复事件、重复简报或重复向量。
- 资源被删除或已进入 `removed_pending_purge` 后，所有新查询必须拒绝或返回不可见。

## 10. 状态码与版本

- `200`：查询成功或幂等重放。
- `201`：同步资源创建成功。
- `202`：已受理异步任务。
- `204`：无响应体的删除确认。
- `400/401/403/404/409/410/422/429/500/503` 按统一错误模型返回。
- `/api/v1` 内部不允许破坏性变更；新增字段必须保持向后兼容。
- `schema_version` 写入简报和 SSE 事件，客户端按版本解析，未知字段必须忽略。
