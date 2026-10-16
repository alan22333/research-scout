# Research Scout 技术设计包 v1.1

| 项目 | 内容 |
|---|---|
| 状态 | 设计冻结；可直接进入实现，RAG 变更必须通过质量评测 |
| 日期 | 2026-10-16 |
| 上游产品文档 | [PRD v1.4](../prd/2026-10-16-zotero-research-assistant-prd.md) |
| 上游架构基线 | [架构基线 v1.1](../architecture/2026-10-16-tech-stack.md) |
| API 契约骨架 | [`openapi/research-scout.yaml`](../../openapi/research-scout.yaml) |
| 开发实施规范 | [SDD + TDD v1.0](../development/2026-10-16-sdd-tdd-development-standard.md) |

本设计包把架构基线展开为可编码规格。产品结论以 PRD 为准，架构边界以架构基线为准；本目录负责模块接口、数据模型、API/SSE、同步索引、检索 Agent、Provider、运行和测试细节。

## 文档地图

1. [实现蓝图](./2026-10-16-implementation-blueprint.md)：仓库结构、依赖方向、配置、错误模型、实施顺序和阶段 0 smoke test。
2. [数据模型与生命周期](./2026-10-16-data-model.md)：数据库表、约束、索引、版本、tombstone 和备份边界。
3. [API 与事件契约](./2026-10-16-api-and-events.md)：REST 资源、幂等、错误格式、SSE 重连和简报结构。
4. [Zotero 同步与索引](./2026-10-16-zotero-sync-indexing.md)：Web API 接入、增量游标、全文降级、chunk 和重试。
5. [检索、Agent 与简报](./2026-10-16-retrieval-agent-brief.md)：三种模式、混合检索、证据充分性、缺口规则和状态机。
6. [Provider、运行与测试](./2026-10-16-providers-runtime-testing.md)：模型与搜索 Provider、配置、可观测性、测试矩阵和验收场景。
7. [RAG 质量与评测](./2026-10-16-rag-quality-evaluation.md)：查询意图、golden set、指标、hard gate、消融实验和版本追踪。

## 设计约束

- v1 只使用 Zotero Web API 和个人 My Library，不包含 Connector、Local API、group library、PDF 上传或 PDF 解析。
- v1 不接 Lab Nexus、不实现 OIDC；本地开发使用固定工作区身份适配器，保留未来替换边界。
- 联网搜索优先复用模型供应商原生能力，本设计默认 Qwen/DashScope，不默认接 Tavily、Brave 或自建爬虫。
- `metadata_only` 是索引状态，不是证据类型；只有 `personal_fulltext`、`structured_abstract`、`web_context` 可以进入证据包。
- 检索、Agent、Provider 或 Prompt 变更必须绑定 `index_epoch`、Embedding 版本和检索配置版本，并通过 RAG 质量 Gate。
- 本设计包不创建应用代码、数据库迁移、部署配置或真实密钥。编码从实现蓝图中的阶段 0 开始。
