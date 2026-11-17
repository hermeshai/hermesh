#if DEBUG
import HermeshHer

/// Replies of Her's in the demo that put the Markdown renderer to the test, for a debug build launched with
/// -HerDemoMarkdown YES (MarkdownUITests): tables of every shape, nested lists and quotes, code, math, diagrams and
/// what an agent writes that Markdown leaves alone. Each is one turn, so each can be looked at by itself.
let herMarkdownGallery: [(ask: String, tool: String, args: JSON, details: JSON)] = herMarkdownCases.map { ($0.ask, sendToUser, ["text": .string($0.text)], .null) }

private let herMarkdownCases: [(ask: String, text: String)] = [
    ("表格：两列，第二列很长", """
    有，最贴近你描述的是 **Fluxmail**：把已有的多种邮箱接起来，直接提供 MCP，不需要复杂邮件客户端。

    | 产品 | 接入与 MCP |
    | --- | --- |
    | [Fluxmail](https://fluxmail.ai) | Gmail、Outlook、iCloud 及通用 IMAP/SMTP；原生 MCP，接入后 Agent 可以直接读写邮件 |
    | [Nylas Connect](https://www.nylas.com) | 多供应商统一邮件接口，或自托管邮件 webhook |
    | [Composio](https://composio.dev) | 已核实 Gmail、Outlook，代管 OAuth，也能连接其他上百种工具 |
    | [EmailEngine](https://emailengine.app) | Gmail、Microsoft Graph 与 IMAP；官方 MCP 仍为 beta，有自托管成本 |
    | [AgentMail](https://agentmail.to) | 给 Agent 新建邮箱，官方 MCP，不是聚合现有邮箱 |

    我的建议：先试 Fluxmail；如果需要收到邮件后主动唤醒 Agent，再重点评估 Nylas。
    """),
    ("Table: wide, aligned numbers", """
    | Region | Q1 | Q2 | Q3 | Q4 | YoY | Status |
    | :--- | ---: | ---: | ---: | ---: | :---: | :--- |
    | North America | 1,204,500 | 1,388,020 | 1,402,910 | 1,610,330 | +18.4% | On track |
    | Europe | 880,100 | 912,430 | 870,010 | 1,002,880 | +6.1% | Watch |
    | Asia Pacific | 640,000 | 702,550 | 811,940 | 930,120 | +31.0% | Ahead |
    | Latin America | 98,400 | 101,220 | 99,870 | 120,010 | −2.3% | Behind |
    | **Total** | **2,823,000** | **3,104,220** | **3,184,730** | **3,663,340** | **+16.2%** | |
    """),
    ("表格：单元格里的各种格式", """
    | 项 | 写法 | 说明 |
    | --- | :---: | --- |
    | 行内代码 | `git rebase -i HEAD~3` | 代码里有竖线也要转义：`a \\| b` |
    | 强调 | **粗体**、*斜体*、~~删除线~~ | ***粗斜体*** 混在中文里 |
    | 链接 | [文档](https://example.com/docs) | 裸链接 https://example.com/a/very/long/path?with=query&and=more 之后是中文 |
    | 公式 | $E = mc^2$ | 行内 $\\sum_{i=1}^{n} x_i$ 与文字同一行 |
    | 表情 | ✅ ⚠️ ❌ | 🚀 发布、🐛 修复 |
    | 空的 |  | 中间一格是空的 |
    | 换行 | 第一行<br>第二行 | HTML 换行原样显示 |
    | 少一格 | 只有两格 |
    | 多一格 | 一 | 二 | 三（多出来的） |
    """),
    ("Table: edge shapes", """
    A table of one column:

    | Steps |
    | --- |
    | Clone the repository |
    | Run `make setup` |

    A table with only its header:

    | Name | Owner | Due |
    | --- | --- | --- |

    A cell with nothing to break on:

    | Key | Value |
    | --- | --- |
    | digest | sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08 |
    | url | https://storage.example.com/buckets/hermesh-artifacts/releases/2026-11-17/hermesh-ios-universal.ipa |

    A table two words wide:

    | A | B |
    | - | - |
    | 1 | 2 |
    """),
    ("列表：多层嵌套", """
    ## 发布前检查

    1. 准备工作
       - 拉取最新 `main`
       - 确认 CI 全绿
         - 单元测试
         - 端到端测试：iPhone、Mac、Linux 三个平台都要跑一遍，失败的用例要单独记录原因
    2. 打包
       1. 生成版本号
       2. 签名

          ```bash
          ./scripts/testflight.sh --build 214
          ```

       3. 上传
    3. 发布后
       > 观察 24 小时崩溃率，超过 0.5% 就回滚。

       - [x] 通知团队
       - [ ] 更新文档
         - [ ] 中文
         - [x] English

    7. 从 7 开始的编号
    8. 下一项

    - 列表里的表格：

      | 平台 | 状态 |
      | --- | --- |
      | iOS | 通过 |
      | Mac | 失败 |
    """),
    ("Quotes and headings", """
    # Heading one
    ## Heading two
    ### Heading three
    #### Heading four
    ##### Heading five
    ###### Heading six

    A line ending in two spaces
    breaks here, and this one
    breaks on its single newline.

    ---

    > **Note**
    > A quote with a list:
    > - first
    > - second
    >
    > > A quote inside a quote, with `code` and a [link](https://example.com).
    >
    > ```json
    > { "retry": 3, "backoff": "exponential" }
    > ```
    """),
    ("代码块", """
    ```swift
    func retry<T>(times: Int = 3, delay: Duration = .seconds(1), _ body: () async throws -> T) async throws -> T { for attempt in 1...times { do { return try await body() } catch where attempt < times { try await Task.sleep(for: delay) } }; fatalError() }
    ```

    ```diff
    - let seconds = min(30, 1 << failures)
    + let seconds = min(30, 1 << min(5, max(0, failures - 1)))
    ```

    ```
    没有语言的代码块
        缩进保留
    ```

    ````markdown
    ```python
    print("fences inside fences")
    ```
    ````
    """),
    ("公式、图和其它", """
    行内公式 $a^2 + b^2 = c^2$，以及独立公式：

    $$
    \\int_0^\\infty e^{-x^2} \\, dx = \\frac{\\sqrt{\\pi}}{2}
    $$

    ```mermaid
    graph LR
      A[收到邮件] --> B{需要回复?}
      B -- 是 --> C[唤醒 Agent]
      B -- 否 --> D[归档]
    ```

    ![架构图](https://example.com/arch.png)

    <details>
    <summary>HTML 块</summary>
    原样显示
    </details>

    脚注[^1]和转义字符 \\*不是斜体\\*。A second note[^why], and the first again[^1].

    [^1]: 这是脚注的内容。
    [^why]: A note with **bold**, `code` and a [link](https://example.com).
        Its second line, indented under it.
    """),
]
#endif
