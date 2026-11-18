# her 自定义服务接入方案

| 项 | 内容 |
|---|---|
| 版本 | v0.2 |
| 日期 | 2026-11-10 |
| 状态 | 一期已实现，未合并，待评审；实现记录见 §17 |
| 关系 | 扩展 model-catalog-design.md（下称目录方案）的 §3.1 用户新增的服务商、§4.5 模型 ID 到目录行、§6.2 默认显示；standalone-cli-design.md §7.3 的 `her login --base-url`；desktop-kernel-integration.md（下称桌面集成方案）§8.4 的设置页。实现时要同步改的地方见 §14.3 |

## 方案摘要

- **要解决的问题**：用户自己的模型服务（公司网关、LiteLLM、New API 一类的中转站、vLLM / Ollama / LM Studio 这类本机推理服务、云上的兼容接口）现在只能手写 `models.toml` 或用 `her login NAME --base-url URL --api …` 接入：要自己选协议、自己写对路径前缀，桌面端没有入口。接上之后，网关上的模型名常常和官方写法不同（`us.anthropic.claude-opus-4-1-20250805-v1:0`、`claude-opus4-7`、`qwen3:32b`），现在的匹配规则只认出 41%，其余一律按默认参数用。
- **用户只填地址和密钥**（本机服务可以不填密钥）。内核自己探出这个地址讲哪几种协议、每种协议的路径前缀、密钥放在哪个请求头、有哪些模型（§4）。探测只发不计费的请求：列表请求和请求体为空的 POST，按状态码和错误体的形状判断，接口存在时回 400、401 或 422，不存在时回 404；再对同一前缀下一个不存在的路径发一次对照，对照不回 404 的服务（对什么都回 401 的网关）不靠状态码下结论。
- **协议**（§2，看了 30 来个网关和推理服务的源码或文档，并实测了十几家）：
  - Chat Completions 人人都讲。
  - Anthropic Messages 现在也几乎都讲：vLLM、SGLang、Ollama、LM Studio、llama.cpp、LiteLLM、New API、OpenRouter、Bedrock 等。不讲的只剩已停更的 One API 和 Groq。
  - OpenAI Responses 同样普遍，2026 年 1 月有了带合规测试的开放规范 Open Responses。
  - Gemini 原生格式只有少数网关讲，它们也都讲 Chat Completions。

  一期用 Chat Completions 和 Messages 两个已经验收过的适配器；探到 Responses 先记下，等通用的 Responses 适配器验证之后再用（与 standalone-cli-design D-545 同一个条件）。
- **每个模型走哪种协议**（§5）：默认 Chat Completions；Claude 模型在网关也讲 Messages 时走 Messages。Claude 的提示缓存要请求里写缓存断点，her 的 Messages 适配器会写，Chat Completions 适配器不写；缓存命中的输入按原价的十分之一计费，智能体每轮都重发整段对话，差别很大。公共目录里已有 17 家服务这样按模型分协议。
- **模型参数**（§6）：在"渠道行 → 厂商行"之后加一步"规范模型"。公共目录给 85% 的行标了 `canonical_model_id`，用它建一张"各家叫法 → 规范模型"的别名表，再加几条名字规整规则（去掉路由前缀、区域前缀、版本和量化后缀、Ollama 标签，数字间的 `p`、`.`、`-` 互换，去掉符号再比）。实测 34 个网关型渠道的 2,629 个对话模型，每次排除被测的那一家：认出的比例从 41% 升到 86%，新认出的模型 99% 对到了正确的规范模型。只有去掉日期或修饰后缀才对上的算弱匹配（再加 4 个百分点，模型对的 96%，但推理标记不可靠），只借上下文和图片。
- **防止串名**：没有规范模型的名字（`auto` 在三家聚合商里分别是 32K、128K、100 万上下文）仍只认模型厂商自己的行，与现在的规则相同；一个名字对到几个不同的规范模型时不猜。
- **认不出的模型照样能用**（§7）：列表里自带的参数优先（vLLM 的 `max_model_len`、Ollama、LM Studio、Vercel AI Gateway、OpenRouter 等都在列表里给上下文或能力），没有就用现在的默认值并标"参数未知"。自定义服务里已识别的模型不足 12 个时，用未识别的补进菜单，免得只跑自己微调模型的服务接上后菜单是空的。用户可以在"管理模型"里指定它"像哪个模型"（套用那个模型的参数），或者只填上下文窗口、是否收图片。
- **自部署的坑**：列表给的上下文小于目录时（自部署限了长度），不借目录的输出上限。vLLM 一类服务要求"提示 + 输出上限 ≤ 上下文"，借来 32K 的输出上限会让短上下文的部署每次都拒绝请求。
- **入口**：桌面"设置 › 模型 › API 与自定义服务 › 添加"的选择器里多一块"自定义服务"；命令行 `her login NAME --base-url URL` 不写 `--api` 时自动探测。两者调同一个内核函数，配置写进 `models.toml`，运行中的服务不用重启。
- **协议**：新增 `providers/add`、`providers/update`、`providers/remove`、`models/configure`、`models/catalog`；`ModelSummary` 加 `canonical`，`ProviderSummary` 加 `custom`。
- **不强制 https**（D-610）：公网地址用 http 照样接入，只提示"这个地址不加密"；密钥只发往用户填的主机，不跟随重定向。
- **分期**：一期是本文主体。从错误里学参数、通用 Responses、LiteLLM 的模型详情接口等列在"按需再加"，各写明触发条件（§14.2）。

## 目录

- 方案摘要
- 0. 阅读说明
- 1. 现状与目标
- 2. 调研：网关讲哪些协议
- 3. 接入流程
- 4. 探测
- 5. 每个模型走哪种协议
- 6. 模型参数
- 7. 认不出的模型
- 8. 配置与存储
- 9. 协议
- 10. 命令行
- 11. 桌面端
- 12. 安全
- 13. 测试与验收
- 14. 分期
- 15. 决策记录
- 16. 待定问题
- 17. 实现记录
- 附录 A. 识别率的测法

## 0. 阅读说明

- **编号**：本文的决策为 D-600–D-629（§15），待定问题为 Q-330–Q-339（§16）。
- **术语**：
  - **自定义服务**：用户填地址接入的模型服务，在 her 里就是 `models.toml` 里一个不是内置的服务商。设置页那一节叫"API 与自定义服务"。
  - **线协议**：请求和响应的格式。her 有三个适配器：`openai-chat`（OpenAI Chat Completions）、`anthropic-messages`（Anthropic Messages）、`openai-responses`（OpenAI Responses，目前只照 ChatGPT 订阅的线路写）。
  - **前缀**：适配器拼请求地址用的 `base_url`。`openai-chat` 请求 `{前缀}/chat/completions`，`anthropic-messages` 请求 `{前缀}/v1/messages`，所以同一个网关的两种协议前缀往往不同：`https://gw.example.com/v1` 和 `https://gw.example.com`。
  - **规范模型**：公共目录（models.dev）给每一行标的 `canonical_model_id`。例如 Bedrock 的 `us.anthropic.claude-opus-4-1-20250805-v1:0` 标为 `anthropic/claude-opus-4-1-20250805`，Fireworks 的 `accounts/fireworks/models/qwen3p8-max` 标为 `alibaba/qwen3.8-max`，Ollama Cloud 的 `gemma4:31b` 标为 `google/gemma-4-31b-it`。
  - **别名表**：从公共目录全部 235 家的行得出的"各家叫法 → 规范模型"。
  - **探测**：接入时内核发的几个不计费请求，用来弄清这个地址讲哪些协议。
  - 渠道行、厂商行、`known` 等沿用目录方案 §0。

## 1. 现状与目标

### 1.1 现状

| 项 | 现在 | 问题 |
|---|---|---|
| 入口 | 命令行 `her login NAME --base-url URL --api …`（standalone-cli-design §7.3），或手写 `models.toml`（目录方案 §3.1） | 桌面端没有入口："API 与自定义服务"一节只能加内置服务商 |
| 协议 | 用户自己选 `--api`，默认 `openai-chat` | 多数用户不知道网关讲哪种；网关两种都讲时只能用一种 |
| 前缀 | 用户填的就是前缀 | 少写或多写一个 `/v1` 就是 404；两种协议的前缀还不一样 |
| 鉴权 | Chat Completions 固定 `Authorization: Bearer`；Messages 默认 `x-api-key`，可改为 Bearer | 用别的请求头的服务（§4.5）接不上 |
| 模型参数 | 渠道行 → 厂商行；比较名字时不分大小写，去掉最后一个 `/` 前面的部分（目录方案 §4.5） | 网关上的叫法五花八门，实测只认出 41%（§6.6）。其余按默认值：128K 上下文、不收图、没有思考档位 |
| 认不出的模型 | 不进菜单（默认只显示参数已知的，目录方案 §6.2），可以手写 `models.toml` 补参数 | 只跑自己微调模型的 vLLM 接上后菜单是空的；桌面端补不了参数 |
| 列表里的参数 | 只读 OpenRouter 的字段和 Ollama 的 `/api/show` | 其他服务列表里给的上下文和能力都没读（§6.5） |

### 1.2 目标

| 编号 | 目标 | 含义 |
|---|---|---|
| G1 | 填地址就能用 | 地址加密钥（本机服务可以不要），几秒内接好；不用懂协议、路径和请求头 |
| G2 | 参数靠得住 | 认得出的尽量认出，认错的比例低到可以默认启用；认不出的照样能用，用户补得上 |
| G3 | 用得省 | Claude 模型保住提示缓存 |
| G4 | 不拖慢 | 启动和一次性命令不多花时间；探测只在接入和"重新检测"时发生 |
| G5 | 安全 | 密钥只发往用户填的主机；探测不产生费用 |

### 1.3 非目标

- 只认特定客户端特征的中转密钥（要模仿某个客户端的系统提示和请求头才能用）。
- Bedrock、Vertex 的原生签名（SigV4、OAuth）：走它们的 OpenAI 兼容接口或网关（standalone-cli-design §7.1）。Vertex 的兼容接口要每小时过期的令牌，可以用 `models.toml` 现有的 `api_key_command`（§4.6）。
- 几个地址之间的自动切换和负载均衡：这是网关自己的事。
- Gemini 原生协议（§2.3）。

## 2. 调研：网关讲哪些协议

2026-11-09 至 11-10 调研。开源服务看源码（各自默认分支），托管服务看官方文档，再用不带密钥的请求实测了十几家公开服务（§2.4）。

### 2.1 结论

- **Chat Completions 是共同底线**：所有网关、云上的兼容接口、本机推理服务都讲。
- **Anthropic Messages 也几乎都讲了**。只讲 Anthropic 协议的编码客户端很流行，网关为了接它们都加了这个接口：
  - 网关：LiteLLM、New API、OpenRouter、Vercel AI Gateway、Portkey、Bifrost、Envoy AI Gateway、Higress、APISIX、Cloudflare AI Gateway；
  - 云：Bedrock；Azure 只给 Claude；
  - 本机推理：vLLM、SGLang、Ollama（0.14 起）、LM Studio（0.4.1 起）、llama.cpp、LocalAI、Xinference；
  - 托管：Fireworks、DeepInfra、SiliconFlow。
  - 不讲的：One API（2025 年 2 月起停更）、Groq、TensorZero（已归档）。
