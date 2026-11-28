#if targetEnvironment(simulator)
import CryptoKit
import Foundation
import HermeshHer
import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftProtobuf
import UIKit

/// Large-scale realistic stress dataset (`-DemoPerfSuite`) covering sessions, nested Her trees, 80-turn rich Markdown
/// conversations, 240-step tool cards, live 25Hz token streaming, 1,500-line multi-file diffs, 1,200-file trees,
/// 3,000-line source files, and interactive HTML Demo previews.
extension DemoService {
    func seedPerfSuite() {
        let now = Date.now

        // 1. Expand machines, projects, and grants to a heavy multi-environment owner workspace.
        var mbp = machine("perf-mbp", "MacBook Pro M4 Max", "darwin/arm64")
        mbp.local = true
        mbp.status.maxSessions = 12
        mbp.status.agents = ["codex", "claude", "antigravity", "gemini"].map { a in
            .with {
                $0.name = a; $0.available = true; $0.loggedIn = true; $0.version = "2.4.0"
                $0.models = [
                    .with { $0.id = "\(a)-pro"; $0.name = "\(a.capitalized) Pro"; $0.efforts = ["low", "medium", "high"] },
                    .with { $0.id = "\(a)-fast"; $0.name = "\(a.capitalized) Fast"; $0.efforts = ["low", "medium"] },
                ]
            }
        }
        var cloud = machine("perf-cloud", "AWS Build Cluster", "linux/arm64")
        cloud.status.maxSessions = 16
        world.machines.append(contentsOf: [mbp, cloud])

        let projectDefs: [(String, String, String)] = [
            ("demo-mac", "hermesh", "/Users/owner/Projects/hermesh"),
            ("demo-mac", "ios-client", "/Users/owner/Projects/ios-client"),
            ("perf-mbp", "desktop-app", "/Users/owner/Projects/desktop-app"),
            ("perf-mbp", "ml-pipeline", "/Users/owner/Projects/ml-pipeline"),
            ("perf-cloud", "infra-cdk", "/srv/work/infra-cdk"),
            ("demo-linux", "search-index", "/home/dev/search-index"),
        ]
        for (mid, pid, path) in projectDefs {
            world.projects.append(.with {
                $0.machineID = mid; $0.projectID = pid; $0.path = path
                $0.defaultBaseRef = "main"; $0.workspace = .worktree; $0.repo = true
                $0.agents = ["codex", "claude", "antigravity", "gemini"]
                $0.description_p = "Stress fixture repository \(pid)"
            })
        }

        let extraClients: [(String, String, String)] = [
            ("c-her", "Her", "demo-mac"),
            ("c-claude", "Claude Desktop", ""),
            ("c-cursor", "Cursor Agent", ""),
            ("c-ci", "Nightly Bot", ""),
        ]
        for (cid, label, herMachine) in extraClients where !world.clients.contains(where: { $0.clientID == cid }) {
            world.clients.append(.with {
                $0.clientID = cid; $0.grantID = "grant-\(cid)"; $0.label = label; $0.online = true
                $0.herMachineID = herMachine; $0.grantVersion = 1
                $0.expiresAt = .init(date: now.addingTimeInterval(21 * 86400))
                $0.spec = .with { $0.label = label; $0.machineIds = ["*"]; $0.projectIds = ["*"]; $0.agents = ["*"]; $0.ttlDays = 30 }
            })
        }

        // 2. Seed Pinned Core Stress Sessions at top of Overview & Sidebar.
        seedMegaSession(now: now)
        seedStreamingSession(now: now)
        seedActionPanelsSession(now: now)

        // 3. Seed 7 additional concurrent running sessions (total 8 running for Overview LinkMap + Shimmer + Orbit stress).
        let runningSpecs: [(String, String, String, String, String, String)] = [
            ("perf-run-1", "Refactor WebSocket multiplex framing", "perf-mbp", "desktop-app", "antigravity", "Bash · cargo test --workspace"),
            ("perf-run-2", "Optimize SQLite WAL checkpoint cadence", "demo-mac", "hermesh", "codex", "Edit · internal/owner/store.go"),
            ("perf-run-3", "Train quantization calibration split", "perf-cloud", "ml-pipeline", "claude", "Bash · python3 evaluate.py --fp8"),
            ("perf-run-4", "Audit SwiftUI scroll hitch regressions", "demo-mac", "ios-client", "antigravity", "Grep · LazyVStack"),
            ("perf-run-5", "Synthesize multi-region CDK stacks", "perf-cloud", "infra-cdk", "codex", "Bash · npx cdk synth --quiet"),
            ("perf-run-6", "Rebuild Tantivy incremental segments", "demo-linux", "search-index", "gemini", "Read · src/indexer/merge.rs"),
            ("perf-run-7", "Verify APNs push extension encryption", "demo-mac", "ios-client", "claude", "Bash · swift test --parallel"),
        ]
        for (idx, sp) in runningSpecs.enumerated() {
            var s = session(sp.0, title: sp.1, machine: sp.2, status: .running)
            s.projectID = sp.3
            s.agent = sp.4
            s.createdBy = idx.isMultiple(of: 2) ? "owner" : "c-her"
            s.activeRunID = "r1"
            let started = now.addingTimeInterval(Double(-95 - idx * 47))
            s.createdAt = .init(date: started)
            s.updatedAt = .init(date: now.addingTimeInterval(Double(-idx * 3)))
            s.progress.turnStartedAt = .init(date: started)
            s.progress.currentTool = sp.5
            world.sessions.append(s)
            beginRun(sp.0, runID: "r1", ordinal: 1, asker: s.createdBy, status: .running, startedAt: started)
            appendItem(sp.0, runID: "r1", kind: .userMessage) { $0.text = sp.1 }
            appendItem(sp.0, runID: "r1", kind: .commandExecution) { $0.command = sp.5; $0.status = "running" }
        }

        // 4. Seed 3 Her parent conversations with 24 nested child coding sessions (`parentSessionID`).
        for h in 0 ..< 3 {
            let parentID = "perf-her-parent-\(h)"
            var parent = session(parentID, title: "Her Sprint Coordinator #\(h + 1)", machine: "demo-mac", status: .idle)
            parent.agent = "her"
            parent.projectID = "__her__"
            parent.createdBy = "owner"
            parent.updatedAt = .init(date: now.addingTimeInterval(Double(-180 - h * 900)))
            parent.progress.lastMessageExcerpt = "Coordinated 8 sub-tasks across desktop, iOS, and backend services."
            world.sessions.append(parent)
            syncSession(parentID)

            for c in 0 ..< 8 {
                let cid = "perf-her-child-\(h)-\(c)"
                let proj = projectDefs[(h * 8 + c) % projectDefs.count]
                var child = session(cid, title: "Subtask \(h + 1).\(c + 1): Implement \(proj.1) module check", machine: proj.0, status: .idle)
                child.projectID = proj.1
                child.agent = ["codex", "claude", "antigravity", "gemini"][c % 4]
                child.createdBy = "c-her"
                child.lineage = .with { $0.relationship = herAgent; $0.parentSessionID = parentID }
                child.updatedAt = .init(date: now.addingTimeInterval(Double(-220 - h * 900 - c * 40)))
                child.progress.diffStat = .with { $0.filesChanged = UInt32(2 + c); $0.insertions = UInt32(34 + c * 17); $0.deletions = UInt32(8 + c * 5) }
                world.sessions.append(child)
                syncSession(cid)
            }
        }

        // 5. Seed 220 historical + failed + archived sessions (total ~260 sessions) for Sidebar, TasksView & SearchSheet stress.
        let topics = [
            "Fix iPhone Duo side panel spring animation",
            "Add incremental SQLite WAL snapshot compaction",
            "Migrate renderer virtualized list row measurement",
            "Implement Shiki syntax token LRU cache",
            "Benchmark WebSocket binary protobuf codec",
            "Harden OAuth PKCE token refresh race condition",
            "Build interactive HTML canvas flamegraph preview",
            "Reduce cold launch CoreAnimation offscreen passes",
            "Support Mermaid sequence diagram pan and zoom",
            "Streamline background daemon log tailing buffer",
        ]
        let agents = ["codex", "claude", "antigravity", "gemini", "deepseek", "opencode"]
        for i in 0 ..< 220 {
            let id = "perf-hist-\(i)"
            let proj = projectDefs[i % projectDefs.count]
            let isArchived = i >= 180
            let isFailed = !isArchived && (i % 29 == 7)
            let status: SessionStatus = isArchived ? .archived : (isFailed ? .crashed : .idle)
            let topic = topics[i % topics.count]
            var s = session(id, title: "\(topic) (#\(i + 1))", machine: proj.0, status: status)
            s.projectID = proj.1
            s.agent = agents[i % agents.count]
            s.createdBy = ["owner", "demo-client", "c-claude", "c-cursor"][i % 4]
            s.branch = "feat/\(proj.1)-\(i + 1)"
            let age: TimeInterval = Double(300 + i * 2_400)
            s.createdAt = .init(date: now.addingTimeInterval(-age - 600))
            s.updatedAt = .init(date: now.addingTimeInterval(-age))
            s.progress.turnCount = UInt32(2 + (i % 14))
            s.progress.lastMessageExcerpt = "Completed verification for \(topic.lowercased()) in \(proj.1) with full regression coverage."
            if isFailed {
                s.error = "Provider rate limit exceeded while streaming turn \(s.progress.turnCount)"
                s.progress.lastFailure = .with { $0.kind = .rateLimit; $0.message = s.error; $0.retryable = true }
            } else {
                s.progress.diffStat = .with {
                    $0.filesChanged = UInt32(1 + (i % 12))
                    $0.insertions = UInt32(15 + (i * 23) % 420)
                    $0.deletions = UInt32(4 + (i * 11) % 160)
                }
                s.gitStatus = .with {
                    switch i % 5 {
                    case 0: $0.state = .merged
                    case 1: $0.state = .pushed
                    case 2: $0.state = .unpushed; $0.commitsAhead = 2
                    case 3: $0.state = .dirty; $0.dirtyFiles = 3
                    default: $0.state = .clean
                    }
                    $0.branch = s.branch
                    $0.targetRef = "main"
                    $0.headCommit = String(format: "%07x", 0x10a4f0 + i)
                    $0.headSubject = topic
                }
            }
            world.sessions.append(s)
            syncSession(id)
        }
    }

