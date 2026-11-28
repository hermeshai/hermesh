import HermeshHer
import HermeshProto
import HermeshSessions
import SwiftUI

func workspaceText(_ w: Hermesh_Identity_V1_Workspace) -> String {
    switch w {
    case .inPlace: String(localized: "In place")
    default: String(localized: "Separate worktree")
    }
}

/// Where each coding agent says how to install it.
let installPage: [String: URL] = [
    "claude": URL(string: "https://code.claude.com/docs/en/setup")!,
    "codex": URL(string: "https://github.com/openai/codex")!,
    "antigravity": URL(string: "https://github.com/google-antigravity/antigravity-cli")!,
    "cursor": URL(string: "https://cursor.com/docs/cli/installation")!,
    "goose": URL(string: "https://github.com/aaif-goose/goose")!,
    "kiro": URL(string: "https://kiro.dev/docs/getting-started/installation/")!,
    "deepseek": URL(string: "https://github.com/deepseek-ai/deepseek-harness")!,
]

/// "v1.2.3" or "1.2.3-dev" as [1, 2, 3]; nil for a build with no version.
func versionParts(_ v: String) -> [Int]? {
    let core = v.trimmingCharacters(in: CharacterSet(charactersIn: "v")).split(separator: "-").first ?? ""
    let parts = core.split(separator: ".").compactMap { Int($0) }
    return parts.count >= 2 ? parts : nil
}

/// A machine's "darwin/arm64" as "macOS · arm64".
func platformText(_ platform: String) -> String {
    let parts = platform.split(separator: "/").map(String.init)
    let os = ["darwin": "macOS", "linux": "Linux", "windows": "Windows"][parts.first ?? ""] ?? parts.first ?? ""
    return ([os] + parts.dropFirst()).filter { !$0.isEmpty }.joined(separator: " · ")
}

func machineIcon(_ m: Machine) -> String {
    m.status.platform.contains("linux") ? "server.rack" : m.local ? "laptopcomputer" : "desktopcomputer"
}

/// The green shield of a trusted device.
struct TrustedMark: View {
    var body: some View {
        Image(systemName: "checkmark.shield.fill").font(.footnote).foregroundStyle(.green)
            .accessibilityLabel(Text("Trusted device"))
    }
}

struct MachineRow: View {
    @Environment(AppModel.self) private var model
    let machine: Machine
    var trusted = false

    @Environment(\.dynamicTypeSize) private var size

    var body: some View {
        let m = machine
        // In the largest text sizes the state goes under the name, which then has the row's width.
        let layout = size.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 6)) : AnyLayout(HStackLayout(spacing: 12))
        layout {
            HStack(spacing: 12) {
                Image(systemName: machineIcon(m)).frame(width: 28, height: 28).foregroundStyle(.secondary)
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 5) {
                        Text(m.machineName)
                        if trusted { TrustedMark() }
                    }
                    Text(m.online ? platformText(m.status.platform) : String(localized: "Last online \(m.hasLastSeen ? agoText(m.lastSeen.date, now: model.now) : String(localized: "a long time ago"))"))
                        .font(.subheadline).foregroundStyle(.secondary)
                }
            }
            if !size.isAccessibilitySize { Spacer() }
            state(m)
        }
    }

    private func state(_ m: Machine) -> some View {
        let running = Int(m.status.runningSessions)
        return HStack(spacing: 12) {
            Circle().fill(m.online ? Color.green : .clear).strokeBorder(m.online ? .clear : Color.secondary, lineWidth: 1.5)
                .frame(width: 8, height: 8)
            Group {
                if !m.online {
                    Text("Offline")
                } else if m.status.paused {
                    Text("Paused")
                } else if running > 0 {
                    Text("\(running) running")
                } else {
                    Text("Online")
                }
            }
            .font(.subheadline).foregroundStyle(.secondary).monospacedDigit()
        }
        // Under the name, past the icon.
        .padding(.leading, size.isAccessibilitySize ? 40 : 0)
    }
}

/// A command to run on a machine, to copy or send there: what it is for,
/// the command, and what went wrong when that is known.
struct CommandSheet: View {
    let title: LocalizedStringKey
    let message: LocalizedStringKey
    let command: String
    var details = ""
    /// Its content's height: the sheet is as tall as that.
    @State private var height: CGFloat = 300

    var body: some View {
        NavigationStack {
            VStack(alignment: .leading, spacing: 16) {
                Text(message).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                MonoBox(text: command)
                CopyShareButtons(text: command, shareTitle: "Send…")
                if !details.isEmpty {
                    DisclosureGroup("Details") {
                        Text(details).font(.caption.monospaced()).foregroundStyle(.secondary).textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading).padding(.top, 8)
                    }
                    .font(.subheadline)
                }
            }
            .padding(.horizontal, 20).padding(.top, 8).padding(.bottom, 8)
            .fixedSize(horizontal: false, vertical: true)
            .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { height = $0 }
            .frame(maxHeight: .infinity, alignment: .top)
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { CloseButton() }
        }
        .presentationDetents([.height(height + 70)])
    }
}

/// A project in a list: its name, how agents work in it and its directory.
struct ProjectLine: View {
    let project: Project
    let detail: String

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: "folder").foregroundStyle(.secondary).frame(width: 30)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(project.projectID)
                    Text(detail).font(.subheadline).foregroundStyle(.secondary)
                }
                .lineLimit(1)
                Text(project.path).font(.caption.monospaced()).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
            }
        }
    }
}