- **OpenAI Responses 同样普遍**。2026-01-15 发布了开放规范 Open Responses：OpenAI 牵头，OpenRouter、Hugging Face、LM Studio、vLLM、Ollama、Vercel 首批实现，带合规测试，2026-10-21 起成为 Linux 基金会旗下的项目。它只定义 `POST /v1/responses` 和 `/v1/responses/compact`，不管模型列表；很多实现是无状态的（Ollama；Bedrock runtime 只能同步）。
- **Gemini 原生格式**只有 LiteLLM、New API、Bifrost 和配置了的 Kong 讲，它们都同时讲 Chat Completions。
- **模型列表**：几乎都有 OpenAI 形式的 `GET /v1/models`。例外：Bedrock runtime 没有；Azure 列的是模型，不是请求里要写的部署名；Kong、APISIX、Cloudflare 没有统一的列表。列表里的参数没有标准，各家字段见 §6.5。
- **前缀**：OpenAI 系的前缀带版本段，常见的是 `/v1`，也有 `/openai/v1`（Azure、Bedrock、Groq）、`/api/v1`（OpenRouter）、`/inference/v1`（Fireworks）、`/v1/openai`（DeepInfra）。Anthropic 系的前缀从不带 `/v1`，客户端自己加 `/v1/messages`；常见的是主机根，或者 `/anthropic`（DeepSeek、Kimi、DeepInfra、Bedrock、Azure、Bifrost、Envoy、Xinference）。
- **鉴权**：
  - OpenAI 系几乎都认 `Authorization: Bearer`。
  - Anthropic 系多数认 `x-api-key`，但 vLLM、SGLang、Ollama Cloud 只认 Bearer，Cloudflare 的 Anthropic 透传只认 `x-api-key`。
  - 有的服务先鉴权再路由：DeepSeek、vLLM、llama.cpp、Helicone、Portkey 没有密钥时对任何路径都回 401。
- **按模型分协议是现实**：
  - New API 的列表给每个模型写了 `supported_endpoint_types`（`openai`、`openai-response`、`anthropic`、`gemini`……），这是找到的唯一一种逐模型的协议提示。
  - 公共目录里有 17 家服务给同一服务里的模型配了不同协议（§5）。

### 2.2 一览

| 产品 | Chat Completions | Messages | Responses | 列表 | 用户填的地址 |
|---|---|---|---|---|---|
| LiteLLM | 有 | 有 | 有 | `/v1/models`；详情 `/model/info` | `http://host:4000`，带不带 `/v1` 都行 |
| New API | 有 | 有 | 有 | `/v1/models`，形状随请求头变 | `https://host/v1`；Anthropic 填 `https://host` |
| One API | 有 | 无 | 无 | `/v1/models` | `https://host/v1` |
| OpenRouter | 有 | 有，`/api/v1/messages` | 有 | `/api/v1/models`，公开 | `https://openrouter.ai/api/v1`；Anthropic 填 `https://openrouter.ai/api` |
| Vercel AI Gateway | 有 | 有 | 有 | `/v1/models`，公开，参数最全 | `https://ai-gateway.vercel.sh/v1` |
| Portkey | 有 | 有 | 有 | `/v1/models` | `https://api.portkey.ai/v1`；要 `x-portkey-*` 请求头 |
| Cloudflare AI Gateway | 有 | 有 | 有（REST） | 无统一的 | 带账户和网关 ID 的长地址 |
| Bifrost | 有 | 有，`/anthropic` 下 | 有 | `/v1/models` | `http://host:8080/v1` |
| Envoy AI Gateway | 有 | 有，`/anthropic` 下 | 有 | `/v1/models` | `http://gw/v1` |
| Kong、APISIX、Higress | 看配置 | 看配置 | 看配置 | 多数没有 | 运维定的路由 |
| Azure | 有 | 只给 Claude，在 `https://{资源}.services.ai.azure.com/anthropic` | 有 | 列模型，不列部署 | `https://{资源}.openai.azure.com/openai/v1` 或 `.services.ai.azure.com` |
| Bedrock | 有，`/openai/v1` | 有，`/anthropic` | 有 | runtime 没有；Mantle 有 | `https://bedrock-runtime.{区域}.amazonaws.com/openai/v1` |
| Vertex | 有 | 无（Claude 走它自己的接口） | 未写明 | 未写明 | 要每小时过期的 OAuth 令牌 |
| vLLM、SGLang | 有 | 有 | 有 | `/v1/models`，带 `max_model_len` | `http://host:8000/v1` |
| Ollama | 有 | 有（0.14 起） | 有，无状态 | `/v1/models`；详情 `/api/show` | `http://localhost:11434` |
| LM Studio | 有 | 有（0.4.1 起） | 有 | `/v1/models`；详情 `/api/v1/models` | `http://localhost:1234/v1` |
| llama.cpp | 有 | 有 | 有 | `/v1/models`；运行参数 `/props` | `http://host:8080/v1` |
| LocalAI、Xinference | 有 | 有 | 有 | `/v1/models` | `http://host:8080/v1` |
| Fireworks、DeepInfra、SiliconFlow、Together | 有 | 有 | 有（SiliconFlow 无） | `/v1/models`（Together 回一个裸数组） | 各家文档的地址 |
| Groq | 有 | 无 | 有（测试中） | `/openai/v1/models`，带 `context_window` | `https://api.groq.com/openai/v1` |

### 2.3 为什么一期只做两种

- **Chat Completions 和 Messages**：两个适配器都已经在真实服务上验收过（目录方案 §13.3），覆盖上表全部服务。
- **Responses 先不用**：`openai_responses.rs` 照 ChatGPT 订阅的线路写，工具包成 `namespace`，没在普通接口上验证过（standalone-cli-design D-545）。Open Responses 带合规测试，正好用来验证通用的变体；验证通过后在 §5 加一条规则即可（§14.2）。探测照样记下哪些服务讲 Responses，到时不用再探。
- **Gemini 原生不做**：讲它的网关都讲 Chat Completions。

### 2.4 实测

不带密钥，请求体 `{}`，记状态码。"随机路径"是同一前缀下一个不存在的路径，用来判断探测结果可不可信（§4.4）：

| 服务 | Chat Completions | Responses | Messages | 随机路径 |
|---|---|---|---|---|
| OpenAI | 401 | 401 | 404 | 404 |
| Anthropic | 401 | 404 | 401 | 404 |
| OpenRouter | 401 | 401 | 401 | 404 |
| Vercel AI Gateway | 400 | 400 | 400 | 404 |
| Fireworks | 400 | 400 | 400 | 404 |
| NVIDIA | 400 | 400 | 400 | 404 |
| Together | 401 | 400 | 400 | 404（网页） |
| Groq | 401 | 401 | 404 | 404 |
| SiliconFlow | 401 | 404 | 401 | 404 |
| DeepInfra（主机根） | 401 | 401 | 404 | 404 |
| Gemini 兼容接口 | 400 | 404 | 404 | 404 |
| Kimi | 401 | 401 | `/v1/messages` 404，`/anthropic/v1/messages` 401 | 404 |
| DeepSeek | 401 | 401 | 401 | 401（纯文本） |
| Helicone、Portkey | 401 | 401 | 401 | 401 |

- 随机路径回 404 的服务，接口在不在一清二楚：在的回 400 或 401，不在的回 404。
- Vercel AI Gateway、Fireworks、NVIDIA 先校验请求体再鉴权，所以没有密钥也回 400。
- DeepSeek、Helicone、Portkey 先鉴权，没有密钥时什么都看不出来；带上密钥后才能判断（§4.4）。
- Anthropic 也讲 Chat Completions（它的 OpenAI 兼容层），可见"接口存在"不等于"应该用它"：这正是 §4.6 认出内置服务商、§5 按模型选协议的原因。
- OpenRouter 的 Messages 在 `/api/v1/messages`，按 §4.2 的规则由 `https://openrouter.ai/api/v1` 去掉 `/v1` 得到前缀 `https://openrouter.ai/api`，正好对上。

## 3. 接入流程

### 3.1 桌面端

1. 设置 › 模型 › API 与自定义服务 › 添加：选择器最后一块是"自定义服务"，写"OpenAI 或 Anthropic 兼容的地址"。
2. 表单三项：
   - **服务地址**（必填），占位文字 `https://llm.example.com/v1`；
   - **API Key**（可选），说明"本机服务一般不需要"；
   - **名称**（可选），不填就用主机名。
3. 点"连接"：按钮变成"正在检测…"，内核探测（§4），通常一两秒，最多十几秒。
4. 成功：卡片里写结果，例如"找到 37 个模型，33 个已识别参数 · OpenAI Chat Completions、Anthropic Messages"，主按钮"完成"，次要按钮"管理模型"。服务出现在"API 与自定义服务"的列表里。
5. 失败：留在表单上，写明原因和下一步（§3.3），什么都不保存。
6. 首次启动引导页用同一个选择器，同样有"自定义服务"。

### 3.2 命令行

```
$ her login acme --base-url https://llm.acme.com
粘贴 acme 的密钥（不会显示）：
检测到 OpenAI Chat Completions（https://llm.acme.com/v1）、Anthropic Messages（https://llm.acme.com）
列出 37 个模型，33 个已识别参数；Claude 模型走 Anthropic Messages
已写入 ~/.her/models.toml，密钥已保存到钥匙串
默认模型：
  › acme/claude-sonnet-5-5
    acme/gpt-6.1-terra
    …
```

- `--api` 改为可选：写了就只用这种协议，跳过协议探测（列表仍然要查）。
- 非交互：`printf %s "$KEY" | her login acme --base-url URL --key-stdin`；本机服务用 `--no-key`。
- 命令行和桌面端调同一个内核函数（§9），不再各写一份。现在 `cmd_login.rs` 里写 `models.toml` 的代码移进 her-core。

### 3.3 失败时

| 情况 | 提示 | 下一步 |
|---|---|---|
| 连不上（DNS、拒绝连接、超时） | "连不上这个地址" | 检查地址、网络或服务是否在运行 |
| 证书错误 | "这个地址的证书无效" | 内网自签证书要先装进系统钥匙串 |
| 不像模型服务（所有路径都是 404 或网页） | "这个地址不像模型服务：没找到 OpenAI 或 Anthropic 兼容的接口" | 对照服务的文档检查地址 |
| 要密钥但没填 | "这个服务需要 API Key" | 填密钥 |
| 密钥被拒 | "密钥无效"，附服务的原话（截断到 500 字符） | 换密钥 |
| 公网地址用 http | 不算失败：结果里提示"这个地址不加密，密钥会明文传输" | 服务支持 https 就改用 https（§12） |
| 有接口但没有模型列表 | 表单多出一栏"模型名"，可填多个 | 填网关文档里的模型名；保存后照常可用 |
| 对任何路径都回同样的错误 | 不算失败：有列表就按 Chat Completions 接入，提示"没法确认它是否讲 Anthropic 协议"（§4.4） | 需要时在高级选项里指定协议 |
| 内置服务商的官方地址 | 不算失败，提示"这是 DeepSeek 的官方地址，用内置接入能得到它专门的思考参数和人民币价格"，给"改用 DeepSeek"按钮（§4.6） | 用户选择 |
| Azure、Bedrock、Vertex、Portkey | 按 §4.6 各自提示 | 补部署名、模型名，或改用 `models.toml` |