    private func seedMegaSession(now: Date) {
        let id = "perf-mega-session"
        var s = session(id, title: "Mega 80-Turn Architecture", machine: "demo-mac", status: .idle)
        s.projectID = "hermesh"
        s.agent = "antigravity"
        s.createdBy = "owner"
        s.pinned = true
        s.branch = "perf/unified-rendering-pipeline"
        s.createdAt = .init(date: now.addingTimeInterval(-14_400))
        s.updatedAt = .init(date: now.addingTimeInterval(120))
        s.progress.turnCount = 80
        s.progress.diffStat = .with { $0.filesChanged = 12; $0.insertions = 984; $0.deletions = 312 }
        s.activeBackgroundCount = 1
        s.activeDaemonCount = 1
        s.queuedRunCount = 2
        s.gitStatus = .with {
            $0.state = .unpushed; $0.branch = s.branch; $0.targetRef = "main"; $0.commitsAhead = 4
            $0.headCommit = "9f3c21a"; $0.headSubject = "perf(ios): lazy conversation & zero-alloc row layout"
        }
        world.sessions.insert(s, at: 0)

        let totalTurns = 80
        for t in 1 ... totalTurns {
            let runID = "r\(t)"
            // Include multi-hour pauses every 8 turns so TurnTimeLine separators are exercised.
            let turnStart = now.addingTimeInterval(Double(-(totalTurns - t) * 120 - ((totalTurns - t) / 8) * 10_800))
            beginRun(id, runID: runID, ordinal: UInt32(t), asker: "owner", status: .completed, startedAt: turnStart)
            appendItem(id, runID: runID, kind: .userMessage) {
                $0.actor = .with { $0.kind = .owner; $0.id = "owner" }
                $0.createdAt = .init(date: turnStart)
                $0.text = "Turn #\(t): Analyze subsystem \(t) rendering latency, inspect the hot paths in `src/core/Engine.ts`, and propose concrete improvements."
            }

            if t == totalTurns {
                // Final turn has a massive 240-step folded ToolGroup + rich summary with links to HTML demo, 3000-line TS, Markdown & PNGs!
                for step in 0 ..< 240 {
                    let mod = step % 4
                    if mod == 0 {
                        appendItem(id, runID: runID, kind: .toolCall) {
                            $0.itemID = "mega-tool-\(step)"
                            $0.toolName = "Read"
                            $0.filePath = "packages/generated/module_\(String(format: "%04d", (step % 1200) + 1)).ts"
                            $0.input = "{\n  \"path\": \"\($0.filePath)\"\n}"
                            $0.output = "export function computeMetric\(step)(input: number): number { return input * 1.618; }"
                        }
                    } else if mod == 1 {
                        appendItem(id, runID: runID, kind: .toolCall) {
                            $0.itemID = "mega-tool-\(step)"
                            $0.toolName = "Grep"
                            $0.text = "TimelineView"
                            $0.input = "{\n  \"pattern\": \"TimelineView\"\n}"
                            $0.output = "Views/Components/Components.swift:109: TimelineView(.animation)\nViews/Components/Components.swift:273: TimelineView(.animation)"
                        }
                    } else if mod == 2 {
                        appendItem(id, runID: runID, kind: .fileChange) {
                            $0.itemID = "mega-tool-\(step)"
                            $0.toolName = "Edit"
                            $0.filePath = "src/core/Engine.ts"
                            $0.input = "{\n  \"path\": \"src/core/Engine.ts\"\n}"
                            $0.diff = "@@ -42,4 +42,5 @@\n-  const eager = rows.map(renderRow);\n+  const visible = windowSlice(rows, viewport);\n+  const rendered = visible.map(renderRow);"
                        }
                    } else {
                        appendItem(id, runID: runID, kind: .commandExecution) {
                            $0.itemID = "mega-tool-\(step)"
                            $0.toolName = "Bash"
                            $0.command = "pnpm --filter @hermesh/core test --grep suite_\(step)"
                            $0.input = "{\n  \"command\": \"\($0.command)\"\n}"
                            $0.exitCode = 0
                            $0.output = "\u{001B}[32m✓\u{001B}[0m suite_\(step) passed (14 assertions in 18ms)\nMemory delta: +0.2 MB"
                        }
                    }
                }
                appendItem(id, runID: runID, kind: .assistantMessage) {
                    $0.text = Self.megaFinalReplyMarkdown
                }
            } else {
                // Regular rich turn: 6 tool steps + rich Markdown (tables, code blocks, Mermaid or images on periodic turns).
                for step in 0 ..< 6 {
                    let isCmd = step == 5
                    appendItem(id, runID: runID, kind: isCmd ? .commandExecution : .toolCall) {
                        $0.toolName = isCmd ? "Bash" : (step.isMultiple(of: 2) ? "Read" : "Grep")
                        if isCmd {
                            $0.command = "go test ./internal/pkg\(t % 16)/... -count=1"
                            $0.input = "{\n  \"command\": \"\($0.command)\"\n}"
                            $0.exitCode = 0
                            $0.output = "ok  \therm/internal/pkg\(t % 16)\t0.\(12 + (t % 80))s"
                        } else if step.isMultiple(of: 2) {
                            $0.filePath = "src/core/Engine.ts"
                            $0.input = "{\n  \"path\": \"src/core/Engine.ts\"\n}"
                            $0.output = "// Read section \(t)"
                        } else {
                            $0.text = "dispatchBatch_\(t)"
                            $0.input = "{\n  \"pattern\": \"dispatchBatch_\(t)\"\n}"
                            $0.output = "src/core/Engine.ts:\(t * 25): function dispatchBatch_\(t)()"
                        }
                    }
                }
                appendItem(id, runID: runID, kind: .assistantMessage) {
                    $0.text = Self.richTurnMarkdown(turn: t)
                }
            }
        }

        // Attach active + completed background tasks and queued runs to perf-mega-session.
        var p = projections[id, default: .init()]
        p.backgroundTasks = [
            .with {
                $0.taskID = "bg-dev-server"; $0.sessionID = id; $0.runID = "r80"
                $0.kind = .command; $0.status = .running; $0.awaited = false
                $0.title = "Interactive Demo Vite Server"; $0.command = "pnpm --filter @hermesh/demo dev --port 4173"
                $0.startedAt = .init(date: now.addingTimeInterval(-420))
                $0.outputTail = (1 ... 45).map { "[\($0)s] HMR bundle ready · 124 modules transformed in 34ms" }.joined(separator: "\n")
            },
            .with {
                $0.taskID = "bg-profiler"; $0.sessionID = id; $0.runID = "r80"
                $0.kind = .subagent; $0.status = .running; $0.awaited = true
                $0.title = "Frame Hitch Regression Verifier"; $0.command = "xcrun xctrace analyze --template AnimationHitches"
                $0.startedAt = .init(date: now.addingTimeInterval(-68))
                $0.outputTail = (1 ... 30).map { "Sample window #\($0): p95 frame time 8.2ms, 0 severe hitches" }.joined(separator: "\n")
            },
            .with {
                $0.taskID = "bg-unit-1"; $0.sessionID = id; $0.runID = "r78"
                $0.kind = .command; $0.status = .completed; $0.awaited = true
                $0.title = "HermeshKit Unit Suite"; $0.command = "swift test --package-path packages/HermeshKit"
                $0.startedAt = .init(date: now.addingTimeInterval(-600))
                $0.completedAt = .init(date: now.addingTimeInterval(-565))
                $0.outputTail = "Test Suite 'All tests' passed.\nExecuted 86 tests, with 0 failures in 4.82 seconds."
            },
        ]
        for q in 1 ... 2 {
            p.runs.append(.with {
                $0.runID = "q-mega-\(q)"; $0.sessionID = id; $0.status = .queued
                $0.queueOrder = Double(1000 * q)
                $0.author = .with { $0.kind = .owner; $0.id = "owner" }
                $0.prompt = q == 1
                    ? "Run the 120Hz scroll benchmark again after switching VStack to LazyVStack"
                    : "Export before/after CPU and memory tables to docs/Architecture.md"
                $0.createdAt = .init(date: now.addingTimeInterval(Double(-20 + q * 5)))
            })
        }
        p.session = world.sessions[0]
        projections[id] = p
    }

