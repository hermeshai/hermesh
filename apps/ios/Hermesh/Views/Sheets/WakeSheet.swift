import HermeshKit
import HermeshProto
import HermeshSessions
import SwiftUI
import UIKit

// An agent's doorbell (docs/agent-names.md §7): the webhook Hermesh calls
// when messages wait for the agent, or one of its bots. A bot that can be
// woken asks for one with a wake link, which a computer opens (the routine's
// URL and key show in Grok's desktop app only); here, one is seen, woken
// once, given another URL and key, or stopped. The key goes from here to
// Hermesh's vault; neither the bot nor its model ever sees it.

/// Why a webhook didn't take a call, from its answer, in the app's words.
func wakeReason(status: Int32, reason: String) -> String {
    switch status {
    case 400: return String(localized: "It answered 400: is the routine paused?")
    case 401, 403: return String(localized: "It answered \(Int(status)): the key is wrong")
    case 404, 410: return String(localized: "It answered \(Int(status)): no such webhook, check the URL")
    case 429: return String(localized: "It’s busy (429): try again in a while")
    case 500...: return String(localized: "It failed (\(Int(status))): try again in a while")
    case 0 where !reason.isEmpty: return String(localized: "Hermesh couldn’t reach the webhook")
    default: return reason
    }
}