## 4. 探测

### 4.1 地址规整

1. 去掉首尾空白。没写 `http://` 或 `https://` 时：本机和内网地址（`localhost`、`127.0.0.0/8`、`::1`、`10/8`、`172.16/12`、`192.168/16`、`*.local`）补 `http://`，其余补 `https://`。
2. 去掉末尾的 `/`，再去掉粘贴进来的接口路径：`/chat/completions`、`/completions`、`/responses`、`/messages`、`/models`。例如 `https://gw.example.com/v1/chat/completions` 变成 `https://gw.example.com/v1`。
3. 带查询参数、`#` 片段或用户名密码（`https://user:pass@host`）的地址一期不接受，提示去掉。适配器按"前缀 + 路径"拼地址，带查询参数的前缀拼不出正确的请求（Q-333）。
4. 地址是 http、又不是本机或内网地址时，探测结果标 `insecure`，界面提示，不拦着（D-610）。内网地址指字面上落在第 1 条的范围里，或者主机名解析出的地址全部落在这些范围里（公司内网常用 `http://llm.corp.internal` 这类内部域名）。

### 4.2 候选前缀

记规整后的地址为 U，它的主机根（`协议://主机[:端口]`）为 H；U 以 `/v1` 结尾时，去掉它得到 R，否则 R = U。

| 协议 | 候选前缀（按顺序） | 例子 |
|---|---|---|
| 列表、Chat Completions、Responses | U 以 `/v<数字>` 结尾时只有 U；否则 `U/v1`、U。U 就是主机根时，再试常见前缀 `H/openai/v1`、`H/api/v1`、`H/inference/v1`、`H/v1/openai` | `https://gw.example.com` → `https://gw.example.com/v1`、`https://gw.example.com`…… |
| Messages（适配器再加 `/v1/messages`） | R、`R/anthropic`、`H/anthropic`，去重 | `https://openrouter.ai/api/v1` → `https://openrouter.ai/api`；`https://api.moonshot.cn/v1` → `https://api.moonshot.cn/anthropic`；Bedrock 的 `…/openai/v1` → `https://bedrock-runtime.{区域}.amazonaws.com/anthropic` |

- 每种协议各自取第一个应答为"有"的候选，所以同一个服务的两种协议可以有不同的前缀。
- 常见前缀来自 §2.1 的调研，排在最后，前面的候选都不在时才试，一般用不到。

### 4.3 请求

1. **列表**：对列表的候选依次 `GET {前缀}/models`，第一个回出模型列表的为准。都没有时，再按 Anthropic 的形式 `GET {R}/v1/models`（带 `anthropic-version`），给只讲 Messages 的网关。
2. **对照**：对每个要用到的前缀发一次 `POST {前缀}/her-probe-{8 位随机十六进制}`，请求体 `{}`。它一定不存在，用来判断下一步的结论可不可信（§4.4）。
3. **协议**：同时发
   - `POST {前缀}/chat/completions`，请求体 `{}`；
   - `POST {前缀}/responses`，请求体 `{}`；
   - `POST {前缀}/v1/messages`，请求体 `{}`，带 `anthropic-version: 2023-06-01`。
   列表请求成功时，Chat Completions 和 Responses 只试列表所在的前缀；否则每个候选都试。
4. **本机服务的详情接口**：主机是本机或内网地址时，顺带试 `GET {H}/api/version`（Ollama）、`GET {H}/api/v1/models`（LM Studio）、`GET {H}/props`（llama.cpp），认出哪一种就按 §6.5 读它的详情。列表条目的 `owned_by` 是 `llamacpp` 时，不管地址在哪都读 `/props`。
5. **请求体为空的 POST 不会产生费用**：服务在校验请求体时就拒绝，不调用模型，不产生 token。目录方案和 Token Plan 接入都定过"不偷偷发起计费请求"，这里一样。
6. **没有列表但有协议**时，探测结果要求用户填模型名（§3.3）。

### 4.4 判断

| 回答 | 判断 |
|---|---|
| 400、422，正文是 JSON | 接口存在：请求体不合格是预期的 |
| 401、403，不论正文 | 对照回 404 时，说明接口存在、鉴权没过：有密钥时换请求头再试（§4.5），都不行就是密钥被拒；没有密钥时提示"需要 API Key"。不看正文，因为有的服务回纯文本（DeepSeek 回 `Authentication Fails`） |
| 429 | 接口存在（限流也说明有这个接口） |
| 2xx，正文是 JSON | 接口存在（少见） |
| 404、405 | 没有这个接口 |
| 2xx、400、422，正文不是 JSON（例如网页） | 没有这个接口。不少网关的管理界面对任何路径都回首页，状态码是 200 |
| 5xx、超时、连不上 | 不知道；所有请求都这样时报"连不上" |

同一种协议的几个候选前缀都"存在"时，取列表所在的那个；没有列表就取第一个。实测 Gemini 的兼容接口对 `…/openai/chat/completions` 和 `…/openai/v1/chat/completions` 回同一个校验错误，两个都能用，不存在的路径仍回 404（§2.4）。

**对照路径决定结论可不可信**：

- 对照回 404 或 405：上表的判断成立。§2.4 实测的服务大多如此。
- 对照也回 400、401、403 或 422：状态码说明不了什么。Portkey 的自部署版把 `/v1` 下所有路径都转发出去，Helicone、Portkey 托管版对任何路径都回 401。这时只认两件事：列表回出了模型，或者用户给了 `--api`。有列表就按只讲 Chat Completions 接入，Messages、Responses 记为"不确定"；界面写"没法确认它是否讲 Anthropic 协议"，用户可以在高级选项里指定（§11）。不发计费请求去确认。

两条补充：

- **Messages 要看错误体的形状**：Anthropic 格式的错误（`{"type": "error", "error": {…}}`）算有；OpenAI 格式的 400 只在正文提到字段校验（`model`、`messages`、`max_tokens` 之类）时算有，免得把网关对未知路径的通用 400 当成接口。
- **先鉴权再路由的服务**（例如 DeepSeek 的官方接口）没有密钥时连不存在的路径也回 401（§2.4 的实测）。所以有密钥时一律带上；没有密钥、所有请求都回 401 时，提示"需要 API Key"。
- **列表公开、其余要密钥的服务**（OpenRouter：谁都能列模型，对话接口不带密钥回 401）：一个协议都没探到、又有请求被要求鉴权时，结论就是"需要 API Key"或"密钥被拒"。不因为列表拿到了就报"没找到协议"，也不按上面"对照不可信"的规则从列表推定讲 Chat Completions：所有 POST 连对照都回 401 的服务，说明的是要密钥，不是"对什么都回同样的错误"（2026-11-10 实测发现，已改）。
- **先校验请求体再鉴权的服务**（Vercel AI Gateway、Gemini 的兼容接口）不带密钥时也回 400，探测会当作不要密钥接入，等到真正对话才被拒。不发计费请求就分不出来，所以不拦；自定义服务在"管理模型"里随时可以设置或更换密钥（§11）。

### 4.5 鉴权请求头

- **列表、Chat Completions、Responses**：先用 `Authorization: Bearer <密钥>`；回 401 或 403 时依次换 `x-api-key`、`api-key`。用通的那个记下来（§8）。列表请求不带 `anthropic-version`：New API 看到 `x-api-key` 加 `anthropic-version` 会回 Anthropic 形状的列表，OpenRouter 还会给每个 ID 加上 `anthropic/` 前缀，和 Chat Completions 要的 ID 对不上。
- **Messages**：先 `x-api-key`（Anthropic 的标准，也是网关为只讲 Anthropic 协议的客户端准备的），回 401 再换 `Authorization: Bearer`。vLLM、SGLang、Ollama Cloud 的 Messages 只认 Bearer，Cloudflare 的 Anthropic 透传只认 `x-api-key`，两种都要能走通。
- 一个请求只带一种鉴权头，不同时带两个，结果才知道是哪个头起了作用。
- 没填密钥就不带任何鉴权头。
- 几种请求头都被拒时，提示用第一种（常用的那种）得到的原话：服务不读的请求头只会换来"没有提供密钥"，说的不是密钥本身（百炼 Token Plan 实测：Bearer 回 `Invalid API-key provided.`，`api-key` 回 `No API-key provided.`）。

### 4.6 认出特定的服务

- **内置服务商的官方地址**：规整后的主机名与某个内置服务商的 `base_url` 主机相同时（`api.deepseek.com`、`api.openai.com`、`api.anthropic.com`、`openrouter.ai`……），探测照做，结果里附一句提示和"改用内置"的选项。内置条目有这家专门的思考参数格式、人民币价格和渠道行，自定义服务只能按通用规则走。不拦着用户：同一家开两个条目（例如两把密钥）也是合理的。
- **Azure**（`*.openai.azure.com`、`*.services.ai.azure.com`、`*.cognitiveservices.azure.com`）：列表列的是模型，请求里要写的是部署名。列表照用，部署名常常和模型同名；结果里提示"Azure 的请求用部署名"，并给出补填部署名的一栏（同 §3.3 "没有列表"）。
- **Bedrock runtime**（`bedrock-runtime.*.amazonaws.com`）：没有列表，要填模型名；密钥是 Bedrock 的 API Key。
- **Vertex**（`aiplatform.googleapis.com`）：要每小时过期的 OAuth 令牌，表单接不了。提示改在 `models.toml` 里用现有的 `api_key_command`（例如 `["gcloud", "auth", "print-access-token"]`，内核每 5 分钟重取一次）。
- **Portkey**：要 `x-portkey-*` 请求头才能路由，提示在 `models.toml` 里写 `headers`。

### 4.7 结果

| 字段 | 内容 |
|---|---|
| `base_url` | 规整后的地址 |
| `protocols` | 每种协议是否存在、前缀、用通的请求头 |
| `list` | 列表的地址、形状（OpenAI、Anthropic、Ollama）、模型数 |
| `models` | 模型数、其中参数已知的数量 |
| `builtin` | 认出的内置服务商（§4.6），没有就不出现 |
| `problems` | 没成功的步骤和原因，只用于提示 |

结果不单独保存：协议和前缀写进 `models.toml`（§8），列表写进现有的列表缓存（目录方案 §7）。

### 4.8 时间与费用

- 列表最多 3 个请求（加上换请求头的重试），协议 3–6 个并行请求。每个请求连接 5 秒、总共 10 秒超时，不跟随重定向（§12）。
- 通常一两秒；最坏是所有候选都超时，约 20 秒，界面一直显示"正在检测…"，可以取消。
- 只在接入、"重新检测"和改地址时探测；之后的列表刷新沿用目录方案 §3.2 的节奏。

## 5. 每个模型走哪种协议

一个自定义服务可以同时讲几种协议。每个模型按下面的顺序选，取第一条成立的（D-602）：