    private func seedStreamingSession(now: Date) {
        let id = "perf-streaming-session"
        var s = session(id, title: "Live Streaming Stress", machine: "perf-mbp", status: .running)
        s.projectID = "desktop-app"
        s.agent = "codex"
        s.createdBy = "owner"
        s.pinned = true
        s.activeRunID = "r36"
        s.createdAt = .init(date: now.addingTimeInterval(-3_600))
        s.updatedAt = .init(date: now.addingTimeInterval(110))
        s.progress.turnCount = 35
        s.progress.turnStartedAt = .init(date: now.addingTimeInterval(-12))
        s.progress.currentTool = "Replying…"
        world.sessions.insert(s, at: 1)

        for t in 1 ... 35 {
            let rid = "r\(t)"
            beginRun(id, runID: rid, ordinal: UInt32(t), asker: "owner", status: .completed, startedAt: now.addingTimeInterval(Double(-(36 - t) * 90)))
            appendItem(id, runID: rid, kind: .userMessage) {
                $0.actor = .with { $0.kind = .owner; $0.id = "owner" }
                $0.text = "Streaming turn #\(t): inspect frame pacing and memory allocations."
            }
            appendItem(id, runID: rid, kind: .assistantMessage) {
                $0.text = Self.richTurnMarkdown(turn: t)
            }
        }

        beginRun(id, runID: "r36", ordinal: 36, asker: "owner", status: .running, startedAt: now.addingTimeInterval(-12))
        appendItem(id, runID: "r36", kind: .userMessage) {
            $0.actor = .with { $0.kind = .owner; $0.id = "owner" }
            $0.text = "Stream a comprehensive optimization report with code blocks and tables at 25Hz."
        }
        appendItem(id, runID: "r36", kind: .todoList) {
            $0.status = "in_progress"
            $0.todoSteps = [
                .with { $0.id = "s1"; $0.text = "Profile conversation VStack layout cost"; $0.status = "completed" },
                .with { $0.id = "s2"; $0.text = "Stream live Markdown synthesis & benchmark deltas"; $0.status = "in_progress" },
                .with { $0.id = "s3"; $0.text = "Verify memory footprint under warning pressure"; $0.status = "pending" },
            ]
        }
        let thinkItem = appendItem(id, runID: "r36", kind: .reasoning) {
            $0.itemID = "stream-think"
            $0.status = "running"
            $0.streaming = true
            $0.text = "Evaluating SwiftUI invalidation fan-out during 25Hz token deltas…"
        }
        let replyItem = appendItem(id, runID: "r36", kind: .assistantMessage) {
            $0.itemID = "stream-reply"
            $0.status = "running"
            $0.streaming = true
            $0.text = "## Live Streaming Synthesis\n\nMeasuring real-time token ingestion:\n\n"
        }

        let chunks = [
            "- **Observation 1**: Isolating `@State draft` prevents full-transcript diffs on every keystroke.\n",
            "- **Observation 2**: Switching conversation `VStack` to `LazyVStack` reduces cold-open layout from O(N) to O(visible).\n",
            "\n```swift\n@MainActor final class TranscriptMemo {\n    var steps: [Step] = []\n    var previousTurns: [Date?] = []\n}\n```\n\n",
            "| Metric | Baseline | Target | Delta |\n| --- | ---: | ---: | ---: |\n| Cold Open | 480 ms | 55 ms | -88% |\n| Stream CPU | 64% | 14% | -78% |\n\n",
            "Continuing token stream verification across nested Markdown blocks, inline `CodeText` runs, and scroll-anchor updates. ",
        ]

        tasks[id] = Task { [weak self] in
            var tick = 0
            var acc = replyItem.text
            while !Task.isCancelled {
                try? await Task.sleep(for: .milliseconds(40)) // 25 Hz
                guard let self, !Task.isCancelled else { return }
                // Only emit while a subscriber has perf-streaming-session open on screen.
                guard self.followers.values.contains(where: { $0.0 == id }) else { continue }
                let piece = chunks[tick % chunks.count]
                acc += piece
                if acc.count > 6_500 {
                    acc = "## Live Streaming Synthesis (Cycle \(tick / chunks.count))\n\n" + piece
                }
                tick += 1
                if var p = self.projections[id] {
                    if tick.isMultiple(of: 5), let tIdx = p.items.firstIndex(where: { $0.itemID == thinkItem.itemID }) {
                        p.items[tIdx].text = "Step \(tick): checking scroll geometry & text layout reuse (\(acc.count) bytes)…"
                        p.items[tIdx].updatedAt = .init(date: .now)
                        let updatedThink = p.items[tIdx]
                        self.projections[id] = p
                        self.emitEvent(id) { $0.type = "item.updated"; $0.item = updatedThink }
                    }
                    if let rIdx = self.projections[id]?.items.firstIndex(where: { $0.itemID == replyItem.itemID }) {
                        self.projections[id]!.items[rIdx].text = acc
                        self.projections[id]!.items[rIdx].updatedAt = .init(date: .now)
                        let updatedReply = self.projections[id]!.items[rIdx]
                        self.emitEvent(id) { $0.type = "item.updated"; $0.item = updatedReply }
                    }
                }
            }
        }
    }

