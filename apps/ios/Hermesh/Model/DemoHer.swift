#if DEBUG
import Foundation
import HermeshHer
import HermeshProto
import SwiftProtobuf

/// A conversation with Her in the demo, for a debug build launched with -HerDemo <turns> (HerScrollUITests, and to
/// feel how the conversation scrolls by hand): the Mac mini is one of her environments, and this iPhone keeps what was
/// said there, as the core does for a real one. What is written to her waits here, as it would. The demo itself has
/// no Her.
extension DemoService {
    /// Launched with -HerDemo (its number of turns; 0, a conversation with nothing said yet).
    static var herDemoOn: Bool { UserDefaults.standard.object(forKey: "HerDemo") != nil }
    static var herDemoTurns: Int { UserDefaults.standard.integer(forKey: "HerDemo") }
    /// Launched with -HerDemoMarkdown YES as well: her replies are DemoMarkdown.swift's, one to a turn.
    static var herDemoMarkdown: Bool { UserDefaults.standard.bool(forKey: "HerDemoMarkdown") }

    /// The machine as one of Her's environments, where she runs with a model. Nothing answers her connections in the
    /// demo, so what was said is what this iPhone keeps, and what is written waits here.
    static func herDemo(_ machine: inout Hermesh_Local_V1_Machine) {
        machine.her = .with { $0.enabled = true; $0.running = true; $0.modelReady = true }
    }

    /// Her's calls of the core's, answered from what the demo keeps; nil for any other.
    func herDemo(_ method: String, _ r: [String: Any]) throws -> Data? {
        switch method {
        case "GetHerConversation":
            return try Hermesh_Local_V1_GetHerConversationResponse.with {
                $0.bot = JSON.object(["id": "her", "name": "Her", "default": true, "session_id": "her-demo"]).data()
                $0.snapshot = herDemoSnapshot(turns: Self.herDemoTurns).data()
            }.jsonUTF8Data()
        case "ListHerWaiting":
            return try Hermesh_Local_V1_ListHerWaitingResponse.with { $0.waiting = herWaiting }.jsonUTF8Data()
        case "HerSend":
            let waiting = Hermesh_Local_V1_HerWaiting.with {
                $0.id = r["id"] as? String ?? UUID().uuidString; $0.machineID = r["machineId"] as? String ?? ""
                $0.botID = r["botId"] as? String ?? ""; $0.text = r["text"] as? String ?? ""; $0.createdAt = .init(date: .now)
            }
            herWaiting.append(waiting)
            return try Hermesh_Local_V1_HerSendResponse.with { $0.waiting = waiting }.jsonUTF8Data()
        case "DeleteHerWaiting":
            herWaiting.removeAll { $0.id == r["id"] as? String }
            return try Hermesh_Local_V1_DeleteHerWaitingResponse().jsonUTF8Data()
        default:
            return nil
        }
    }
}

/// The conversation as bots/open gives it: `turns` of the owner's message and what Her sent for it, of every kind
/// the conversation draws (words short and long, lists, code, tables, a question, files, a session she started,
/// the team's work, a scheduled task), the last a moment ago.
private func herDemoSnapshot(turns: Int) -> JSON {
    var items: [JSON] = []
    let end = Date.now.timeIntervalSince1970 * 1000
    for i in 0 ..< turns {
        // A turn every seven minutes, and a pause of some hours every tenth, where a time line shows.
        let ts = end - Double(turns - i) * 420_000 - Double((turns - i) / 10) * 14_400_000
        let gallery = DemoService.herDemoMarkdown ? herMarkdownGallery[i % herMarkdownGallery.count] : nil
        let reply = gallery.map { (tool: $0.tool, args: $0.args, details: $0.details) } ?? herDemoReplies[i % herDemoReplies.count]
        let ask = gallery?.ask ?? herDemoAsks[i % herDemoAsks.count]
        items.append(["type": "message", "message_id": .string("u\(i)"),
                      "message": ["role": "user", "ts": .number(ts), "content": [["type": "text", "text": .string("\(ask) #\(i + 1)")]]]])
        items.append(["type": "message", "message_id": .string("a\(i)"),
                      "message": ["role": "assistant", "ts": .number(ts + 1000), "duration_ms": 4000,
                                  "content": [["type": "tool_call", "id": .string("c\(i)"), "name": .string(reply.tool), "arguments": reply.args]]]])
        items.append(["type": "tool_result", "call_id": .string("c\(i)"), "result": ["tool_name": .string(reply.tool), "details": reply.details]])
        items.append(["type": "task_end", "outcome": "completed", "ts": .number(ts + 6000)])
    }
    return ["session": ["session_id": "her-demo", "model": "demo/demo-model"], "last_seq": .number(Double(items.count)), "transcript": ["items": .array(items)]]
}