1. `models.toml` 的模型条目写了 `api`：用它（现有字段）。
2. 列表写明了这个模型支持哪些协议（New API 的 `supported_endpoint_types`）：只在它写的协议里选，先后仍按下面几条。
3. 模型是 Claude（它的规范模型或厂商行属于 Anthropic），而且服务讲 Messages：Messages。
4. 服务讲 Chat Completions：Chat Completions。
5. 只讲 Messages：Messages。

为什么 Claude 要走 Messages：

- **缓存**：Anthropic 的提示缓存要求请求里写缓存断点。her 的 Messages 适配器在系统提示和最后一条消息上写断点（`api/anthropic.rs` 的 `cache_control`，默认开），Chat Completions 适配器不写。网关转给 Claude 时，Messages 请求原样转发，断点跟着走；Chat Completions 请求里本来就没有断点。以 LiteLLM 为例，它的文档写明：Claude 上游（Anthropic、Bedrock、Vertex）的 `/v1/messages` 原样转发，`cache_control` 照发；Chat Completions 要等管理员打开自动加断点（`enable_anthropic_prompt_caching`，默认关）才有缓存。缓存命中的输入按原价的十分之一计费，智能体每轮都重发整段对话，长会话里输入占大头。
- **思考**：Claude 在工具调用之间的思考内容带签名，下一轮要原样发回。Messages 适配器原生处理；经 Chat Completions 转一道时，各网关的字段不统一（`reasoning_content`、`thinking_blocks`、`reasoning_details`），容易丢。
- **生态里已经这样做**：公共目录里有 17 家服务给同一个服务里的模型配了不同协议，GitHub Copilot、Azure、Cloudflare AI Gateway、ZenMux 等都是 Claude 走 Anthropic 协议、其余走 OpenAI 协议。

为什么不是别的组合：

- **OpenAI 的推理模型不走 Responses**：`openai_responses.rs` 照 ChatGPT 订阅的线路写（工具包成 `namespace`、`store: false`），没有在普通 Responses 接口上验证过，与 standalone-cli-design D-545 的理由相同。探测到 Responses 先记下，通用适配器验证后加一条规则即可（§14.2）。
- **其他模型不走 Messages**：网关把 Messages 请求转给非 Claude 模型时要翻译一道，而 her 在 Messages 上发的思考参数（`adaptive`、`output_config.effort`）是 Anthropic 的，别的模型未必认。Chat Completions 是网关给这些模型的原生路径。

## 6. 模型参数

### 6.1 层次（D-603）

目录方案 §4.1 的合并顺序不变，只在"厂商行"和"默认值"之间加一层：

| 顺序 | 来源 | 本文的变化 |
|---|---|---|
| 1 | 用户 `models.toml` | 模型条目可以写 `canonical`（§7.3） |
| 2 | 渠道的声明 | 不变 |
| 3 | 列表自带的参数 | 读的字段变多（§6.5） |
| 4 | 公共目录里这个渠道的行 | 不变。自定义服务没有渠道行 |
| 5 | 公共目录里模型厂商的行 | 不变 |
| 6 | **规范模型**（新） | 按别名表找到规范模型，取它的参数（§6.2–§6.4） |
| 7 | 默认值 | 不变 |

第 6 层对所有渠道都生效，不只自定义服务：腾讯 Token Plan、火山 Agent Plan 这类只靠厂商行的渠道也会多认出一些模型（§6.8）。

### 6.2 别名表

- **来源**：
  - 公共目录全部 235 家里的对话模型行（与目录方案 §6.1 的资格过滤相同），每行的 ID 指向它的 `canonical_model_id`。2026-11-09 的数据：8,934 行里 7,606 行（85%）有规范模型；去掉非对话模型后约 2,900 个名字，对应 517 个规范模型。
  - 公共目录的实验室级文件 `models.dev/models.json`（以规范模型为键，610 个）里的 Hugging Face 仓库链接：vLLM、SGLang 不改名时用仓库名当模型名。2026-11-10 共 244 个仓库名，其中 72 个是各家行里没有的写法，例如 `meta-llama/Llama-4-Maverick-17B-128E-Instruct` → `meta/llama-4-maverick-17b-instruct`。
- **名字指向几个规范模型时**：参数一致（上下文相差 6% 以内、推理一致，通常是同一个模型的日期版本，例如 `qwen3.8-max` 与 `qwen3.8-max-preview`）就取最短的那个；不一致就算歧义，不猜。这样的名字有 27 个。
- **没有规范模型的名字**：只认模型厂商自己的行，与现在第 5 层的规则相同（D-331）。`auto`、`openrouter/auto` 这类路由名都没有规范模型，所以不会串到不相干的模型上。
- **规范模型的参数**，取第一个有的：
  1. 模型厂商自己的行（规范模型 ID 的前缀对应的那一家，例如 `anthropic/…` 对应 `anthropic`），连同它写的思考控制方式；
  2. 实验室级文件里这个规范模型的条目：上下文、输出上限、图片、能否推理、能否调用工具（572 / 627 个规范模型有）。这个文件不写思考的控制方式，控制方式取各行里最常见的那一种；
  3. 都没有时取各行的多数：能否推理、收图、调用工具按多数，上下文和输出上限取中位数中较小的那个。

  第 2 条和第 3 条实测差别不大（与渠道自己的行比，上下文一致 86% 对 85%），选实验室条目是因为它是实验室自己公布的规格，规则也简单。价格和停用状态一律不借，与厂商行相同。
- **生成与更新**：同步程序（目录方案 §5.1）多产出两个文件：`aliases.json`（名字 → 规范模型，只收不能由规范模型 ID 本身推出的名字，约 150 KB）和 `canonical.json`（规范模型 → 参数，与现有快照同一种行格式，约 180 KB）。编译进二进制，常驻服务每 24 小时随公共目录一起刷新（它本来就下载完整的上游文件）。
- **启动开销**：与厂商行相同的做法（目录方案 §5.3），构建时生成名字的哈希有序表，大多数名字查一次索引就知道没有，不解析文件；查到了才读那一行。

### 6.3 名字规整（D-604）

要查的名字先是服务给的模型 ID，其次是列表条目里另带的名字：OpenRouter 的 `alias_target`（`~anthropic/claude-opus-latest` 这类别名指向的模型）和 `hugging_face_id`，vLLM、One API 的 `root`（改了名的部署、LoRA 指向的基座），Gemini 的 `baseModelId`。每个名字依次生成下面的写法，在别名表里查（不分大小写），第一个查到的为准：

| 步骤 | 规则 | 例子 |
|---|---|---|
| 1 | 原样 | `claude-sonnet-5.5` |
| 2 | 去掉 `/` 前面的部分，从长到短逐段去 | `fireworks_ai/accounts/fireworks/models/x` → … → `x` |
| 3 | 去掉路由后缀，可以叠加：`:free`、`:nitro`、`:exacto`、`:floor`、`:batch`、`:online`、`:thinking`、`:extended`、`:beta`、`:latest`、`:cheapest` | `openai/gpt-5.2:nitro:exacto` |
| 4 | 去掉 `@` 及其后面：Vertex 的日期、LM Studio 的量化、区域。`@` 后面是 `thinking`、`effort` 这类设置时（New API 的 `qwen3-max@effort:high`）算弱匹配，见第 12 步 | `claude-sonnet-4-6@default`、`gemma-4-26b-a4b@q4_k_m` |
| 5 | 去掉区域前缀（`us.`、`eu.`、`apac.`、`ap.`、`au.`、`jp.`、`ca.`、`global.`、`us-gov.`、`in.`）和 Bedrock 的厂商前缀（`anthropic.`、`meta.`、`openai.`……） | `us.anthropic.claude-opus-4-1-20250805-v1:0` |
| 6 | 去掉版本后缀 `-v1:0`、`-1:0`、`:0` | 同上 |
| 7 | 去掉量化和格式后缀 `-fp8`、`-int4`、`-nvfp4`、`-awq`、`-gptq`、`.gguf`、`-mlx`、`-bf16`、`-4bit`、`-q4_k_m`…… | `Qwen3-30B-A3B-Q4_K_M.gguf` |
| 8 | Ollama 标签 `名字:标签` 改成 `名字-标签`，`:latest` 直接去掉 | `qwen3:32b` → `qwen3-32b` |
| 9 | 数字之间的 `p` 换成 `.`；数字之间的 `.` 与 `-` 互换 | `qwen3p8-max` → `qwen3.8-max`；`claude-sonnet-4.5` ↔ `claude-sonnet-4-5` |
| 10 | Claude 的版本号和系列名换位置 | `claude-4.5-sonnet`、`claude-4-5-sonnet` → `claude-sonnet-4-5` |
| 11 | 去掉厂商词和所有符号再比，只接受唯一的结果 | `claude-opus4-7`、`openai-gpt-61-terra` |
| 12 | **弱匹配**：去掉日期（`-20250929`、`-2024-08-06`、`-0613`、`-260428`、`-05-20`、`-10-2025`），修饰后缀（`-fast`、`-ultrafast`、`-batch`、`-thinking`、`-think`、`-nothinking`、`-nothink`、`-no-thinking`、`-search`、`-high`、`-low`、`-turbo`、`-tput`、`-all`、`-free`、`-tee`、`-preview`、`-latest`、`-exp`），中转站加的前缀（`coding-`、`cc-`），以及第 4 步的设置和其余任何 `:标签` | `openai/gpt-5.4-fast` → `gpt-5.4` |

- 规则只增加候选写法，不改发出去的模型 ID：请求里永远是服务自己的叫法（目录方案 §4.5）。
- 原样排在最前，所以 `qwen3-max`、`kimi-k2-thinking` 这种后缀本来就是名字一部分的模型先按原名查到，不会被去掉后缀。只有原名查不到才往下试。

### 6.4 匹配强弱（D-604）

| 匹配 | 借哪些参数 | 不借 | 理由 |
|---|---|---|---|
| 精确（第 1–11 步） | 与厂商行相同：上下文、输出上限、能否推理、思考档位、图片、发布日期、是否对话模型 | 价格、停用状态 | 新认出的模型 99% 对到正确的规范模型（§6.6） |
| 弱（第 12 步） | 上下文、图片、是否对话模型 | 输出上限、推理、思考档位 | 模型对的 96%，但推理标记只有 73%–94% 一致；日期版本的输出上限常不同（`gpt-4o-2024-05-13` 是 4,096，`gpt-4o` 是 16,384）；`-thinking`、`-high` 这类后缀往往说明网关已经替模型定好了思考方式，再发参数可能冲突 |

两种匹配的模型都算 `known`，`ModelSummary.canonical` 写出规范模型和是否弱匹配（§9），"管理模型"里显示"参照 Claude Sonnet 5.5"或"按名称推测：参照 GPT-5.4"，认错了用户一眼能看出来并改掉（§7.3）。

### 6.5 列表里的参数（D-605）

列表自带的参数排在目录前面（第 3 层），因为它说的是这个部署的实际情况：OpenRouter 和 Vercel AI Gateway 都把 Claude Sonnet 4.5 列为 100 万上下文，公共目录里 Anthropic 那一行写的是 20 万。现在只读 OpenRouter 的字段；扩展为按下表认字段，同一项取第一个有的：

