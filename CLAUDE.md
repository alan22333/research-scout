# Research Scout 开发指针

在开始实现前，先阅读：

- [产品需求文档](docs/prd/2026-10-16-zotero-research-assistant-prd.md)
- [架构基线 v1.1](docs/architecture/2026-10-16-tech-stack.md)
- [技术设计包](docs/design/README.md)
- [OpenAPI 3.1 契约骨架](openapi/research-scout.yaml)
- [SDD + TDD 开发规范](docs/development/2026-10-16-sdd-tdd-development-standard.md)
- [Agent 强制约束](AGENTS.md)

技术方案已冻结。[实现蓝图阶段 0](docs/design/2026-10-16-implementation-blueprint.md) 的最低闭环（PostgreSQL/pgvector、迁移骨架、本地 workspace、脱敏 Zotero fixture、建索引和一次可追溯的 `my_library` 查询）已实现并通过验证，见 [Change Spec RS-SPEC-0001](docs/changes/RS-SPEC-0001-stage0-smoke.md)（状态 Verified）。后续阶段（真实 Zotero Web API、Provider、Web UI、任务队列）尚未实现，开工前先读对应设计并遵守 Change Spec 门禁。

当前方向的硬边界：

- v1 使用 Zotero Web API，仅个人 My Library；不开发、不要求 Connector。
- 不保存、不解析、不上传 PDF；不索引 Zotero 批注和笔记。
- 全文不可用时：有摘要降级为 `structured_abstract`，仅元数据设为 `metadata_only`；两者都不能伪装成 `personal_fulltext`。
- v1 不接 Lab Nexus、不实现 OIDC；身份必须通过适配器边界接入。
- 联网搜索优先复用模型供应商原生能力，v1 默认 Qwen/DashScope；不默认接 Tavily、Brave 或自建爬虫。
- 架构变更必须先更新架构文档和 ADR，再进入模块实现。

## 开发强制门禁

- 修改代码前必须阅读 [Agent 约束](AGENTS.md) 和 [SDD + TDD 开发规范](docs/development/2026-10-16-sdd-tdd-development-standard.md)。
- 没有已批准的 Change Spec 和可测试验收条件，不得开始业务实现。
- 每个行为遵循 `Red -> Green -> Refactor`；必须先运行失败测试，再写最小实现，最后在测试保持绿色时重构。
- 不得删除、弱化、跳过测试或修改 fixture 来掩盖失败。
- 产品行为先改 PRD；架构边界先加 ADR；API 先改设计和 OpenAPI；数据库先写 migration 和迁移测试。
- 所有业务查询必须保留 workspace、生命周期、scope 和 `index_epoch` 过滤；`metadata_only` 不得进入科学结论证据。
- RAG 检索、Agent、Embedding 或 Prompt 变更必须绑定版本，并通过 `make eval-rag` 和质量 Gate。
- 不把真实 Key、私人全文、PDF、未脱敏响应或生产数据写入仓库、日志和测试。
- Agent 不得自行 commit、push、创建 PR 或发布；只有用户明确要求时才能执行。