private let herDemoAsks = [
    "帮我看看今天有哪些任务在跑", "这个报错是什么原因？", "好的", "把登录页的改动总结一下，顺便说说还有哪些风险点需要我在发版前确认，尤其是和老版本客户端兼容相关的部分",
    "Can you compare the two approaches?", "继续", "写个脚本批量重命名这些文件", "明天早上九点提醒我看一下发布结果",
]

private func herDemoWords(_ text: String) -> (tool: String, args: JSON, details: JSON) { (sendToUser, ["text": .string(text)], .null) }

private let herDemoReplies: [(tool: String, args: JSON, details: JSON)] = [
    herDemoWords("好的，我来看一下。"),
    herDemoWords("""
    现在有 **3 个会话**在运行：

    1. `hermesh/welcome`：Codex 正在跑测试，已经改了 4 个文件
    2. `api/rate-limit`：Claude 在等你回答一个问题
    3. `docs/guide`：刚开始，还在读代码

    需要我先处理哪一个？详情见 https://example.com/sessions 。
    """),
    herDemoWords("""
    这个报错来自连接重试：超时之后没有重置计数器，所以第二次重连会立刻失败。

    ```swift
    func pause(_ failures: Int) async {
        let seconds = min(30, 1 << min(5, max(0, failures - 1)))
        try? await Task.sleep(for: .seconds(seconds))
    }
    ```

    把 `failures = 0` 放到连接成功之后就可以了。下面这条命令可以复现：

    ```bash
    HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testHerSending go test ./e2e -run 'TestIPhone$' -v -timeout 40m -count=1
    ```
    """),
    herDemoWords("""
    ## 两种方案的对比

    | 方案 | 首屏耗时 | 内存 | 改动量 | 风险 |
    | --- | ---: | ---: | --- | --- |
    | 全量渲染 | 420 ms | 180 MB | 无 | 长对话卡顿 |
    | 懒加载 | 60 ms | 45 MB | 中等 | 滚动定位要重新处理 |
    | 分页 + 懒加载 | 35 ms | 30 MB | 较大 | 需要服务端配合 |

    > 我的建议是先做懒加载，分页已经有了，只是窗口偏大。

    后续步骤：

    - [x] 量一下现在的帧率
    - [ ] 换成懒加载
    - [ ] 在真机上再量一次
    """),
    (sendToUser, ["type": "question", "text": "要现在就开始改吗？", "options": [["label": "现在开始"], ["label": "先等等"]]], .null),
    herDemoWords("""
    The two approaches differ mainly in **where the state lives**. With the first, every row reads the clock, so each \
    tick lays the whole conversation out again; with the second only the rows that show a time do. For a conversation \
    of a hundred messages that is the difference between a hitch every other second and none at all.

    A few things to keep in mind:

    - Rows keep their identity by the message's key, so nothing is rebuilt when earlier history is put above.
    - *Markdown is parsed once* and kept by its text.
    - Wide tables and code scroll sideways by themselves.
    """),
    (sendToUser, ["type": "attachment", "text": "脚本和说明都在这里。", "files": [["path": "rename.sh"], ["path": "README.md"]]],
     ["files": [["path": "out/rename.sh", "size": 1840], ["path": "out/README.md", "size": 5320]]]),
    (sessionStart, ["title": "批量重命名脚本"], ["session": ["session_id": "demo-finished-session", "title": "批量重命名脚本", "agent": "codex", "machine": "Linux Studio"]]),
    (workSummary, [:], ["work": ["days": 1, "sessions": 12, "done": 9, "turns": 41, "input_tokens": 182_000, "output_tokens": 23_500,
                                 "buckets": [0, 0, 1, 3, 2, 0, 4, 1, 0, 2, 5, 3], "by_agent": [["agent": "codex", "sessions": 7], ["agent": "claude", "sessions": 5]],
                                 "by_client": [["client": "demo-client", "sessions": 12]]]]),
    (manageRoutine, ["action": "create"], ["action": "create", "routine_id": "r1", "name": "看发布结果", "when": "每天 09:00"]),
    herDemoWords("改好了，一共三处：登录页的按钮文案、超时后的提示，还有一个旧版客户端的兼容分支。发版前建议你确认两点：一是 1.4 之前的客户端收到新字段会不会报错，二是灰度期间两种文案同时存在是否可以接受。测试我都跑过了，全部通过。"),
]
#endif