| 参数 | 字段 | 出自 |
|---|---|---|
| 上下文 | 先取运行中的上下文：llama.cpp `/props` 的 `default_generation_settings.n_ctx`，LM Studio 已加载实例的 `context_length`。其次 `context_length`、`context_window`、`max_context_length`、`max_model_len`、`max_input_tokens`、`inputTokenLimit`、`metadata.context_length`、`top_provider.context_length`。最后才是训练时的上下文：llama.cpp 的 `meta.n_ctx_train`、Ollama `/api/show` 的 `<架构>.context_length` | OpenRouter、Together；Vercel AI Gateway、Groq；Mistral、LM Studio；vLLM、SGLang；Anthropic、LiteLLM；Gemini；DeepInfra |
| 输出上限 | `max_output_tokens`、`max_completion_tokens`、`outputTokenLimit`、`top_provider.max_completion_tokens`；`max_tokens` 只在同一条里另有上下文字段、且比它小时才算 | LiteLLM；Groq；Gemini；OpenRouter；Anthropic、Vercel AI Gateway |
| 图片 | `architecture.input_modalities` 或 `modalities.input` 含 `image`；`capabilities.image_input.supported`；`capabilities.vision`；`capabilities` 数组含 `vision`；`tags` 含 `vision`；`supports_vision` | OpenRouter、llama.cpp、Vercel AI Gateway；Anthropic；LM Studio、Mistral；Ollama；DeepInfra；LiteLLM |
| 工具 | `supported_parameters` 含 `tools`；`tags` 含 `tool-use`；`capabilities.function_calling`、`capabilities.trained_for_tool_use`；`capabilities` 数组含 `tools`；`supports_function_calling` | OpenRouter；Vercel AI Gateway；Mistral、LM Studio；Ollama；LiteLLM |
| 思考的控制方式 | `reasoning_options`（与公共目录同一种格式，按目录行的规则推导档位，目录方案 §18.2）；Anthropic 的 `capabilities.thinking.types` 和 `capabilities.effort`；OpenRouter 的 `reasoning.supported_efforts` | Vercel AI Gateway；Anthropic；OpenRouter |
| 只知道能推理 | `supported_parameters` 含 `reasoning`；`tags` 含 `reasoning`；`capabilities.reasoning`；`capabilities` 数组含 `thinking`；`supports_reasoning`；`thinking: true` | OpenRouter；Vercel AI Gateway、DeepInfra；Mistral、LM Studio；Ollama；LiteLLM；Gemini |
| 不是对话模型 | `type` 是 `embedding`、`image` 等（与目录方案 §6.1 相同）；`mode` 不是 `chat`（LiteLLM）；Ollama 的 `capabilities` 没有 `completion` | Vercel AI Gateway、Together；LiteLLM；Ollama |
| 协议 | `supported_endpoint_types` → §5 | New API |
| 另一个名字 | `alias_target`、`hugging_face_id`、`root`、`baseModelId` → §6.3 | OpenRouter；vLLM、One API；Gemini |

几个坑，都来自实际数据：

- **字段名不保证意思**：DeepInfra 的 `max_tokens` 在全部 91 个对话模型上都等于上下文，不是输出上限；Vercel AI Gateway 的 `max_tokens` 才是输出上限。所以 `max_tokens` 要和上下文比过才用。
- **训练时的上下文不是运行时的上下文**：Ollama、llama.cpp、LM Studio 都区分两者。Ollama 的源码里，没设 `OLLAMA_CONTEXT_LENGTH` 时运行上下文按显存分三档：47 GiB 及以上 262,144，23 GiB 及以上 32,768，其余 4,096；超出的部分它默认悄悄丢掉较早的消息。`/api/show` 只给训练时的数，已加载模型的实际上下文在 `/api/ps`。所以运行时的数优先，训练时的数排最后。内置的 Ollama 条目现在用的是训练时的数，同样有这个问题（Q-335）。
- **价格单位各不相同**：OpenRouter、Vercel AI Gateway 是每 token 的美元字符串，LiteLLM 是每 token 的浮点数，DeepInfra、Together 是每百万 token，New API 是倍率。一期只读现在已经读的 OpenRouter 形式（`pricing.prompt`、`pricing.completion`），其余显示"费用未知"。中转站按自己的价格收费，借目录的价格反而会误导（目录方案 D-331 本来就不借价格）。

### 6.6 实测

方法见附录 A。数据：2026-11-09 的公共目录（235 家、8,934 行）和 LiteLLM 的模型表（3,457 个对话模型，它自己维护上下文、推理、图片的值）。

**网关型渠道，排除被测的那一家**（34 家：OpenRouter、Vercel AI Gateway、GitHub Copilot、Bedrock、Vertex、Azure、Fireworks、Together、Groq、DeepInfra、SiliconFlow、Hugging Face、NVIDIA、Cloudflare、Ollama Cloud、LM Studio、AiHubMix、302.AI、ZenMux、ofox、Requesty 等，共 2,629 个对话模型；标签是这家行上的规范模型）：

| 阶段 | 这一步认出 | 累计 | 对到正确的规范模型 | 上下文一致 | 推理一致 | 图片一致 |
|---|---|---|---|---|---|---|
| 现在的规则（厂商行） | 1,078 | 41.0% | 576 / 598 | 92% | 97% | 98% |
| 别名表与规整（第 1–10 步） | 1,087 | 82.4% | 1,025 / 1,035 | 86% | 95% | 98% |
| 去掉符号再比（第 11 步） | 97 | 86.0% | 83 / 83 | 68% | 98% | 98% |
| 弱匹配（第 12 步） | 99 | 89.8% | 78 / 81 | 77% | 94% | 99% |
| 认不出 | 268 | | | | | |

"一致"是和这家自己的行比：上下文相差 6% 以内，推理、图片相同。上下文不一致多半不是认错了模型，而是这家给模型限了长度：第 11 步认出的 97 个里 Venice 一家占 31 个，它改写名字，也常把上下文限小（31 个不一致里占 10 个）。这正是列表参数要排在目录前面的原因（§6.5），也是 §6.7 要处理的情况。

**LiteLLM 的模型表**（3,457 个对话模型，按网关上会出现的上游叫法查，跟它自己的值比）：

| 阶段 | 累计 | 上下文一致 | 推理一致 | 图片一致 |
|---|---|---|---|---|
| 现在的规则 | 27.6% | 80% | 98% | 94% |
| + 别名表与规整 | 66.3% | 83% | 97% | 98% |
| + 去掉符号再比 | 69.0% | 61% | 90% | 100% |
| + 弱匹配 | 74.4% | 78% | 73% | 98% |

这张表收了大量冷门托管方和专用部署（承诺用量、区域部署），比一般网关难认，但趋势一致。弱匹配的推理一致只有 73%，所以弱匹配不借推理（§6.4）。

**认不出的是什么**：只在一家出现的模型（`nex-agi/Nex-N2-Pro`）、服务自己的路由名和免费试用名（`exo-free`）、公共目录还没收的新模型，以及 Azure 部署名这类用户自取的名字（`prod-gpt4o-eastus`）。这些靠列表参数和用户补参数（§7）。

### 6.7 自部署的上下文与输出上限（D-606）

vLLM、SGLang 一类服务要求"提示 token + 请求的输出上限 ≤ 部署的上下文长度"，超了直接拒绝。her 请求的输出上限是"模型的输出上限与 32,000 取小"（`Model::default_max_output`）。设想一个 vLLM 把 Qwen3 限在 32,768 上下文：列表给了 32,768（第 3 层），输出上限从目录借来 32,768 以上，请求就带 32,000，提示只要超过 768 个 token 就被拒；压缩重试也救不回来，因为光输出上限就占满了。

规则：**列表给的上下文比目录行的小时，不借目录行的输出上限**。这时按"输出上限未知"处理（目录方案 §4.8）：Chat Completions 不带输出上限，由服务按剩余空间决定；Messages 必须带时用 8,192。

### 6.8 对内置渠道的影响

第 6 层对所有渠道生效。已有渠道行或厂商行的模型不受影响（它们在第 4、5 层就有结果），守卫测试 `crates/her-llm/tests/channels.rs` 照常遍历每个内置渠道的每个模型和档位。会变的是现在落到默认值的模型：它们会变成参数已知，可能因此进菜单。实现时用守卫测试列出前后差异，逐个看过再合入。

## 7. 认不出的模型

### 7.1 默认值

不变，沿用目录方案 §4.1、§4.8：上下文 128,000；输出上限不设（Messages 用 8,192）；不收图片；不推理，只有"默认"思考档位，请求里不发思考参数；假定能调用工具（不过滤）。标"参数未知"。

图片按不收处理的理由也不变：把图片发给不收图的模型，这个会话之后每次请求都会失败，比少看一张图严重得多。

### 7.2 进不进菜单（D-607）

目录方案 §6.2 只默认显示参数已知的模型。对自定义服务改一条：**已识别的模型不足 12 个时，按列表顺序用未识别的补到 12 个**。

- 只跑一个自己微调模型的 vLLM：唯一的模型进菜单，标"参数未知"。
- 几百个模型的中转站：已识别的超过 12 个，未识别的不进菜单，照旧可以在"管理模型"里打开。
- 内置服务商不变。

### 7.3 用户补参数（D-608）

"管理模型"里，参数未知和弱匹配的模型行多一个"设置参数"，展开后两种填法：

1. **像哪个模型**：搜索公共目录里的模型（按名字），选一个，套用它的全部参数，效果与精确匹配相同。适合部署名、改了名的模型。搜索框预先填上按 §6.3 规整过的名字，最像的几个排在最前（`prod-gpt4o-eastus` 会先列出 GPT-4o）。写入：

   ```toml
   [[providers.acme.models]]
   id = "prod-gpt4o-eastus"
   canonical = "openai/gpt-4o"
   ```

2. **直接填**：上下文窗口、能看图片、会思考（开了之后，档位按协议给保守的一组，目录方案 §18.2 的"没有目录行但模型能推理"）。写入现有字段 `context_window`、`input`、`reasoning`。

两种可以叠加：先选参照模型，再改上下文。字段的合并与现在一样，用户写的永远优先（第 1 层）。清除就是删掉这个条目，回到自动识别。

`canonical` 也用于账号连接：千问办公的 `flash`、`pro` 将来要从目录补字段时（目录方案 Q-147），连接的声明里写上规范模型即可，不用再造一种映射。

已知的取舍：模型条目会让模型算作"已配置"，总在菜单里（目录方案 §6.2）。用户给一个模型补了参数，通常就是要用它，这个副作用可以接受。

### 7.4 以后：从错误里学

服务的错误信息常常带着真实的限制：`This model's maximum context length is 32768 tokens`、`max_tokens must be less than or equal to 8192`、`image input is not supported`、`Unrecognized request argument supplied: reasoning_effort`。可以把它们记下来，作为列表之下、目录之上的一层。主流编码客户端里已有这样做的：网关拒绝思考参数时去掉那个字段重试一次，并按模型和地址记住。

