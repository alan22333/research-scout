# ADR-015：优先复用模型供应商的原生联网搜索

| 项目 | 内容 |
|---|---|
| 状态 | 已采用 |
| 日期 | 2026-10-16 |
| 关联文档 | [架构基线 v1.1](../2026-10-16-tech-stack.md)、[Provider 设计](../../design/2026-10-16-providers-runtime-testing.md) |

## 背景

Research Scout 的 `landscape` 模式需要通用联网上下文，但学术结论主要由 Zotero fulltext、结构化摘要、Semantic Scholar 和 OpenAlex 支撑。此前预留了“通用搜索 Provider”，但没有决定是否自建搜索供应商适配。

主流模型产品的联网能力并不采用同一个搜索 API：

- OpenAI 在 Responses API 中提供 hosted `web_search` 工具，可控制搜索上下文规模、域名范围和返回 URL 引用。
- Gemini 通过 Google Search grounding 返回搜索 grounding metadata 与来源引用。
- Claude 提供服务端 web search tool，可限制使用次数和域名，并在响应中返回引用。
- DeepSeek 公开 API 文档当前明确支持的是函数调用工具，没有把服务端联网搜索作为 API 的稳定前提。
- Qwen/DashScope 在 OpenAI 兼容 Chat Completions 中支持 `enable_search`，在 Responses API 中提供 `web_search` 工具，并可返回 `search_info`、来源 URL 和时效参数。

因此，把 Tavily、Brave 或其他搜索 API 写成 v1 默认依赖，会比模型本身的能力更窄，也会增加一轮不必要的供应商选择。

## 决策

1. 定义统一的 `WebSearchProvider` 领域接口，但 v1 首个实现固定为 `QwenNativeWebSearch`。
2. Provider 通过能力探测暴露 `supports_native_search`；不支持时返回 `web_search_unavailable`，不得静默伪装成空结果。
3. `landscape` 先执行个人文库和结构化发现，再按需执行原生联网搜索；联网结果只能标记为 `web_context`。
4. 搜索响应必须归一出标题、URL、获取时间和短片段或可验证摘要；没有 URL 引用的结果不进入证据包。
5. 保留 `DisabledWebSearchProvider` 作为无 Key、测试和供应商不可用时的兜底实现；v1 不实现 Tavily、Brave 或自建爬虫连接器。
6. OpenAI、Gemini、Claude 未来可通过同一接口增加适配器，但不改变 Agent、证据类型和简报结构。

## 结果

- 首版直接复用 Qwen/DashScope 的联网能力，减少搜索供应商接入、配额和网页清洗工作。
- 搜索质量仍随模型供应商变化；系统必须在来源状态中明确显示实际使用的提供方和降级状态。
- 网页不能单独支撑科学事实。网页发现 DOI 或论文标识后，应再次查询 Semantic Scholar/OpenAlex 获取结构化元数据。
- 如果后续需要更稳定的域过滤、页面抓取或独立搜索配额，再新增搜索 Provider；这不需要修改核心检索和证据模型。

## 官方参考

- OpenAI：`https://platform.openai.com/docs/guides/tools-web-search`
- Google Gemini：`https://ai.google.dev/gemini-api/docs/web-search`
- Anthropic Claude：`https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-search-tool`
- DeepSeek API：`https://api-docs.deepseek.com/api/create-chat-completion`
- Alibaba Model Studio：`https://help.aliyun.com/zh/model-studio/web-search`
