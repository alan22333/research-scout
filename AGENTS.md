# Research Scout Agent 约束

本文件适用于整个仓库。任何 Agent 在修改文件前，必须先阅读：

- [产品需求文档 v1.4](docs/prd/2026-10-16-zotero-research-assistant-prd.md)
- [架构基线 v1.1](docs/architecture/2026-10-16-tech-stack.md)
- [技术设计包 v1.1](docs/design/README.md)
- [OpenAPI 3.1 契约](openapi/research-scout.yaml)
- [SDD + TDD 开发规范 v1.0](docs/development/2026-10-16-sdd-tdd-development-standard.md)

开发规范是强制门禁，不是建议。若本文件与开发规范冲突，以开发规范正文为准，并立即修正本文件。

## 修改代码前的硬门禁

1. 先定位本次工作的 Change Spec。没有规格和可测试验收条件时，先在 `docs/changes/` 创建规格并完成 Ready 门禁；不得直接开始业务实现。
2. 一个 Change Spec 只处理一个可独立验收的垂直切片。若范围扩大，先更新规格并重新进入 Ready。
3. 先写能够失败的测试并实际运行到 Red，再写最小实现到 Green，最后在测试保持 Green 的前提下重构。
4. 不得删除、弱化、跳过测试，或修改 fixture 来掩盖失败。
5. 产品行为变化先更新 PRD；架构边界变化先新增 ADR；API 变化先更新设计和 OpenAPI；数据库变化先写 migration 和迁移测试。
6. 如果实现中发现规格不完整，暂停扩展范围，更新规格并重新执行 Ready 门禁。
7. 文档-only 任务可以不写应用测试，但必须检查 Markdown 链接、YAML/JSON 解析和 `git diff --check`。

## 项目硬边界

- v1 只使用 Zotero Web API 和个人 My Library；不开发 Connector、不使用 Local API、不支持 group libraries。
- 不保存、不解析、不上传 PDF；不索引 Zotero 批注和笔记。
- Web API 无 fulltext 时按 `structured_abstract` 或 `metadata_only` 降级；`metadata_only` 永远不能成为科学结论证据。
- 不接 Lab Nexus、不实现 OIDC；身份只能通过 `IdentityAdapter` 边界接入。
- 联网搜索优先使用 Qwen/DashScope 原生能力；不默认接 Tavily、Brave、自建爬虫或第三方搜索 API。
- 所有业务查询先应用 `workspace_id`、生命周期、scope 和当前 `index_epoch` 过滤；禁止跨 workspace 或 epoch 泄漏。
- RAG 检索、Agent、Embedding 或 Prompt 变更必须绑定版本，并通过 `make eval-rag` 和质量 Gate；不得只用个案例判断效果。
- 不在仓库、日志、测试、快照或 Agent 输出中写入真实 Key、私人全文、真实 PDF、未脱敏 Provider 响应或生产数据。
- Agent 不得主动执行 `git commit`、`git push`、创建 PR、发布或改写历史；只有用户明确要求时才能执行。

## 验证与交付

- 阶段 0 必须建立 `make verify`、`make test-unit`、`make test-integration`、`make test-contract`、`make openapi-lint` 和 `make smoke-stage0` 等稳定入口。
- 默认 CI 与单元测试不得访问真实 Provider；使用 fixture、stub 或 contract fixture。
- 完成后必须报告：Spec ID、AC 映射、Red/Green 证据、完整验证命令、失败或未运行项、风险和已知限制。
- 未经运行的命令不得声称通过。无法运行时必须明确说明未验证范围。
- 不得自行修改 PRD、架构基线、设计包或历史评测结果中的产品结论；变化必须按事实源优先级先评审再修改。

完整流程、测试分层、DoD、模板和自检清单见 [SDD + TDD 开发规范](docs/development/2026-10-16-sdd-tdd-development-standard.md)。