    private func seedActionPanelsSession(now: Date) {
        let id = "perf-panels-session"
        var s = session(id, title: "Action Panels & Plan Review", machine: "demo-mac", status: .waitingInput)
        s.projectID = "ios-client"
        s.agent = "claude"
        s.createdBy = "owner"
        s.pinned = true
        s.interactionMode = .plan
        s.activeRunID = "r4"
        s.pendingRequestID = "perf-plan-req"
        s.updatedAt = .init(date: now.addingTimeInterval(100))
        world.sessions.insert(s, at: 2)

        beginRun(id, runID: "r4", ordinal: 4, asker: "owner", status: .waitingRequest, startedAt: now.addingTimeInterval(-30))
        appendItem(id, runID: "r4", kind: .userMessage) {
            $0.actor = .with { $0.kind = .owner; $0.id = "owner" }
            $0.text = "Draft an execution plan for iOS 120Hz smoothness and wait for review."
        }
        appendItem(id, runID: "r4", kind: .proposedPlan) {
            $0.planTitle = "iOS 120Hz Smoothness & Memory Optimization Plan"
            $0.text = """
            ### Phase 1: Conversation Virtualization
            - Switch `SessionPage` and `HerRows` from eager `VStack` to `LazyVStack`.
            - Isolate composer `@State draft` into its own subview so keystrokes do not diff transcript rows.

            ### Phase 2: RenderServer Offloading
            - Replace 120Hz SwiftUI `.mask` shimmer & canvas orbit loops with lightweight animations and hidden-drawer gating.
            """
        }
        let req = Hermesh_Session_V1_RuntimeRequest.with {
            $0.requestID = "perf-plan-req"; $0.sessionID = id; $0.runID = "r4"
            $0.kind = .planReview; $0.status = .pending; $0.responseMode = .resumeTurn
            $0.planReview = .with { $0.planID = "perf-plan-1"; $0.title = "iOS 120Hz Smoothness & Memory Optimization Plan" }
        }
        projections[id, default: .init()].requests.append(req)
        appendItem(id, runID: "r4", kind: .runtimeRequest) { $0.requestID = req.requestID; $0.status = "waiting" }
        syncSession(id)
    }

