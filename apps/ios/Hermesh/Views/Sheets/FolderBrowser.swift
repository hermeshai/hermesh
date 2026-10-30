import HermeshKit
import HermeshProto
import SwiftUI

/// One of a machine's folders, for Add a Project: its folders, each opening
/// a screen of its own, and Choose at the bottom. Hidden folders and a typed
/// path are in the menu.
struct FolderBrowser: View {
    @Environment(AppModel.self) private var model
    let machineID: String
    let path: String
    @Binding var browsing: [String]
    /// The folder chosen, with its listing; none from a machine from before
    /// ListFolders, where the person typed its path.
    let choose: (String, Hermesh_Machine_V1_ListFoldersResponse?) -> Void
    @State private var listing: Hermesh_Machine_V1_ListFoldersResponse?
    @State private var failure: String?
    @State private var tooOld = false
    @AppStorage("showHiddenFolders") private var hidden = false
    @State private var typing = false
    @State private var typed = ""
    @State private var asking: Hermesh_Machine_V1_Folder?

    private var shown: String { listing?.path ?? path }
    private var machine: String { model.machineName(machineID) }

    var body: some View {
        List {
            Section {
                if let listing {
                    ForEach(listing.folders, id: \.path) { f in
                        Button { open(f) } label: {
                            HStack {
                                FolderLabel(name: f.name, detail: f.repo ? "git · \(f.branch)" : f.protected ? String(localized: "macOS asks on that Mac before it opens") : "",
                                            repo: f.repo, protected: f.protected)
                                Spacer()
                                Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(.tertiary)
                            }
                            .contentShape(Rectangle())
                        }
                        .opacity(f.name.hasPrefix(".") ? 0.6 : 1)
                        .accessibilityIdentifier("folder-\(f.name)")
                    }
                    if listing.folders.isEmpty { Text("No folders").foregroundStyle(.secondary) }
                } else if let failure {
                    Text(failure).foregroundStyle(.secondary)
                    Button("Type a Path…") { typing = true }
                } else {
                    HStack(spacing: 10) {
                        ProgressView()
                        Text("Reading \(machine)…").foregroundStyle(.secondary)
                    }
                }
            } header: {
                trail
            } footer: {
                if let listing {
                    VStack(alignment: .leading, spacing: 4) {
                        if listing.repo { Text("A git repository, on \(listing.branch).") }
                        if listing.files > 0 { Text("\(listing.files) files aren’t shown.") }
                    }
                }
            }
        }
        .navigationTitle(folderName(shown))
        .navigationSubtitle(machine)
        .navigationBarTitleDisplayMode(.inline)
        .safeAreaInset(edge: .bottom) {
            Button {
                if let listing { choose(listing.path, listing) }
            } label: {
                Text(shown == "~" ? "Open a Folder to Choose It" : "Choose “\(folderName(shown))”").lineLimit(1).frame(maxWidth: .infinity)
            }
            .buttonStyle(.glassProminent).controlSize(.large)
            .disabled(listing == nil || shown == "~")
            .padding(.horizontal, 20).padding(.bottom, 8)
            .accessibilityIdentifier("choose-folder")
        }
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Menu {
                    Toggle("Show Hidden Folders", isOn: $hidden)
                    Button("Type a Path…", systemImage: "character.cursor.ibeam") { typing = true }
                } label: {
                    Label("More", systemImage: "ellipsis")
                }
                .accessibilityIdentifier("folder-menu")
            }
        }
        .task(id: hidden) { await load() }
        .alert("Type a Path", isPresented: $typing) {
            TextField("~/code/my-app", text: $typed).textInputAutocapitalization(.never).autocorrectionDisabled()
            Button("Cancel", role: .cancel) {}
            Button("Go") { go(typed.trimmingCharacters(in: .whitespaces)) }
        } message: {
            Text("A folder on \(machine).")
        }
        .alert("Open “\(asking?.name ?? "")”?", isPresented: Binding(get: { asking != nil }, set: { if !$0 { asking = nil } }), presenting: asking) { f in
            Button("Cancel", role: .cancel) {}
            Button("Open") { browsing.append(f.path) }
        } message: { _ in
            Text("macOS asks on \(machine) whether Hermesh may read this folder. Someone at that Mac has to click Allow.")
        }
    }

    /// Where this folder is, each folder above it one tap away.
    private var trail: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 4) {
                ForEach(Array(folderTrail(shown).enumerated()), id: \.offset) { i, p in
                    if i > 0 { Image(systemName: "chevron.compact.right").foregroundStyle(.tertiary) }
                    if p == shown {
                        Text(folderName(p)).fontWeight(.semibold).foregroundStyle(.primary)
                    } else {
                        Button(folderName(p)) { jump(p) }.buttonStyle(.borderless)
                    }
                }
            }
            .font(.subheadline)
        }
        .defaultScrollAnchor(.trailing)
        .textCase(nil)
    }

    private func load() async {
        failure = nil
        do {
            listing = try await model.api?.folders(machine: machineID, path: path, hidden: hidden)
        } catch let e as APIError where e.code == "unimplemented" {
            tooOld = true
            failure = String(localized: "Hermesh on \(machine) is too old to browse its folders. Update it there, or type a path.")
        } catch let e as APIError where e.code == "permission_denied" {
            failure = String(localized: "macOS didn’t let Hermesh read this folder. On that Mac, allow it in System Settings › Privacy & Security › Files & Folders.")
        } catch {
            failure = error.localizedDescription
        }
    }

    private func open(_ f: Hermesh_Machine_V1_Folder) {
        if f.protected { asking = f } else { browsing.append(f.path) }
    }

    /// A folder above this one: back to it when it was browsed, else to it alone.
    private func jump(_ p: String) {
        if let i = browsing.firstIndex(of: p) { browsing = Array(browsing[...i]) } else { browsing = [p] }
    }

    private func go(_ p: String) {
        guard !p.isEmpty else { return }
        if tooOld { choose(p, nil) } else { browsing.append(p) }
    }
}

