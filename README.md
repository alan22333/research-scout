# Research Scout

面向信息安全课题组的个人文献研究助理。

Research Scout 以用户授权的 Zotero collection 为首要全文语料，结合 Semantic Scholar、
OpenAlex 和通用联网搜索，在显式研究范围内生成带证据分层、引用和缺口清单的研究简报。

- [产品需求文档](docs/prd/2026-10-16-zotero-research-assistant-prd.md)
- [技术方案草案](docs/architecture/2026-10-16-tech-stack.md)
- 当前阶段：技术方案设计（技术栈方向待评审）

## 推荐技术方向

- Go 模块化单体 + PostgreSQL/pgvector + React/Vite + 原生 SwiftUI macOS Connector。
- OpenAPI 3.1 契约先行，REST + SSE，不引入微服务、独立向量数据库或 Kubernetes。
- 原始 PDF 只留在用户本机；服务端只保存 Zotero Local API 提供的派生全文、分块、向量和元数据。
- Lab Nexus 作为身份提供方，但需要先补齐 OIDC 接入；不共享密码、JWT secret 或数据库。

## 核心边界

- 只读 Zotero，不回写、不修改 collection。
- 原始 PDF 保留在用户本机，服务端只保存派生文本、分块、向量和元数据。
- v1 仅支持 macOS Connector。
- v1 不包含站内分享、公共库、批注/笔记索引和手动 PDF 上传。