    // MARK: - Stress RPC Handlers (Files, HTML Demo, 3000-line Code, 1500-line Diff, 80-turn Her)

    func perfSuiteCall(_ rpc: String, data: Data, id: String) throws -> Data? {
        var dec = JSONDecodingOptions()
        dec.ignoreUnknownFields = true
        switch rpc {
        case "GetHerConversation":
            return try Hermesh_Local_V1_GetHerConversationResponse.with {
                $0.bot = JSON.object(["id": "her", "name": "Her", "default": true, "session_id": "her-perf"]).data()
                $0.snapshot = Self.perfHerSnapshot().data()
            }.jsonUTF8Data()
        case "GetDiff":
            if id == "perf-mega-session" {
                return try Hermesh_Session_V1_GetDiffResponse.with { $0.diff = Self.megaUnifiedDiff }.jsonUTF8Data()
            }
            return nil
        case "ResolveFiles":
            let req = try Hermesh_Session_V1_ResolveFilesRequest(jsonUTF8Data: data, options: dec)
            return try Hermesh_Session_V1_ResolveFilesResponse.with {
                $0.folder = "/Users/owner/Projects/hermesh"
                $0.files = req.paths.map { asked in
                    let clean = Self.cleanPath(asked)
                    let entry = Self.virtualFile(clean)
                    return .with {
                        $0.asked = asked
                        $0.path = clean
                        $0.kind = entry != nil ? .file : (Self.isDirectory(clean) ? .folder : .missing)
                        $0.size = UInt64(entry?.size ?? 0)
                    }
                }
            }.jsonUTF8Data()
        case "ListFiles":
            let req = try Hermesh_Session_V1_ListFilesRequest(jsonUTF8Data: data, options: dec)
            let dir = Self.cleanPath(req.dir)
            return try Hermesh_Session_V1_ListFilesResponse.with {
                $0.dir = dir
                $0.folder = "/Users/owner/Projects/hermesh"
                $0.entries = Self.directoryEntries(for: dir)
            }.jsonUTF8Data()
        case "ReadFile":
            let req = try Hermesh_Session_V1_ReadFileRequest(jsonUTF8Data: data, options: dec)
            let clean = Self.cleanPath(req.path)
            guard let file = Self.virtualFile(clean) else {
                throw APIError(code: "not_found", message: "file not found: \(clean)")
            }
            return try Hermesh_Session_V1_ReadFileResponse.with {
                $0.path = clean
                $0.folder = "/Users/owner/Projects/hermesh"
                $0.size = UInt64(file.size)
                $0.kind = file.kind
                $0.mimeType = file.mime
                if file.kind == .image {
                    $0.image = file.bytes
                } else {
                    $0.text = file.text
                    $0.encoding = "utf-8"
                    $0.hash = SHA256.hash(data: Data(file.text.utf8)).map { String(format: "%02x", $0) }.joined()
                }
            }.jsonUTF8Data()
        case "ReadFileBytes":
            let req = try Hermesh_Session_V1_ReadFileBytesRequest(jsonUTF8Data: data, options: dec)
            let clean = Self.cleanPath(req.path)
            guard let file = Self.virtualFile(clean) else {
                throw APIError(code: "not_found", message: "file not found: \(clean)")
            }
            let all = file.kind == .image ? file.bytes : Data(file.text.utf8)
            let start = min(Int(req.offset), all.count)
            let maxLen = req.length == 0 ? 4 * 1024 * 1024 : Int(req.length)
            let end = min(all.count, start + maxLen)
            return try Hermesh_Session_V1_ReadFileBytesResponse.with {
                $0.data = all.subdata(in: start ..< end)
                $0.size = UInt64(all.count)
                $0.mimeType = file.mime
            }.jsonUTF8Data()
        default:
            return nil
        }
    }