/// A folder in a list: its icon (marked when it is a git repository or
/// macOS protects it), name and a line under it.
struct FolderLabel: View {
    let name: String
    let detail: String
    var repo = false
    var protected = false
    var large = false

    var body: some View {
        HStack(spacing: 14) {
            Image(systemName: "folder.fill").font(large ? .title : .title2).foregroundStyle(.tint)
                .overlay(alignment: .bottomTrailing) {
                    if repo || protected {
                        Image(systemName: repo ? "arrow.triangle.branch" : "lock.fill")
                            .font(.system(size: 8, weight: .bold)).foregroundStyle(.secondary)
                            .padding(3).background(Color(.secondarySystemGroupedBackground), in: Circle())
                            .offset(x: 5, y: 4)
                    }
                }
                .frame(width: 34)
            VStack(alignment: .leading, spacing: 2) {
                Text(name).fontWeight(large ? .semibold : .regular).foregroundStyle(.primary).lineLimit(1)
                if !detail.isEmpty {
                    Text(detail).font(large ? .caption.monospaced() : .footnote).foregroundStyle(.secondary)
                        .lineLimit(1).truncationMode(.middle)
                }
            }
        }
    }
}

/// A folder path's last part, as the person writes paths: "app" of "~/code/app".
func folderName(_ path: String) -> String {
    if path == "~" { return String(localized: "Home") }
    // A machine's own separators: "/", or "\" on Windows.
    return path.split(whereSeparator: { $0 == "/" || $0 == "\\" }).last.map(String.init) ?? path
}

/// The folder a folder is in: "~" holds "~/code", "/" holds "/opt".
func parentFolder(_ path: String) -> String {
    guard let i = path.lastIndex(of: "/") else { return path }
    let p = String(path[..<i])
    return p.isEmpty ? "/" : p
}

/// A folder and those above it, outermost first.
func folderTrail(_ path: String) -> [String] {
    var out = [path]
    while let last = out.last, parentFolder(last) != last { out.append(parentFolder(last)) }
    return out.reversed()
}
