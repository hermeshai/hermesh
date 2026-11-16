import HermeshHer
import SwiftUI

// The connectors the page offers (docs/her-connectors-v2-demo.html).

/// A connector the page offers: an address put to her's own check and reached.
struct HerCatalogEntry: Identifiable {
    let id: String
    let name: String
    let url: String
    let about: LocalizedStringResource
    /// It needs no sign-in, and connects where its row stands. The others sign in with an account.
    var open = false
    /// Its icon is a full square of its own, edge to edge.
    var fills = false
    /// Where the service makes a token, for an owner who would sooner use one.
    var tokenPage: URL?

    /// The way it signed in when it was checked: what its sheet says before her has asked again.
    var way: HerConnectWay { open ? .open : .account }
}

struct HerCatalogGroup: Identifiable {
    let id: String
    let title: LocalizedStringResource
    let entries: [HerCatalogEntry]
}

/// What the page offers. Each was put to her's own check (`her mcp add`) on 2026-11-16, and one step further: one
/// that signs in with an account is one whose sign-in page took her's client (GitHub: her's built-in app); one that
/// needs no sign-in answered a call to one of its tools.
let herConnectorCatalog: [HerCatalogGroup] = [
    HerCatalogGroup(id: "development", title: "Development", entries: [
        HerCatalogEntry(id: "github", name: "GitHub", url: "https://api.githubcopilot.com/mcp/", about: "Repositories, issues, pull requests and code search",
                        fills: true, tokenPage: URL(string: "https://github.com/settings/personal-access-tokens/new")),
        HerCatalogEntry(id: "sentry", name: "Sentry", url: "https://mcp.sentry.dev/mcp", about: "Errors, performance issues and releases", fills: true),
        HerCatalogEntry(id: "vercel", name: "Vercel", url: "https://mcp.vercel.com", about: "Projects, deployments and logs", fills: true),
        HerCatalogEntry(id: "neon", name: "Neon", url: "https://mcp.neon.tech/mcp", about: "Postgres databases and branches", fills: true),
        HerCatalogEntry(id: "posthog", name: "PostHog", url: "https://mcp.posthog.com/mcp", about: "Product analytics, dashboards and feature flags", fills: true),
        HerCatalogEntry(id: "amplitude", name: "Amplitude", url: "https://mcp.amplitude.com/mcp", about: "Product analytics, charts and cohorts"),
    ]),
    HerCatalogGroup(id: "work", title: "Work", entries: [
        HerCatalogEntry(id: "notion", name: "Notion", url: "https://mcp.notion.com/mcp", about: "Pages, databases and comments"),
        HerCatalogEntry(id: "linear", name: "Linear", url: "https://mcp.linear.app/mcp", about: "Issues, projects and cycles", fills: true),
        HerCatalogEntry(id: "airtable", name: "Airtable", url: "https://mcp.airtable.com/mcp", about: "Bases, tables and records"),
        HerCatalogEntry(id: "canva", name: "Canva", url: "https://mcp.canva.com/mcp", about: "Designs, templates and exports"),
        HerCatalogEntry(id: "monday", name: "monday.com", url: "https://mcp.monday.com/mcp", about: "Boards, items and updates"),
        HerCatalogEntry(id: "todoist", name: "Todoist", url: "https://ai.todoist.net/mcp", about: "Tasks, projects and due dates"),
        HerCatalogEntry(id: "granola", name: "Granola", url: "https://mcp.granola.ai/mcp", about: "Meeting notes and transcripts", fills: true),
        HerCatalogEntry(id: "fireflies", name: "Fireflies", url: "https://api.fireflies.ai/mcp", about: "Meeting transcripts and summaries"),
    ]),
    HerCatalogGroup(id: "reference", title: "Search & Reference", entries: [
        HerCatalogEntry(id: "context7", name: "Context7", url: "https://mcp.context7.com/mcp", about: "Up-to-date library documentation for writing code",
                        open: true, fills: true),
        HerCatalogEntry(id: "deepwiki", name: "DeepWiki", url: "https://mcp.deepwiki.com/mcp", about: "Documentation and answers for public repositories",
                        open: true, fills: true),
        HerCatalogEntry(id: "gitmcp", name: "GitMCP", url: "https://gitmcp.io/docs", about: "Documentation of any GitHub repository", open: true),
        HerCatalogEntry(id: "exa", name: "Exa", url: "https://mcp.exa.ai/mcp", about: "Web search built for AI", open: true, fills: true),
        HerCatalogEntry(id: "tavily", name: "Tavily", url: "https://mcp.tavily.com/mcp", about: "Web search and page extraction", fills: true),
        HerCatalogEntry(id: "jina", name: "Jina", url: "https://mcp.jina.ai/v1", about: "Read web pages and search the web", open: true),
        HerCatalogEntry(id: "apify", name: "Apify", url: "https://mcp.apify.com", about: "Web scrapers and crawlers"),
        HerCatalogEntry(id: "huggingface", name: "Hugging Face", url: "https://huggingface.co/mcp", about: "Models, datasets and papers", open: true),
        HerCatalogEntry(id: "microsoft-learn", name: "Microsoft Learn", url: "https://learn.microsoft.com/api/mcp", about: "Microsoft’s official documentation",
                        open: true),
        HerCatalogEntry(id: "aws-knowledge", name: "AWS Knowledge", url: "https://knowledge-mcp.global.api.aws", about: "AWS documentation and guidance",
                        open: true, fills: true),
        HerCatalogEntry(id: "cloudflare-docs", name: "Cloudflare Docs", url: "https://docs.mcp.cloudflare.com/mcp", about: "Cloudflare’s developer documentation",
                        open: true),
    ]),
]

func herCatalogEntry(_ name: String) -> HerCatalogEntry? {
    herConnectorCatalog.lazy.flatMap(\.entries).first { $0.id == name }
}

/// The entry at this address, whatever its case or its last slash.
func herCatalogEntry(url: String) -> HerCatalogEntry? {
    func plain(_ s: String) -> String {
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        return t.hasSuffix("/") ? String(t.dropLast()) : t
    }
    return herConnectorCatalog.lazy.flatMap(\.entries).first { plain($0.url) == plain(url) }
}

/// A connector's name as the pages show it: the service's own, for one the page offers.
func herConnectorTitle(_ name: String) -> String { herCatalogEntry(name)?.name ?? name }

/// The host of an address, else the address.
func herHost(_ url: String) -> String {
    URL(string: url.trimmingCharacters(in: .whitespacesAndNewlines))?.host(percentEncoded: false) ?? url
}