    // MARK: - Virtual File System & Rich Payloads

    private struct VirtualFile {
        let kind: Hermesh_Session_V1_FileContentKind
        let mime: String
        let text: String
        let bytes: Data
        var size: Int { kind == .image ? bytes.count : text.utf8.count }
    }

    private static func cleanPath(_ raw: String) -> String {
        var s = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        while s.hasPrefix("./") { s.removeFirst(2) }
        while s.hasPrefix("/") { s.removeFirst() }
        while s.hasSuffix("/") { s.removeLast() }
        return s
    }

    private static func isDirectory(_ dir: String) -> Bool {
        ["", "src", "src/core", "docs", "assets", "packages", "packages/generated"].contains(dir)
    }

    private static func directoryEntries(for dir: String) -> [Hermesh_Session_V1_FileEntry] {
        func folder(_ name: String) -> Hermesh_Session_V1_FileEntry { .with { $0.name = name; $0.dir = true } }
        func file(_ name: String, _ size: UInt64) -> Hermesh_Session_V1_FileEntry { .with { $0.name = name; $0.size = size } }
        switch dir {
        case "":
            return [
                folder("assets"),
                folder("docs"),
                folder("packages"),
                folder("src"),
                file("package.json", 640),
                file("README.md", UInt64(architectureMarkdown.utf8.count)),
            ]
        case "docs":
            return [
                file("Architecture.md", UInt64(architectureMarkdown.utf8.count)),
                file("interactive-demo.html", UInt64(interactiveDemoHTML.utf8.count)),
                file("demo-style.css", 920),
            ]
        case "src":
            return [
                folder("core"),
                file("index.ts", 480),
            ]
        case "src/core":
            return [
                file("Engine.ts", UInt64(engineTypeScript3000Lines.utf8.count)),
                file("Scheduler.ts", 2_400),
            ]
        case "assets":
            let imgSize = UInt64(samplePNG(index: 0).count)
            return [
                file("dashboard-preview.png", imgSize),
                file("shot-1.png", imgSize),
                file("shot-2.png", imgSize),
                file("shot-3.png", imgSize),
                file("shot-4.png", imgSize),
            ]
        case "packages":
            return [folder("generated")]
        case "packages/generated":
            return (1 ... 1_200).map { i in
                file("module_\(String(format: "%04d", i)).ts", 1_480)
            }
        default:
            return []
        }
    }

    private static func virtualFile(_ path: String) -> VirtualFile? {
        switch path {
        case "docs/interactive-demo.html":
            return VirtualFile(kind: .text, mime: "text/html", text: interactiveDemoHTML, bytes: Data())
        case "docs/demo-style.css":
            return VirtualFile(kind: .text, mime: "text/css", text: "body { margin: 0; font-family: -apple-system, sans-serif; background: #0f172a; color: #f8fafc; }", bytes: Data())
        case "docs/Architecture.md", "README.md":
            return VirtualFile(kind: .text, mime: "text/markdown", text: architectureMarkdown, bytes: Data())
        case "src/core/Engine.ts":
            return VirtualFile(kind: .text, mime: "text/typescript", text: engineTypeScript3000Lines, bytes: Data())
        case "src/core/Scheduler.ts", "src/index.ts":
            return VirtualFile(kind: .text, mime: "text/typescript", text: "export * from './core/Engine';\n", bytes: Data())
        case "package.json":
            return VirtualFile(kind: .text, mime: "application/json", text: "{\n  \"name\": \"@hermesh/perf-suite\",\n  \"version\": \"1.0.0\"\n}\n", bytes: Data())
        case "assets/dashboard-preview.png":
            return VirtualFile(kind: .image, mime: "image/png", text: "", bytes: samplePNG(index: 0))
        case "assets/shot-1.png":
            return VirtualFile(kind: .image, mime: "image/png", text: "", bytes: samplePNG(index: 1))
        case "assets/shot-2.png":
            return VirtualFile(kind: .image, mime: "image/png", text: "", bytes: samplePNG(index: 2))
        case "assets/shot-3.png":
            return VirtualFile(kind: .image, mime: "image/png", text: "", bytes: samplePNG(index: 3))
        case "assets/shot-4.png":
            return VirtualFile(kind: .image, mime: "image/png", text: "", bytes: samplePNG(index: 4))
        default:
            if path.hasPrefix("packages/generated/module_"), path.hasSuffix(".ts") {
                let body = (1 ... 35).map { "export const line_\($0) = \($0) * 42;" }.joined(separator: "\n") + "\n"
                return VirtualFile(kind: .text, mime: "text/typescript", text: body, bytes: Data())
            }
            return nil
        }
    }

    private static let cachedPNGs: [Data] = (0 ..< 5).map { idx in
        let renderer = UIGraphicsImageRenderer(size: CGSize(width: 640, height: 400))
        let colors: [UIColor] = [.systemIndigo, .systemTeal, .systemBlue, .systemOrange, .systemPurple]
        let img = renderer.image { ctx in
            colors[idx % colors.count].setFill()
            ctx.fill(CGRect(x: 0, y: 0, width: 640, height: 400))
            UIColor.white.withAlphaComponent(0.18).setFill()
            for bar in 0 ..< 8 {
                let h = CGFloat(60 + ((bar * 43 + idx * 29) % 240))
                ctx.fill(CGRect(x: CGFloat(48 + bar * 70), y: 340 - h, width: 44, height: h))
            }
        }
        return img.pngData() ?? Data()
    }

    private static func samplePNG(index: Int) -> Data {
        cachedPNGs[index % cachedPNGs.count]
    }