一期不做，理由有两条：思考档位默认是"默认"，什么都不发，只有用户自己选了档位才可能被拒，被拒时界面能看到错误，换回"默认"即可；现在也没有数据说明这类失败有多常见。触发条件和做法见 §14.2。现有的上下文超限处理（压缩后重试）不变。

## 8. 配置与存储

自定义服务写在 `models.toml`，与现在手写的服务商同一个位置、同一套字段，新增的只有两个：

```toml
[providers.acme]
name = "Acme 网关"
api = "openai-chat"                                    # 默认协议（§5 第 4、5 条）
base_url = "https://llm.acme.com/v1"                   # 默认协议的前缀
endpoints = { anthropic-messages = "https://llm.acme.com" }   # 新：同一个服务讲的其他协议及其前缀
compat = { auth_header = "bearer" }                    # 探测到的请求头

[[providers.acme.models]]
id = "prod-gpt4o-eastus"
canonical = "openai/gpt-4o"                            # 新：参照模型（§7.3）
context_window = 64000
```

| 层级 | 字段 | 说明 |
|---|---|---|
| 服务商 | `endpoints` | 新。默认协议之外的协议 → 前缀；按 §5 给模型选协议时用。一期只会出现 `anthropic-messages`，探到的 `openai-responses` 也写上，一期不用 |
| 服务商 | `compat.auth_header` | 扩展：现在只对 Messages 生效、取值只有 `x_api_key` 和 `bearer`；改为对所有协议生效，新增 `api_key`（`api-key: <密钥>`）。也认按请求头原名写的 `x-api-key`、`api-key` |
| 模型 | `canonical` | 新。参照的规范模型 ID，作为这个模型的第 6 层，跳过名字查找 |

- **密钥**：照旧按服务商 ID 存进凭据存储（钥匙串，D-39），`models.toml` 里不出现。
- **服务商 ID**：用户没给时由名称或主机名生成（`llm.acme.com` → `acme`，`localhost:8000` → `localhost-8000`），只用小写字母、数字和 `-`，和已有的重复时加 `-2`。不用 `local-` 开头，那是本机账号连接的。
- **运行中生效**（D-609）：现在服务启动时读一次 `models.toml`。改为把 `models.toml` 里非内置的服务商单独放进一张可整体替换的表（`ModelRegistry::replace_custom`），与内置服务商一起按 ID 排序列出。`providers/add`、`update`、`remove` 和 `models/configure` 写完文件就重读这张表（`Catalog::custom_providers`）、清掉合并结果的缓存、发 `models/changed`，不用重启服务。对内置服务商的覆盖（例如改 DeepSeek 的 `base_url`）仍在启动时读，与现在相同。
- **写文件**：用现在 `her login --base-url` 的做法：同一把文件锁，`toml_edit` 保留用户的注释和顺序，先写临时文件再改名。
- **列表缓存、选择**：沿用 `~/.her/models/lists/<provider>.json` 和 `selection.json`（目录方案 §7）。

## 9. 协议

都在协议版本 2 之内，由 capabilities 的 `custom_endpoints` 声明。方案评审通过前，五个新方法在 schema 里标为实验（`x-stability: experimental`），可以随版本改；定下来再转为稳定。

| 方法 | 参数 | 结果 | 说明 |
|---|---|---|---|
| `providers/add` | `base_url`、`api_key?`、`name?`、`api?`、`models?` | `{ provider: ProviderSummary, probe: ProbeReport }` | 探测（§4），成功才写 `models.toml`、保存密钥、写列表缓存。`api` 指定时跳过协议探测；`models` 是用户手填的模型名（没有列表时）。等探测结束才返回 |
| `providers/update` | `id`、`name?`、`base_url?`、`api_key?`、`api?` | 同上 | 改地址、密钥或协议时重新探测，失败则什么都不改；只改名称不探测。"重新检测"就是用原地址调它 |
| `providers/remove` | `id` | `{}` | 删除 `models.toml` 里的条目、保存的密钥、列表缓存。用着它的会话照旧能打开（standalone-cli-design D-536） |
| `models/configure` | `model`、`canonical?`、`context_window?`、`images?`、`reasoning?`、`clear?` | `{ model: ModelSummary }` | 写这个模型的条目（§7.3）。值为 `null` 表示删掉这个字段，`clear` 删掉整个条目。`canonical` 可以写任何能找到它的名字（`GPT-4o`、`gpt-4o-2024-08-06`），保存为规范模型 ID；找不到时报 −32602。只改自定义服务的模型，内置服务商的报 −32602 |
| `models/catalog` | `query`、`limit?` | `{ models: { id, name, context_window?, images, reasoning }[] }` | 在规范模型里按名字找，给"像哪个模型"的输入框做候选。只查内置的别名表，不发请求。排序：名字规整后完全相同的最前，然后是去符号后相同、前缀、包含、分词命中 |

新类型：

```ts
type Api = "openai-chat" | "anthropic-messages" | "openai-responses";

interface EndpointProtocol { api: Api; base_url: string; auth_header?: "bearer" | "x_api_key" | "api_key" }   // 与 models.toml 的取值相同

interface ProbeSummary {       // providers/add、update 结果里的 probe
  base_url: string;            // 规整后的地址（§4.1）
  protocols: EndpointProtocol[];
  unsure: Api[];               // 对照不回 404，判断不了的协议（§4.4）
  models: number;              // 列表里的对话模型数
  known: number;               // 其中认出参数的
  no_list?: boolean;           // 没有列表，用的是用户填的模型名
  builtin?: string;            // 认出的内置服务商 ID（§4.6）
  insecure?: boolean;          // 公网地址用 http（D-610）
  note?: "azure_deployments" | "bedrock_models" | "vertex_token" | "portkey_headers";   // §4.6
}

// ProviderSummary 新增：只有自定义服务有
custom?: { base_url: string; protocols: EndpointProtocol[] };

// ModelSummary 新增：参数参照的模型
canonical?: { id: string; name: string; guessed: boolean };   // guessed：弱匹配（§6.4）
```

`canonical` 在参数来自别人的行时出现：模型厂商的行、规范模型，或用户指定的参照模型。参数来自这个渠道自己的行、列表或用户直接填的字段时不出现。这样自定义服务的每个已识别模型都写得出"参照谁"。

错误：密钥被拒沿用 `key_rejected`（−32021）。探测失败用新错误 −32048 `endpoint_unusable`，`data.reason` 是 `invalid_url`、`unreachable`、`tls`、`not_a_model_service`、`key_required`、`no_models` 之一，对应 §3.3 的各行。公网 http 不是错误（D-610），只在结果里标 `insecure`。

## 10. 命令行

| 命令 | 说明 |
|---|---|
| `her login NAME --base-url URL` | 探测并接入（§3.2）；`--api` 可选，写了就跳过协议探测 |
| `her login NAME --base-url URL --model ID…` | 服务没有列表时手填模型名 |
| `her models set PROVIDER/MODEL --like CANONICAL` | 指定参照模型（§7.3） |
| `her models set PROVIDER/MODEL --context 64k --images on --reasoning off` | 直接填参数；`--clear` 删掉这个条目 |
| `her logout NAME --remove` | 自定义服务：删掉条目和密钥（不带 `--remove` 只删密钥，与内置服务商相同） |

同一个名字再执行 `her login NAME --base-url URL` 是同一个服务：用新给的地址、密钥重新探测并更新条目（即 `providers/update`），不会多出一个 `NAME-2`。

`her models` 的输出里，参数参照别人的模型标 `like Claude Sonnet 4.5`，弱匹配的标 `guessed: like GPT-5.4`，参数未知的照旧标 `details unknown`。`her login --base-url` 打印探到的协议和前缀、模型数，以及 http、内置服务商地址、协议没法确认、Azure 等几种提示。

## 11. 桌面端

- **选择器**：`ProviderTiles` 最后加"自定义服务"一块，图标用现有的代码图标（`Brand` 里已经为 `custom` 准备了）。Token Plan 的选择器不加。
- **表单**：与 `KeyEntry` 同一种卡片：服务地址、API Key、名称三栏；底部"返回""连接"。检测中按钮写"正在检测…"，不能重复提交；成功后卡片换成结果和"完成""管理模型"；失败留在原处显示原因（§3.3）。三栏下面有一个收起的"高级"，里面只有"协议"：自动（默认）、OpenAI Chat Completions、Anthropic Messages，对应命令行的 `--api`；探测不确定时（§4.4）用户在这里指定。
- **列表的行**：自定义服务和 API 服务排在同一组，图标是代码图标，第二行写"主机名 · 显示 8 / 共 37 个模型 · 2 小时前更新"。按钮"管理模型""移除"；移除的确认文字是"移除后它的模型从菜单里消失，保存的密钥一并删除"。不需要密钥的自定义服务（本机服务）也列出来：现在这一组只列 `auth = "api_key"` 且有密钥的服务商，要放宽到所有自定义服务。
- **管理模型**：每行在上下文后面加"参照 Claude Sonnet 5.5"（弱匹配写"按名称推测：参照 GPT-5.4"）；参数未知和弱匹配的行有"设置参数"，在行内展开（§7.3），不另开弹窗；已认出的行也能改，链接颜色淡一些。"像哪个模型"边输入边给候选（`models/catalog`）。底部"连接详情"写地址、各协议的前缀和请求头，带"重新检测"按钮。
- **http 提示**：探测结果标 `insecure` 时，结果卡片里写"这个地址不加密，密钥会明文传输"，不拦着（D-610）。
- **密钥**："管理模型"底部除了"连接详情"，还有"设置密钥…"（没有密钥时）或"更换密钥…"。自定义服务的密钥用 `providers/update` 带着新密钥重新探测，探测通过才保存；不用内置服务商那种只查列表的校验，因为列表公开的服务对错密钥也会回列表。
- **模型菜单**：不变。

## 12. 安全

| 风险 | 对策 |
|---|---|
| 密钥被发到别处 | 探测和之后的请求只发往用户填的主机及其路径；带密钥的请求不跟随重定向（目录方案 §12）；候选前缀只在同一主机上变化 |
| 明文传输密钥 | 不强制 https（D-610）。公网地址用 http 时照样接入，探测结果标 `insecure`，桌面端和命令行提示"这个地址不加密，密钥会明文传输"；本机和内网地址（§4.1 第 4 条）不提示。密钥只发往这个主机，不跟随重定向，所以不会被带到别处 |
| 探测产生费用 | 只发列表请求和请求体为空的 POST，服务在校验阶段就拒绝（§4.3） |
| 恶意或损坏的应答 | 列表沿用目录方案 §3.4 的大小、数量、ID 格式限制；协议探测和对照的应答体最多读 64 KiB；错误正文只取前 500 字符用于提示 |
| 别名表被投毒 | 别名表来自公共目录，只决定参数，不决定请求发往哪里，与目录方案 §12 相同；每周同步由人审阅 |
| 项目配置改写服务 | 项目配置仍不能定义服务商（目录方案 §11.1）；自定义服务只写用户目录的 `models.toml` |
| 请求头里的密钥被日志记下 | 探测的调试日志只记状态码和请求头的名字，不记值 |

## 13. 测试与验收

### 13.1 自动测试

