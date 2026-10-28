import HermeshKit
import HermeshSessions
import SwiftUI

/// Erase Everything (docs/erase-everything.md), from the account: what it
/// erases, by name, and what stays, confirmed by typing the account's email
/// (a server of one's own: its address) and approved with Face ID; then each
/// device, machine and agent ticked as it reports, and this iPhone erased
/// last. Those offline are reset (or paired again) when next used.
struct EraseView: View {
    @Environment(AppModel.self) private var model
    @State private var items: [EraseItem]?
    @State private var typed = ""
    @State private var eraseID: String?
    @State private var working = false
    @State private var error: String?
    @FocusState private var typing: Bool

    /// How long this iPhone waits for those online to report before it erases itself.
    private static let waitOnline: Duration = .seconds(15)

    var body: some View {
        List {
            if eraseID == nil { confirm } else { progress }
        }
        .navigationTitle("Erase Everything")
        .navigationBarTitleDisplayMode(.inline)
        .navigationBarBackButtonHidden(eraseID != nil || working)
        .interactiveDismissDisabled(eraseID != nil || working)
        .task {
            do { items = try await model.eraseItems() } catch { self.error = error.localizedDescription }
        }
    }

    /// The server as its address names it (with its port), as the Mac shows it too.
    private var host: String {
        guard let u = URLComponents(string: model.status.relayURL), let h = u.host else { return model.status.relayURL }
        return u.port.map { "\(h):\($0)" } ?? h
    }

    /// What is typed to confirm: the account's email, or a server of one's own's address.
    private var expected: String {
        model.status.hosted && !model.status.account.isEmpty ? model.status.account : host
    }

    @ViewBuilder private var confirm: some View {
        Section {
            group("Trusted Devices", "Signed out; their owner keys, sessions and settings erased, and, for one that also runs tasks, its projects list and worktrees", kind: "device")
            group("Machines", "Unpaired; their projects lists, sessions and the worktrees Hermesh made erased", kind: "machine")
            group("Agents", "Disconnected; their pairing on their own computers erased", kind: "agent")
            VStack(alignment: .leading, spacing: 2) {
                if model.status.hosted {
                    Text("Data on \(host)")
                    Text("Messages, new-message alerts and connection approvals; every browser signed out").font(.footnote).foregroundStyle(.secondary)
                } else {
                    Text("Your Server · \(host)")
                    Text("Back to unclaimed, with a new claim code").font(.footnote).foregroundStyle(.secondary)
                }
            }
        } header: {
            Text("Erases")
        } footer: {
            Text("Hermesh on all your devices goes back to how it was when first installed.")
        }
        Section {
            Text("Your project directories and repositories, branches included")
            Text("Worktrees with uncommitted changes: moved aside and kept, and you’re told where")
            if model.status.hosted { Text("Your account: sign in with it again for a fresh start") }
            Text("What offline devices hold, until each is reset")
        } header: {
            Text("Stays")
        }
        .font(.subheadline).foregroundStyle(.secondary)
        Section {
            TextField(expected, text: $typed)
                .textInputAutocapitalization(.never).autocorrectionDisabled().keyboardType(.URL)
                .focused($typing)
                .submitLabel(.done)
                .onSubmit { typing = false }
                .accessibilityIdentifier("erase-confirm")
        } footer: {
            Text("This can’t be undone. Type \(expected) to confirm.")
        }
        Section {
            Button(role: .destructive) {
                Task { await erase() }
            } label: {
                HStack {
                    Spacer()
                    if working { ProgressView() } else { Text("Erase Everything").fontWeight(.semibold) }
                    Spacer()
                }
            }
            .disabled(!matches || items == nil || working)
            .accessibilityIdentifier("erase-button")
        } footer: {
            if let error { Text(error).foregroundStyle(.red) }
        }
    }

    private var matches: Bool {
        !expected.isEmpty && typed.trimmingCharacters(in: .whitespaces).lowercased() == expected.lowercased()
    }

    /// One kind of what is erased: what happens to it, and each one by name.
    @ViewBuilder private func group(_ title: LocalizedStringKey, _ desc: LocalizedStringKey, kind: String) -> some View {
        let of = (items ?? []).filter { $0.kind == kind }
        if !of.isEmpty {
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                Text(listText(of.map(name))).font(.subheadline)
                Text(desc).font(.footnote).foregroundStyle(.secondary)
            }
        }
    }

    private func name(_ it: EraseItem) -> String {
        if it.thisDevice { return String(localized: "This iPhone") }
        let n = it.kind == "agent" ? AgentLogo.name(it.name) : it.name
        return it.online || it.kind == "agent" ? n : String(localized: "\(n) (offline)")
    }

    @ViewBuilder private var progress: some View {
        Section {
            ForEach(Array((items ?? []).sorted { !$0.thisDevice && $1.thisDevice }.enumerated()), id: \.offset) { _, it in
                LabeledContent {
                    state(it)
                } label: {
                    Text(it.thisDevice ? String(localized: "This iPhone") : it.kind == "agent" ? AgentLogo.name(it.name) : it.name)
                }
            }
        } header: {
            Text("Erasing…")
        } footer: {
            VStack(alignment: .leading, spacing: 8) {
                Text("Those online erase now; reset or pair the others again when you next use them.")
                if let error {
                    Text(error).foregroundStyle(.red)
                    Button("Try Again") { Task { await finish() } }
                }
            }
        }
    }

    @ViewBuilder private func state(_ it: EraseItem) -> some View {
        if it.done {
            Label(it.kind == "agent" ? "Disconnected" : "Erased", systemImage: "checkmark").foregroundStyle(.green)
        } else if it.thisDevice || it.online {
            HStack(spacing: 6) {
                ProgressView().controlSize(.small)
                Text(it.thisDevice ? "Last" : "Erasing")
            }
            .foregroundStyle(.secondary)
        } else {
            Text(it.kind == "agent" ? "Signs in again when next used" : it.kind == "machine" ? "To pair again when next online" : "Offers a reset when next opened").foregroundStyle(.secondary)
        }
    }

    private func erase() async {
        typing = false
        working = true
        error = nil
        do {
            let id = try await model.eraseEverything()
            eraseID = id
            await follow(id)
        } catch {
            self.error = error.localizedDescription
        }
        working = false
    }

    /// Ticks each one as it reports; once those online did (or after a
    /// while), this iPhone erases itself.
    private func follow(_ id: String) async {
        let start = ContinuousClock.now
        while true {
            if let now = try? await model.eraseItems(id) { items = now }
            if ContinuousClock.now - start > Self.waitOnline || (items ?? []).allSatisfy({ $0.thisDevice || !$0.online || $0.done }) { break }
            try? await Task.sleep(for: .seconds(1))
        }
        await finish()
    }

    private func finish() async {
        error = nil
        do { try await model.finishErase() } catch {
            self.error = String(localized: "This iPhone couldn’t erase itself: \(error.localizedDescription)")
        }
    }
}