    private static func richTurnMarkdown(turn t: Int) -> String {
        var md = """
        ### Subsystem \(t) Frame & Allocation Audit

        Completed inspection of [Engine.ts](src/core/Engine.ts#L\(max(1, (t * 30) % 2800))-L\(max(10, (t * 30) % 2800 + 18))) and cross-checked layout invalidations against `SessionPage` and `DrawerView`.

        | Component | Evaluations / Sec | Main-Thread Cost | Heap Delta | Status |
        | --- | ---: | ---: | ---: | --- |
        | `SessionPage` | \(12 + (t % 8)) | \(14 + (t % 19)) ms | +1.4 MB | Optimized |
        | `MenuSessions` | \(6 + (t % 5)) | \(8 + (t % 11)) ms | +0.6 MB | Cached |
        | `CodeText` | \(4 + (t % 4)) | \(5 + (t % 9)) ms | +0.3 MB | Memoized |

        ```swift
        func reconcileWindow_\(t)(rows: [Step], viewport: CGRect) -> ArraySlice<Step> {
            let start = max(0, Int(viewport.minY / 64) - 4)
            let end = min(rows.count, Int(viewport.maxY / 64) + 6)
            return rows[start ..< max(start, end)]
        }
        ```
        """
        if t.isMultiple(of: 12) {
            md += """


            ```mermaid
            flowchart LR
                Stream["Event Stream #\(t)"] --> Reducer["Transcript Memo"]
                Reducer --> Lazy["LazyVStack Viewport"]
                Lazy --> GPU["CoreAnimation RenderServer"]
            ```
            """
        }
        if t.isMultiple(of: 15) {
            md += "\n\n![Dashboard Preview](assets/dashboard-preview.png)\n"
        }
        return md
    }

    private static let megaFinalReplyMarkdown = """
    ## Comprehensive 80-Turn Stress Audit Complete

    All **240 automated verification steps** succeeded. You can inspect the interactive benchmark dashboard and artifacts directly:

    - Interactive Demo Preview: [interactive-demo.html](docs/interactive-demo.html)
    - Architecture & Mermaid Spec: [Architecture.md](docs/Architecture.md)
    - 3,000-Line Core Engine: [Engine.ts](src/core/Engine.ts#L120-L145)

    | Scenario | Before (p95) | After (p95) | CPU Saving | Memory Saving |
    | --- | ---: | ---: | ---: | ---: |
    | 80-Turn Session Open | 520 ms | 48 ms | -86% | -42 MB |
    | Composer Keystroke | 38 ms | 1.8 ms | -95% | -12 MB |
    | Overview Idle Shimmer | 29% CPU | 3% CPU | -89% | -18 MB |
    | 3,000-Line Code Scroll | 120 FPS | 120 FPS | -54% | -9 MB |

    ```bash
    echo "Perf suite verification green"
    ```

    ![Shot 1](assets/shot-1.png)
    ![Shot 2](assets/shot-2.png)
    ![Shot 3](assets/shot-3.png)
    ![Shot 4](assets/shot-4.png)
    """

    private static let interactiveDemoHTML = """
    <!doctype html>
    <html lang="en">
    <head>
      <meta charset="utf-8">
      <meta name="viewport" content="width=device-width, initial-scale=1">
      <title>Hermesh Interactive Perf Demo</title>
      <link rel="stylesheet" href="demo-style.css">
      <style>
        .wrap { padding: 16px; max-width: 720px; margin: 0 auto; }
        .bar { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
        .pill { background: #1e293b; border: 1px solid #334155; color: #e2e8f0; padding: 8px 14px; border-radius: 999px; font-weight: 600; cursor: pointer; }
        .cards { display: grid; grid-template-columns: repeat(3, 1fr); gap: 10px; margin-bottom: 14px; }
        .card { background: #1e293b; border-radius: 12px; padding: 12px; border: 1px solid #334155; }
        .val { font-size: 22px; font-weight: 700; color: #38bdf8; }
        canvas { width: 100%; height: 200px; background: #090d16; border-radius: 12px; border: 1px solid #1e293b; display: block; }
      </style>
    </head>
    <body>
      <div class="wrap">
        <div class="bar">
          <h2 style="margin:0;font-size:18px">Interactive Frame Pacing Demo</h2>
          <button id="boost" class="pill" onclick="toggleBoost()">Boost Load: OFF</button>
        </div>
        <div class="cards">
          <div class="card"><div>Target</div><div class="val">120 Hz</div></div>
          <div class="card"><div>Particles</div><div id="pcount" class="val">64</div></div>
          <div class="card"><div>Tick</div><div id="tick" class="val">0</div></div>
        </div>
        <canvas id="stage" width="640" height="240"></canvas>
      </div>
      <script>
        let boost = false, tick = 0;
        const canvas = document.getElementById('stage');
        const ctx = canvas.getContext('2d');
        function toggleBoost() {
          boost = !boost;
          document.getElementById('boost').textContent = 'Boost Load: ' + (boost ? 'ON' : 'OFF');
          document.getElementById('pcount').textContent = boost ? '256' : '64';
        }
        function frame() {
          tick++;
          if (tick % 6 === 0) document.getElementById('tick').textContent = String(tick);
          ctx.fillStyle = 'rgba(9, 13, 22, 0.35)';
          ctx.fillRect(0, 0, canvas.width, canvas.height);
          const n = boost ? 256 : 64;
          for (let i = 0; i < n; i++) {
            const x = (i / n) * canvas.width;
            const y = 120 + Math.sin(tick * 0.05 + i * 0.22) * (40 + (i % 5) * 12);
            ctx.fillStyle = i % 2 === 0 ? '#38bdf8' : '#818cf8';
            ctx.fillRect(x, y, 3, 14);
          }
          requestAnimationFrame(frame);
        }
        requestAnimationFrame(frame);
      </script>
    </body>
    </html>
    """