| 范围 | 测试 |
|---|---|
| 地址规整与候选 | §4.1、§4.2 的每条规则，含带接口路径、带 `/v1`、本机地址、查询参数被拒 |
| 判断 | §4.4 表的每一行，用 §2 收集的各家真实应答做样本：OpenAI、OpenRouter、Vercel AI Gateway、Anthropic、DeepSeek（先鉴权）、Gemini（先校验）、返回网页的管理界面 |
| 名字规整 | 约 100 个真实名字及其应有的规范模型，取自 §6.6 的数据和 §2 的调研，覆盖 §6.3 每一步；`auto` 这类名字必须认不出 |
| 别名表 | 歧义名字、日期版本合并、没有规范模型的名字只认厂商行、规范模型的参数取法 |
| 匹配强弱 | 弱匹配不借输出上限、推理和档位 |
| 列表字段 | §6.5 每种服务的真实列表条目 |
| 协议选择 | §5 的五条，含列表写明协议的模型；Claude 在讲 Messages 的服务上走 Messages，请求带缓存断点 |
| 自部署 | 列表上下文小于目录时不带输出上限 |
| 菜单 | 自定义服务已识别不足 12 个时用未识别的补足；内置服务商不变 |
| 写配置 | 保留注释；并发写有锁；失败时文件不变 |

### 13.2 端到端

her-mock 加几种网关形态，每种开在自己的端口上，像真实服务一样独占一个主机：探测会顺带试主机根下的 `/anthropic`，挂在同一个端口的不同路径下会互相串。用真实的 `her serve` 走 `providers/add` → `models/list` → 一次会话，mock 记下每个对话请求实际到达的路径和带密钥的请求头，测试逐项核对：

| 形态 | 用户填的 | 应探到 | 应发到 |
|---|---|---|---|
| 两种都讲（`/gateway`） | 带路径的地址 | Chat Completions 在 `/v1`，Messages 在根 | Claude 走 `/v1/messages` 并带缓存断点，其余走 `/v1/chat/completions` |
| 只讲 Chat Completions | 主机 | Chat Completions，Bearer | Claude 也走 `/v1/chat/completions` |
| 只讲 Messages，Anthropic 形式的列表 | 主机 | Messages 在根，`x_api_key` | 不是 Claude 的模型也走 `/v1/messages`，带 `x-api-key` |
| Azure 形态 | `…/openai/v1` | Chat Completions 在 `/openai/v1`，`api_key` | `/openai/v1/chat/completions`，带 `api-key` |
| Messages 在 `/anthropic` 下、只认 Bearer | `…/v1` | Chat Completions 在 `/v1`，Messages 在 `/anthropic`，都是 Bearer | Claude 走 `/anthropic/v1/messages` 带 Bearer 和缓存断点，其余走 `/v1/chat/completions` |
| New API，列表写明每个模型的协议 | 主机 | 两种都有 | 列表限定只走 OpenAI 的 Claude 走 `/v1/chat/completions`；两种都行的 Claude 走 `/v1/messages` |
| 先验密钥再走路由，401 回纯文本 | 主机 | 不带密钥报"需要 API Key"，错密钥报"密钥被拒"并附服务原话；对的密钥探到 Chat Completions | `/v1/chat/completions`，带 Bearer |

另外：`models/configure` 之后参数生效；`providers/remove` 之后模型消失、旧会话还能打开；命令行的 `her login --base-url` 走同样的检查。探测的判断规则另有 `her-llm/tests/probe.rs` 的 10 种形态（含对任何路径回网页的管理界面、对什么都回同样错误的网关）。

### 13.3 真实服务验收

2026-11-10 用真实的 `her` 二进制、临时的 `HER_HOME` 做了一轮（结果见 §17.3）。只发列表请求和空请求体的 POST，不带密钥或带假密钥，不产生费用。

- 本机起一个 LiteLLM 代理，上游用已授权的百炼 Token Plan，验证两种协议都探到、千问和 DeepSeek 走 Chat Completions；有 Claude 上游时再验证 Messages 与缓存命中。（未做：钥匙串里的 Token Plan 密钥已被服务拒绝，内置条目用它也一样，要换新密钥。）
- Ollama、LM Studio、vLLM 各接一次：本机地址补 `http://`，列表参数生效，菜单里有模型。
- 公网网关（OpenRouter、Vercel AI Gateway）拿到密钥后各接一次，跑 `her models check`（standalone-cli-design §7.6）。
- 识别率随每周的目录同步复测（附录 A 的脚本改成 `cargo run -p her-llm --example alias_report`），数字明显下降时同步 PR 里会看到。

### 13.4 性能

- 启动用 `tools/bench/ratchet.py --only mac` 对比改动前后（2026-11-10，macOS arm64，release 配置，每边 6 份拷贝 × 100 次）。基准用的服务商写在 `models.toml` 里，现在算自定义服务，正好走新路径。前两轮各查出一处、都已改掉：
  - 每建一个会话，内核都要列出"子进程环境里要抹掉的密钥变量"，实现时改成了先复制所有服务商配置再排序，复用会话的两项慢了 3%（p≤0.03）。改回按引用遍历。
  - 每个查不到目录行的名字都去磁盘看一次"刷新有没有留下本地别名表"，进程里第一个会话慢了约 30 微秒（2.2%–2.7%，p=0.005）。改成每次刷新只看一次。做过对照：把这一层整个跳过时这项也回到 0。
  - 改完后的一轮：16 项指标都没有回退（变化在 −1.2% 到 +1.0% 之间，没有一项同时超过 2% 且 p<0.05）。`her exec` 到首个模型请求 8.17 → 8.19 ms（+0.2%，p=0.09）。
- 二进制体积：原先估计约 0.35 MB，只算了两个数据文件。实测（同上，改动前后各编一次）：去掉符号后 81.31 MB → 82.97 MB，多 1.58 MB（+2.0%）。其中嵌入的两份数据和名字哈希约 0.36 MB（`__TEXT.__const`），代码约 1.0 MB（`__text`），异常和展开表约 0.2 MB。代码的大头是几个异步函数（探测、命令行的 `add_service`、目录刷新）：release 配置下它们在 her 和 her-server 两个 crate 里各生成一份，仓库里原有的异步代码也是这样。数据要再小，按 Q-331 改成构建时压缩。

## 14. 分期

### 14.1 一期

探测（§4）；按模型选协议（§5）；别名表、名字规整与匹配强弱（§6.2–§6.4）；列表字段（§6.5）；自部署的输出上限（§6.7）；自定义服务的菜单规则与补参数（§7.2、§7.3）；配置与运行中生效（§8）；协议（§9）；命令行（§10）；桌面端（§11）；测试与验收（§13）。

### 14.2 按需再加

| 问题 | 触发条件 | 做法 |
|---|---|---|
| OpenAI 推理模型经 Chat Completions 丢失跨轮的推理内容 | 通用 Responses 变体通过 Open Responses 的合规测试，并在 api.openai.com 或一个网关上验证通过（D-545 的条件） | 适配器加普通 Responses 变体（不用 `namespace`）；§5 加一条"OpenAI 推理模型在讲 Responses 的服务上走 Responses"；探测结果里已经有前缀 |
| 自定义服务显示不了费用 | 用户要求，或接入的多是 Vercel AI Gateway、LiteLLM 这类列表带价格的服务 | 按各家的单位读列表价格（§6.5 的坑） |
| 认不出的模型因上下文、输出上限、图片或参数被拒 | 日志或用户反馈里同一类失败反复出现 | 解析错误信息里的数字和原因，记在 `~/.her/models/learned.json`，作为列表之下、目录之上的一层 |
| LiteLLM 里管理员自取的模型名认不出 | LiteLLM 用户反馈，或探测到 LiteLLM 的服务里认不出的比例高 | 读它的模型详情接口（§2），取上游的真实模型名和参数 |
| 网关要额外的请求头 | 用户反馈 | `models.toml` 已支持 `headers`；桌面端表单加一栏 |
| 带查询参数的地址 | 出现必须带查询参数才能用的服务（Q-333） | 前缀拆成"路径 + 查询参数"，适配器拼地址时带上 |
| 只讲 Gemini 原生格式的网关 | 出现这样的服务 | 新适配器 |

### 14.3 要同步改的文档

实现时：目录方案 §4.1（第 6 层）、§4.5（别名表）、§6.2（自定义服务的菜单规则）、§11.2（新字段）、Q-147（`canonical`）；standalone-cli-design §7.3（`--api` 可选、探测）；桌面集成方案 §8.4（设置页）；protocol-design 的方法表和错误码；总方案附录 C 的配置示例。

## 15. 决策记录

| 编号 | 决策 | 理由 | 章节 |
|---|---|---|---|
| D-600 | 用户只填地址和密钥，内核探测协议、前缀、请求头和模型列表；探测只发列表请求和请求体为空的 POST | 用户多半不知道这些；空请求体在校验阶段就被拒，不产生费用，接口在与不在由状态码和错误体分得清（§2.4 实测） | §4 |
| D-601 | 一期支持 Chat Completions 和 Messages；Responses 探到先记下、不用；不做 Gemini 原生 | 两个适配器已经验收；所有网关都讲 Chat Completions，多数讲 Messages；Responses 适配器还只适配 ChatGPT 订阅 | §2、§5 |
| D-602 | 每个模型的协议：条目写明的；否则在列表写明的协议范围内（New API 的 `supported_endpoint_types`），Claude 在讲 Messages 的服务上走 Messages，其余 Chat Completions，只讲 Messages 的服务走 Messages | 保住 Claude 的提示缓存和带签名的思考；别的模型在 Chat Completions 上最原生；列表说了不支持的协议不去试 | §5 |
| D-603 | 在厂商行之后加"规范模型"一层，按公共目录的 `canonical_model_id` 建别名表，所有渠道共用 | 实测认出的比例从 41% 升到 86%，新认出的 99% 对到正确的模型 | §6.1、§6.2 |
| D-604 | 名字按 §6.3 的规则依次规整；去日期、去修饰后缀才对上的是弱匹配，只借上下文、图片和是否对话模型 | 弱匹配的推理标记只有 73%–94% 一致，输出上限常随版本变；后缀往往表示网关已经定了思考方式 | §6.3、§6.4 |
| D-605 | 列表里的参数按 §6.5 的字段表读，排在目录之前 | 列表说的是这个部署的真实限制，目录只知道模型本身 | §6.5 |
| D-606 | 列表给的上下文比目录行小时，不借目录行的输出上限 | 自部署服务要求提示加输出上限不超过上下文，借来的上限会让请求次次被拒 | §6.7 |
| D-607 | 自定义服务已识别的模型不足 12 个时，用未识别的补足菜单 | 只跑自己模型的服务接上后菜单不该是空的；大中转站的未知模型仍不进菜单 | §7.2 |
| D-608 | 认不出的模型由用户指定参照模型（`canonical`）或直接填上下文、图片、推理，写进 `models.toml` | 一个选择就能套用整组参数；字段沿用现有合并规则；同一个字段以后也给账号连接用 | §7.3 |
| D-609 | 自定义服务写在 `models.toml`；`models.toml` 里非内置的服务商作为可替换的来源，改完即生效 | 不另造配置文件；桌面端接入后不用重启内核 | §8 |
| D-610 | 不强制 https：公网地址用 http 照样接入，只提示不加密；本机和内网地址（含解析到内网的内部域名）不提示。探测和请求只发往用户填的主机，不跟随重定向 | 用户定（2026-11-10，原提议是公网必须 https）：自建服务常常没有证书，拦下来只会逼人手写配置；提示让人知道密钥会明文传输；不跟随重定向，密钥不会被带走 | §12 |
| D-611 | 认出内置服务商的地址时提示改用内置条目，不拦着 | 内置条目有专门的思考参数和价格；同一家开两个条目也合理 | §4.6 |
| D-612 | 新增 `providers/add`、`update`、`remove`、`models/configure`、`models/catalog`；`ModelSummary.canonical`、`ProviderSummary.custom`；错误 −32048 | 命令行和桌面端共用一个内核实现；界面要能说清参数参照了谁；"像哪个模型"要能搜，名字由内核按同一套规整规则找 | §9 |