/// A routine's URL and key from what was pasted: one of them, or both at
/// once (Grok's Webhook section copied whole), its labels left out.
func splitWebhook(_ text: String) -> (url: String?, key: String?) {
    guard let r = text.range(of: #"https?://[^\s"'<>]+"#, options: .regularExpression) else { return (nil, text) }
    let labels = ["post", "to", "key", "header", "authorization:", "authorization", "bearer"]
    let rest = text.replacingCharacters(in: r, with: " ").split(whereSeparator: \.isWhitespace).map(String.init)
        .filter { !labels.contains($0.lowercased()) && !$0.allSatisfy { ":：-".contains($0) } }
    return (String(text[r]), rest.last)
}

struct WakeSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let clientID: String
    let agent: String
    let existing: Wake

    private enum Step { case webhook, check, manage }
    private enum Field { case url, key }
    @State private var step = Step.manage
    @State private var wake: Wake
    @State private var url = ""
    @State private var key = ""
    @State private var busy = false
    @State private var check: WakeCheck?
    @State private var saved = false
    @State private var secret = ""
    @State private var error = ""
    @State private var confirmRemove = false

    init(clientID: String, agent: String, existing: Wake) {
        self.clientID = clientID
        self.agent = agent
        self.existing = existing
        _wake = State(initialValue: existing)
    }

    private var name: String { existing.name }

    private var who: String { name.isEmpty ? agent : name }

    /// Grok's routines don't check signatures: their owners never need the signing secret.
    private var isGrok: Bool { URL(string: url.trimmingCharacters(in: .whitespaces))?.host?.hasSuffix("cursor.sh") ?? false }

    var body: some View {
        NavigationStack {
            Form { content }
                .navigationTitle(String(localized: "Wake \(who)"))
                .navigationBarTitleDisplayMode(.inline)
                .toolbar { CloseButton() }
        }
        // As it is now: it may have rung since the list was read.
        .task {
            guard let api = model.api, let all = try? await api.listWakes(clientID) else { return }
            if let w = all.first(where: { $0.name == existing.name }) { wake = w }
        }
    }

    @ViewBuilder private var content: some View {
        switch step {
        case .webhook:
            Section {
                HStack {
                    TextField("URL (POST to)", text: Binding(get: { url }, set: { paste($0, into: .url) }))
                        .keyboardType(.URL).textContentType(.URL).textInputAutocapitalization(.never).autocorrectionDisabled()
                        .accessibilityIdentifier("wake-url")
                    Button("Paste") { paste(UIPasteboard.general.string ?? "", into: .url) }.buttonStyle(.borderless)
                }
                HStack {
                    // Not a password of the owner's: a secure field would have iOS offer to save it.
                    TextField("Key", text: Binding(get: { key }, set: { paste($0, into: .key) }))
                        .textInputAutocapitalization(.never).autocorrectionDisabled().privacySensitive()
                        .accessibilityIdentifier("wake-key")
                    Button("Paste") { paste(UIPasteboard.general.string ?? "", into: .key) }.buttonStyle(.borderless)
                }
            } header: {
                Text("Paste the URL and key")
            } footer: {
                Text("In Grok’s desktop app, open the routine the bot made: in its Webhook section, clicking “POST to” or “key” copies it. Pasting both into either box works too. The key stays in Hermesh; the bot never sees it.")
            }
            Section {
                Button("Wake It Once") { Task { await save(keep: false) } }
                    .disabled(url.trimmingCharacters(in: .whitespaces).isEmpty).accessibilityIdentifier("wake-check")
            }
        case .check:
            checkResult
        case .manage:
            manage
        }
    }

    /// Either box takes both at once.
    private func paste(_ text: String, into field: Field) {
        let p = splitWebhook(text)
        if let u = p.url, field == .key || p.key != nil {
            url = u
            key = p.key ?? ""
        } else if field == .url {
            url = text
        } else {
            key = text
        }
    }

    @ViewBuilder private var checkResult: some View {
        Section {
            VStack(spacing: 10) {
                if busy {
                    ProgressView()
                    Text("Waking it…").font(.headline)
                    Text("Hermesh wakes it once; it reads any messages already waiting.").font(.subheadline).foregroundStyle(.secondary)
                } else if check?.ok == true {
                    Image(systemName: "checkmark.circle.fill").font(.system(size: 44)).foregroundStyle(.green)
                    Text("All Set").font(.headline).accessibilityIdentifier("wake-ok")
                    Text("It was just woken once, so it shows one more run. From now on, Hermesh wakes it like this when messages arrive.")
                        .font(.subheadline).foregroundStyle(.secondary)
                    Text("Each time it’s woken, the bot runs once.").font(.footnote).foregroundStyle(.secondary)
                } else {
                    Image(systemName: "exclamationmark.circle.fill").font(.system(size: 44)).foregroundStyle(.red)
                    Text(saved ? String(localized: "Saved, marked as not working") : String(localized: "It Didn’t Wake")).font(.headline)
                    Text(check.map { wakeReason(status: $0.status, reason: $0.reason) } ?? error).font(.subheadline).foregroundStyle(.secondary)
                        .accessibilityIdentifier("wake-reason")
                }
            }
            .multilineTextAlignment(.center).frame(maxWidth: .infinity).padding(.vertical, 8)
        }
        if !busy {
            Section {
                if saved {
                    Button("Done") { dismiss() }.accessibilityIdentifier("wake-done")
                } else {
                    Button("Go Back") { step = .webhook }.accessibilityIdentifier("wake-back")
                    if check != nil { Button("Save Anyway") { Task { await save(keep: true) } } }
                }
            }
            if saved, !secret.isEmpty, !isGrok {
                Section("Signing secret, only for a webhook you wrote yourself that checks signatures (shown this once)") {
                    Text(secret).font(.footnote.monospaced()).textSelection(.enabled)
                }
            }
        }
    }

    /// What a past ring was, and what came of it.
    private func ringText(_ r: WakeRing, doorbell: String) -> String {
        let why = r.check ? String(localized: "A try") : !r.as.isEmpty && r.as != doorbell ? String(localized: "New messages for \(r.as)") : String(localized: "New messages")
        let what = (200..<300).contains(r.status) ? String(localized: "it took it") : wakeReason(status: r.status, reason: "unreachable")
        return "\(why) · \(what)"
    }

    @ViewBuilder private var manage: some View {
        Section {
            LabeledContent("Status", value: wake.state == "on" ? String(localized: "Turned On") : String(localized: "Not working"))
            LabeledContent("Webhook", value: wake.url).font(.body.monospaced()).lineLimit(1)
            LabeledContent("Waiting messages", value: "\(wake.waiting)")
        } footer: {
            if wake.state != "on" { Text(wakeReason(status: wake.lastStatus, reason: wake.reason)).foregroundStyle(.red) }
        }
        Section("Recent") {
            if wake.recent.isEmpty { Text("Not woken yet").foregroundStyle(.secondary) }
            ForEach(Array(wake.recent.enumerated()), id: \.offset) { _, r in
                LabeledContent(agoText(r.at.date, now: model.now), value: ringText(r, doorbell: wake.name))
            }
        }
        Section {
            Button("Wake It Once") { Task { await checkNow() } }.disabled(busy)
            Button("Replace URL and Key…") { step = .webhook }.accessibilityIdentifier("wake-replace")
            Button("Stop Waking…", role: .destructive) { confirmRemove = true }.accessibilityIdentifier("wake-remove")
        }
        .confirmationDialog("Stop waking \(who)?", isPresented: $confirmRemove, titleVisibility: .visible) {
            Button("Stop Waking", role: .destructive) { Task { await remove() } }
        } message: {
            Text("Hermesh stops calling its webhook. Its messages still wait for it.")
        }
    }

    private func bearer() -> String {
        let k = key.trimmingCharacters(in: .whitespaces)
        return k.isEmpty || k.lowercased().hasPrefix("bearer ") ? k : "Bearer \(k)"
    }

    private func save(keep: Bool) async {
        guard let api = model.api else { return }
        step = .check
        busy = true
        check = nil
        defer { busy = false }
        do {
            let res = try await api.setWake(clientID, name: name, url: url.trimmingCharacters(in: .whitespaces), authorization: bearer(), keep: keep)
            check = res.check
            saved = res.hasWake
            secret = res.signingSecret
        } catch {
            self.error = (error as? APIError)?.message ?? error.localizedDescription
        }
    }

    private func checkNow() async {
        guard let api = model.api else { return }
        busy = true
        defer { busy = false }
        if let res = try? await api.checkWake(clientID, name: name) {
            wake = res.wake
            model.show(res.check.ok ? String(localized: "Webhook verified and working.") : wakeReason(status: res.check.status, reason: res.check.reason))
        }
    }

    private func remove() async {
        guard let api = model.api else { return }
        do {
            try await api.clearWake(clientID, name: name)
            model.show(String(localized: "No longer waking \(who)"))
            dismiss()
        } catch {
            model.show((error as? APIError)?.message ?? error.localizedDescription)
        }
    }
}