    private static let architectureMarkdown: String = {
        var parts: [String] = [
            "# Hermesh iOS Performance Architecture",
            "",
            "This specification exercises long-document Markdown rendering, wide GFM tables, syntax-highlighted code blocks, and Mermaid diagrams.",
            "",
            "```mermaid",
            "sequenceDiagram",
            "    participant U as User Finger",
            "    participant UI as UIKit Spring / ScrollView",
            "    participant S as SwiftUI LazyVStack",
            "    participant CA as CoreAnimation RenderServer",
            "    U->>UI: Pan / Flick Gesture",
            "    UI->>CA: GPU Transform & Compositing",
            "    UI->>S: Materialize Visible Slice Only",
            "```",
            "",
        ]
        for section in 1 ... 30 {
            parts.append("""
            ## Section \(section): Subsystem Budget & Invalidation Rules

            Every hot path keeps view invalidation scoped to the leaf view that actually displays changing state:

            | Rule | Target | Budget | Verification |
            | --- | --- | ---: | --- |
            | Lazy Row Materialization | `SessionPage` / `HerConversation` | < 8.3 ms | `PerfStressUITests` |
            | Keystroke Isolation | `ComposerBox` | < 2.0 ms | `testPerfSuite` |
            | Offscreen Drawer Gating | `SideMenuController` | 0% Idle CPU | `FrameMeter` |

            ```typescript
            export function checkBudgetSection\(section)(frameMs: number, hitchMs: number): boolean {
              return frameMs <= 8.33 && hitchMs < 5.0;
            }
            ```
            """)
        }
        return parts.joined(separator: "\n\n")
    }()

    private static let engineTypeScript3000Lines: String = {
        var lines: [String] = [
            "// Engine.ts — 3,000-line synthetic TypeScript module for CodePage & Shiki Highlighter stress testing",
            "export interface FrameSample { seq: number; durationMs: number; late: boolean; cpuMs: number; memKB: number; }",
            "",
        ]
        lines.reserveCapacity(3_020)
        for i in 1 ... 500 {
            lines.append("export function processPipelineStage\(i)(samples: readonly FrameSample[], threshold = 8.33): number {")
            lines.append("  let acc = 0;")
            lines.append("  for (let idx = 0; idx < samples.length; idx++) {")
            lines.append("    const s = samples[idx];")
            lines.append("    if (s.durationMs > threshold || s.late) { acc += s.durationMs - threshold; }")
            lines.append("  }")
            lines.append("  return Math.round(acc * 100) / 100;")
            lines.append("}")
        }
        return lines.joined(separator: "\n")
    }()

    private static let megaUnifiedDiff: String = {
        var chunks: [String] = []
        for fileIdx in 1 ... 12 {
            let path = fileIdx == 1 ? "src/core/Engine.ts" : "src/modules/Pipeline\(fileIdx).ts"
            var hunk = [
                "diff --git a/\(path) b/\(path)",
                "--- a/\(path)",
                "+++ b/\(path)",
                "@@ -1,60 +1,68 @@",
            ]
            for line in 1 ... 60 {
                if line % 5 == 0 {
                    hunk.append("-  const legacyValue\(line) = computeEagerLayout(\(line), state);")
                    hunk.append("+  const fastValue\(line) = computeLazyViewportLayout(\(line), viewport);")
                    hunk.append("+  recordMetric('stage_\(fileIdx)_\(line)', fastValue\(line));")
                } else {
                    hunk.append("   const contextLine\(line) = state.frame + \(line);")
                }
            }
            chunks.append(hunk.joined(separator: "\n"))
        }
        return chunks.joined(separator: "\n") + "\n"
    }()

    private static func perfHerSnapshot() -> JSON {
        let turns = 80
        var items: [JSON] = []
        let end = Date.now.timeIntervalSince1970 * 1000
        for i in 0 ..< turns {
            let ts = end - Double(turns - i) * 360_000 - Double((turns - i) / 8) * 14_400_000
            items.append([
                "type": "message",
                "message_id": .string("hu-\(i)"),
                "message": [
                    "role": "user",
                    "ts": .number(ts),
                    "content": [["type": "text", "text": .string("Sprint check #\(i + 1): summarize running coding agents, frame pacing, and next actions.")]],
                ],
            ])
            let mod = i % 6
            let tool: String
            let args: JSON
            let details: JSON
            if mod == 4 {
                tool = "session_start"
                args = ["title": .string("Refactor WebSocket multiplex framing")]
                details = ["session": ["session_id": "perf-run-1", "title": "Refactor WebSocket multiplex framing", "agent": "antigravity", "machine": "MacBook Pro M4 Max"]]
            } else if mod == 5 {
                tool = "work_summary"
                args = [:]
                details = ["work": [
                    "days": 7, "sessions": 260, "done": 212, "turns": 1840,
                    "input_tokens": 14_200_000, "output_tokens": 2_180_000,
                    "buckets": [4, 9, 12, 7, 15, 22, 18, 14, 19, 25, 21, 16],
                    "by_agent": [["agent": "antigravity", "sessions": 96], ["agent": "codex", "sessions": 88], ["agent": "claude", "sessions": 76]],
                    "by_client": [["client": "owner", "sessions": 160], ["client": "c-her", "sessions": 100]],
                ]]
            } else {
                tool = "send_to_user"
                args = ["text": .string(richTurnMarkdown(turn: i + 1))]
                details = .null
            }
            items.append([
                "type": "message",
                "message_id": .string("ha-\(i)"),
                "message": [
                    "role": "assistant",
                    "ts": .number(ts + 1200),
                    "duration_ms": 2800,
                    "content": [["type": "tool_call", "id": .string("hc-\(i)"), "name": .string(tool), "arguments": args]],
                ],
            ])
            items.append([
                "type": "tool_result",
                "call_id": .string("hc-\(i)"),
                "result": ["tool_name": .string(tool), "details": details],
            ])
            items.append(["type": "task_end", "outcome": "completed", "ts": .number(ts + 4200)])
        }
        return [
            "session": ["session_id": "her-perf", "model": "demo/her-pro"],
            "last_seq": .number(Double(items.count)),
            "transcript": ["items": .array(items)],
        ]
    }
}
#endif