## 16. 待定问题

| 编号 | 问题 | 当前假设 | 章节 |
|---|---|---|---|
| Q-330 | 弱匹配是否默认启用 | 已定（2026-11-10，用户同意）：启用，界面写"按名称推测"，只借上下文和图片 | §6.4 |
| Q-331 | 别名表编译进二进制的体积（约 0.33 MB） | 先用文本格式，便于同步 PR 审阅；体积成问题再改成构建时压缩 | §6.2 |
| Q-332 | 网关给同名模型限了更小的上下文，列表又不说（GitHub Copilot 一类，上下文一致只有 60%） | 一期靠列表参数和用户补参数；从错误里学是按需再加的第一项 | §6.6、§14.2 |
| Q-333 | 带查询参数的地址 | 一期不接受；出现必须带的服务再支持 | §4.1 |
| Q-334 | 自定义服务的思考参数格式 | 不写 `thinking_format`：有档位的模型发 `reasoning_effort`（目录方案 D-333），开关和预算不发；用户可以在 `models.toml` 写。LiteLLM 会把 `reasoning_effort` 翻译成各家自己的参数，New API 原样转发 | §5、§6.4 |
| Q-335 | Ollama 的实际上下文 | 现在内置的 Ollama 条目用 `/api/show` 的训练上下文，显存不足 23 GiB 的机器上实际只有 4,096，超出部分被 Ollama 悄悄丢掉。这是现有问题，与本方案无关，单独处理：读 `/api/ps` 的实际值，或提示用户设 `OLLAMA_CONTEXT_LENGTH` | §6.5 |
| Q-336 | Anthropic 列表里的上下文和能力字段是否要新的 `anthropic-version` | 文档示例用的是新版本号；先照常带 `2023-06-01`，有字段就读，没有就靠目录 | §6.5 |
| Q-337 | 经自定义服务用 Kimi、DeepSeek 这类要回传推理内容的模型 | 沿用目录方案 Q-146 的现状（Chat Completions 默认不回传），由 Q-146 一并决定 | §5 |

## 17. 实现记录

### 17.1 代码位置

| 部分 | 位置 |
|---|---|
| 名字规整（§6.3），`names.rs` 与构建脚本共用一份 | `crates/her-llm/catalog/name_rules.rs`、`src/names.rs` |
| 规范模型与别名表（§6.2） | `crates/her-llm/src/canonical.rs`；数据 `catalog/public/_canonical.json`（552 个规范模型）、`_aliases.json`（3,404 个名字）；构建时生成名字的哈希索引，绝大多数认不出的名字不用读表 |
| 第 6 层、按模型选协议、自部署输出上限、菜单补足（§5、§6.4、§6.7、§7.2） | `crates/her-llm/src/directory.rs` |
| 列表字段（§6.5） | `crates/her-llm/src/discovery.rs` |
| 探测（§4） | `crates/her-llm/src/probe.rs`，测试 `tests/probe.rs`（10 种服务形态） |
| 写 `models.toml`、运行中重读（§8） | `crates/her-core/src/custom_providers.rs`、`runtime::reload_custom`、`ModelRegistry::replace_custom` |
| 协议方法（§9） | `crates/her-server/src/custom_endpoints.rs`；类型在 `her-protocol/src/api.rs` |
| 命令行（§10） | `crates/her-cli/src/cmd_login.rs`、`cmd_info.rs` |
| 桌面端（§11） | `apps/desktop/src/renderer/src/settings/ProviderConnections.tsx`、`ManageModels.tsx`、`ModelConnections.tsx`，引导页 `onboarding/Onboarding.tsx` |
| 端到端（§13.2） | `crates/her-cli/tests/e2e_custom_endpoints.rs`、`e2e_login.rs`；her-mock 的 `/gateway/` 形态、Messages 流式回答，以及 `gateways.rs` 里六种各占一个端口的网关形态（`MockServer::gateway`） |
| 同步与评估脚本 | `cargo run -p her-llm --example sync_catalog -- --input <api.json> --lab <models.json> --canonical-only`；`tools/catalog/alias_eval.py` |

### 17.2 与提议不同的地方

- **运行中生效**：不复用账号连接的可替换来源，而是在 `ModelRegistry` 里单独放一张自定义服务的表（§8）。账号连接的来源按连接管理，自定义服务来自 `models.toml`，重读时整张替换最简单。
- **`models/catalog`**：提议里没有。"像哪个模型"要能按用户熟悉的写法搜（`gpt4o`、`prod-gpt4o-eastus`），名字规整只有内核有，所以加了这个只读方法。
- **列表字段**：Anthropic 列表里的思考档位按"弱到强"排好再用，不依赖 JSON 对象键的顺序（serde_json 在整个工作区一起编译时保留键的原顺序，单独编译 her-llm 时按字母排，同一份列表会得到不同顺序）。
- **规范模型的事实行**：厂商自己有几行指向同一个规范模型时（DeepSeek 把已弃用的视觉预览也指向了 1017 版），依次取 ID 与规范模型同名的行、没有弃用的行、发布最新的行；各行先按服务商和 ID 排好，结果不随 JSON 键顺序变。实测前一版在运行时刷新目录后把 1017 版叫成了"DeepSeek V4 Flash Vision Exp"。
- **密钥**：桌面端自定义服务的"设置/更换密钥"走重新探测（§11）；命令行同名再次登录即更新（§10）。
- **保存失败时**：更新已有服务时写 `models.toml` 失败，放回原来的密钥和模型列表缓存，不是删掉。
- **性能**：别名表在不在本地，每次刷新只看一次；列密钥环境变量不复制服务商配置（§13.4）。

### 17.3 验证

- **端到端，按网关形态**（§13.2 的表）：七种形态都走完"探测 → 保存 → 真实一轮对话"，核对每个请求到达的路径、带密钥的请求头、走哪种协议、Claude 是否带缓存断点。写这组测试时又查出两处：探测结果里请求头的取值文档写成了 `x-api-key`，代码和 models.toml 一直是 `x_api_key`（已统一，models.toml 两种写法都认，桌面端显示真实的请求头名）；服务用纯文本回 401 时"密钥被拒"后面是空的（已改为带上原话）。
- **自动测试**：受影响的 7 个 crate（her-llm、her-core、her-server、her-cli、her-protocol、her-mock、her-connections）61 组 1,050 个测试全部通过，只跳过下面列的 4 个原有失败。其中 her-llm 含探测 10 种形态、规范模型、名字规整、列表字段、协议选择；her-cli 的 `e2e_custom_endpoints`（JSON-RPC 全流程：探测、`models/list`、Claude 走 `/v1/messages` 并带缓存断点、`models/configure`、改名、移除；命令行 `login --base-url`、`models set`、`her models` 的 `like` 标注、`logout --remove`）和 `e2e_login` 通过；桌面端、客户端类型检查通过，客户端单测 129 个通过。
- **桌面端**（网页预览，模拟内核）：自定义服务一块、表单、连不上的报错、成功卡片、列表里的自定义服务行（代码图标、主机名、管理模型、移除）、管理模型里的"参照……"、"设置参数"的候选、"设置密钥"（错密钥显示服务原话，对的保存并提示）。
- **真实服务**（临时 `HER_HOME`，不计费）：
  - Vercel AI Gateway 不带密钥：探到 Chat Completions、Messages、Responses 三种协议和 317 个模型，316 个认出参数；它先校验请求体，所以不带密钥也能接上（§4.4）。
  - OpenRouter：不带密钥报"需要 API Key"，假密钥报"密钥被拒"并附原话。第一次实测报的是"没找到协议"，原因是列表公开时跳过了鉴权判断，已改并加测试。
  - Groq 只填主机名、不带密钥：报"需要 API Key"。`example.com`：报"不像模型服务"。
  - 百炼 Token Plan 假密钥：报"密钥被拒：Invalid API-key provided."。第一次报的是 `No API-key provided.`（最后一种请求头的回答），已改为第一种请求头的原话（§4.5）。
- **已知的、与本方案无关的失败**（origin/main 的 Word、PDF 改动带来的）：`cli_shape` 缺 `HER_WORD_SAVE` 的登记、工具快照里 Word 工具的描述变了、`e2e_sheets` 的审批次数、`docx_host` 的测试等不到宿主请求而一直挂着、pdfium 测试要本机装 PDF 支持、桌面端 e2e 脚本清单少了 `word-drag`。

### 17.4 还没做

- 用有效密钥的真实对话（Token Plan 密钥要换新），以及 Ollama、LM Studio、vLLM 的本机接入（本机没有装）。
- Linux 上的启动对比和缺页预算（ratchet 的 Linux 部分要 `dmit-us` 主机）；打包的桌面应用里走一遍（这次在网页预览里用模拟内核验的界面，内核另由端到端测试验）。

## 附录 A. 识别率的测法

- **数据**（2026-11-09 下载）：公共目录完整文件 `https://models.dev/api.json`（235 家、8,934 行）；LiteLLM 的 `model_prices_and_context_window.json`（4,871 条，其中对话模型 3,457 个，排除 `ft:` 开头的微调名）。
- **网关型渠道，排除被测的那一家**：对每个被测的渠道，用其余 234 家建别名表和厂商行，查被测渠道的每个对话模型。"对到正确的规范模型"与被测行自己的 `canonical_model_id` 比较，日期版本算同一个模型。参数与被测行自己的值比较。
- **LiteLLM**：去掉 LiteLLM 自己的路由前缀（`fireworks_ai/`、`bedrock/us-west-2/`……），按网关上会出现的上游叫法查，参数与它的 `max_input_tokens`、`supports_reasoning`、`supports_vision` 比较。
- **规范模型的参数**：§6.6 的表里，有模型厂商自己的行就用它，否则取各行的多数和较小的中位数（§6.2 的第 3 条）。改用实验室级文件的条目（§6.2 的第 2 条）另测过一次：精确匹配加去符号比较的 1,183 个模型里，上下文一致 86%，取多数是 85%；偏大的 98 个对 90 个，偏小的 70 个对 94 个；推理、图片持平。
- **脚本**：`tools/catalog/alias_eval.py`，用法写在文件开头。实现时改成 `cargo run -p her-llm --example alias_report`（§13.3），每周同步后复测。
