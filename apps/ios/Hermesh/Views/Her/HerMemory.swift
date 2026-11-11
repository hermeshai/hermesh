import HermeshHer
import SwiftUI

/// What an assistant keeps in its memory (docs/her-memory.md), as bots/memory gives it: its MEMORY.md as written,
/// and the other files of the folder.
struct HerMemory: Equatable {
    struct Page: Equatable, Identifiable {
        let name: String
        let path: String
        let modified: Date?
        var id: String { path }
    }

    let text: String
    let pages: [Page]

    var isEmpty: Bool { text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && pages.isEmpty }

    init(_ j: JSON) {
        text = j["text"].string ?? ""
        pages = (j["pages"].array ?? []).compactMap { p in
            guard let name = p["name"].string, let path = p["path"].string else { return nil }
            return Page(name: name, path: path, modified: p["modified_at"].string.flatMap { try? Date($0, strategy: .iso8601) })
        }
    }

    static func load(_ chat: HerChat, bot: String) async throws -> HerMemory {
        HerMemory(try await chat.call("bots/memory", ["bot_id": .string(bot)]))
    }
}

/// The memory on a page of its own, from Her's settings: MEMORY.md as the conversation renders Markdown, whatever
/// form it takes, and the pages it names, opened as a file Her sent is. Changing it goes through the chat.
struct HerMemoryPage: View {
    let chat: HerChat
    let bot: String
    @State private var memory: HerMemory?
    @State private var failed: String?
    @State private var preview: URL?
    @State private var page: HerFileText?
    @State private var opening: String?

    var body: some View {
        Group {
            if let memory, memory.isEmpty {
                ContentUnavailableView {
                    Label("Nothing Remembered Yet", systemImage: "bookmark")
                } description: {
                    Text("Your preferences, what you decide and where things are: Her notes them herself. You can also say “Remember …”.")
                }
            } else if let memory {
                List {
                    if !memory.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                        Section {
                            MarkdownText(text: memory.text).frame(maxWidth: .infinity, alignment: .leading).padding(.vertical, 6)
                                .textSelection(.enabled)
                                .accessibilityIdentifier("her-memory-text")
                        } footer: {
                            Text("This is what Her reads. To change something, tell her.")
                        }
                    }
                    if !memory.pages.isEmpty {
                        Section("Pages") {
                            ForEach(memory.pages) { p in
                                Button { open(p.path) } label: {
                                    LabeledContent(p.name) {
                                        if opening == p.path { ProgressView() } else if let d = p.modified { Text(d, format: .dateTime.month().day()) }
                                    }
                                }
                                .foregroundStyle(.primary)
                            }
                        }
                    }
                }
                .listSectionSpacing(.compact)
            } else if let failed {
                ContentUnavailableView("Can’t Read the Memory", systemImage: "exclamationmark.triangle", description: Text(failed))
            } else {
                ProgressView()
            }
        }
        .navigationTitle("Memory")
        .navigationBarTitleDisplayMode(.inline)
        .task {
            do { memory = try await HerMemory.load(chat, bot: bot) } catch { failed = error.localizedDescription }
        }
        .sheet(item: $page) { HerFilePage(file: $0) }
        .quickLookPreview($preview)
    }

    private func open(_ path: String) {
        guard opening == nil else { return }
        opening = path
        Task {
            await openHerFile(chat: chat, path: path, preview: $preview, page: $page)
            opening = nil
        }
    }
}
