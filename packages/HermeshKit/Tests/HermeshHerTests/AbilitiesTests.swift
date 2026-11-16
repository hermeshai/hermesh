import Foundation
import Testing
@testable import HermeshHer

// What Her's pages make of the console's answers: skills, web search, the browsers, storage and the model sign-ins.

private func skill(_ name: String, scope: String = "user", state: String = "active", removable: Bool = true, source: String? = "upload") -> JSON {
    ["name": .string(name), "path": .string("/Users/me/.her/skills/\(name)/SKILL.md"), "scope": .string(scope), "state": .string(state),
     "description": .string("What \(name) does"), "removable": .bool(removable), "model_invocable": true, "default_enabled": true,
     "source": source.map { ["kind": .string($0)] } ?? nil, "detail": state == "invalid" ? "SKILL.md has no name" : nil]
}

@Test func skillsAreTheEnvironmentsAndByGroup() {
    let s = HerSkills(["enabled": true, "warnings": [], "skills": [
        skill("weekly-report"),
        skill("brand-voice", source: nil),
        skill("meeting-notes", state: "disabled"),
        skill("skill-creator", scope: "system", removable: false, source: nil),
        skill("old-copy", state: "shadowed"),
        skill("broken", state: "invalid"),
        skill("repo-only", scope: "project"),
    ]])
    #expect(s.skills.map(\.name) == ["weekly-report", "brand-voice", "meeting-notes", "skill-creator"])
    #expect(s.group(.user).count == 3 && s.group(.system).map(\.name) == ["skill-creator"])
    #expect(s.onCount == 3)
    #expect(s.broken.map(\.name) == ["broken"] && s.broken[0].problem == "SKILL.md has no name")
    let w = s.skills[0]
    #expect(w.dir == "/Users/me/.her/skills/weekly-report" && w.source == .upload && w.on && w.command == "/skill:weekly-report")
    #expect(s.skills[1].source == .local && s.skills[3].source == .system && !s.skills[2].on)
}

@Test func searchSwitchesAndOrderAreWrittenAsTheConsoleWritesThem() {
    let s = HerSearch(["enabled": true, "current": "exa", "sources": [
        ["id": "exa", "name": "Exa", "configured": true, "enabled": true, "needs": "nothing"],
        ["id": "deepseek", "name": "DeepSeek", "configured": true, "enabled": false, "needs": "account", "key": "account"],
        ["id": "tavily", "name": "Tavily", "configured": false, "enabled": true, "needs": "key", "key_url": "https://tavily.com",
         "failure": ["kind": "auth_failed", "at_ms": 1_763_000_000_000]],
    ]])
    #expect(s.source(s.current)?.name == "Exa" && !s.noneLeft)
    #expect(s.sources[2].wantsKey && !s.sources[0].wantsKey && s.sources[2].failure?.kind == "auth_failed")
    #expect(s.switchEdits("exa", on: false) == [["key_path": "web_search.disabled", "value": ["exa", "deepseek"]]])
    #expect(s.switchEdits("deepseek", on: true) == [["key_path": "web_search.disabled", "value": []]])
    #expect(s.moved(from: IndexSet(integer: 2), to: 0) == ["tavily", "exa", "deepseek"])
    #expect(s.moved(from: IndexSet(integer: 0), to: 3) == ["deepseek", "tavily", "exa"])
    #expect(HerSearch.orderEdits(["tavily", "exa"]) == [["key_path": "web_search.order", "value": ["tavily", "exa"]]])
}

@Test func theBrowserPageReadsStatusAndConfig() {
    let none = HerBrowser(status: ["embedded": false, "window": false, "os": "linux"], config: [:])
    #expect(!none.controlling && none.yours == .off && none.allSites == .ask && none.exceptions.isEmpty && none.os == "linux")

    let used = HerBrowser(status: ["embedded": false, "window": true, "program": "Google Chrome", "extension": ["browser": "Google Chrome 141"]],
                          config: ["browser": ["extension": true, "default": "yours", "sites": ["*": "navigate", "https://github.com": "act"]]])
    #expect(used.controlling && used.yours == .on && used.opensInYours && used.chrome == "Google Chrome 141")
    #expect(used.allSites == .navigate && used.exceptions.map(\.origin) == ["https://github.com"] && used.exceptions[0].level == .act)

    let lost = HerBrowser(status: ["embedded": false, "window": false], config: ["browser": ["extension": true, "default": "yours", "enabled": false]])
    #expect(lost.yours == .lost && !lost.opensInYours && !lost.controlling)
    let old = HerBrowser(status: ["embedded": true, "window": false, "extension": ["problem": "update_extension"]], config: [:])
    #expect(old.yours == .outdated && old.controlling && old.chrome == nil)

    #expect(HerBrowser.allSitesEdits(.ask) == [["key_path": "browser.sites.\"*\"", "value": nil]])
    #expect(HerBrowser.siteEdits("https://github.com", .act) == [["key_path": "browser.sites.\"https://github.com\"", "value": "act"]])
}

@Test func aSiteIsKeptByItsOrigin() {
    #expect(herSiteOrigin("github.com") == "https://github.com")
    #expect(herSiteOrigin(" https://GitHub.com/a/b?c ") == "https://github.com")
    #expect(herSiteOrigin("http://localhost:3000/x") == "http://localhost:3000")
    #expect(herSiteOrigin("https://example.com:443") == "https://example.com")
    #expect(herSiteOrigin("ftp://example.com") == nil && herSiteOrigin("") == nil && herSiteOrigin("https://u:***@example.com") == nil)
}

@Test func storageIsReadAndSortedBySize() {
    let s = HerStorage(["home": "/Users/me/.her", "custom": false, "total": 3000, "parts": [
        ["kind": "files", "bytes": 1000], ["kind": "conversations", "bytes": 1800], ["kind": "memory", "bytes": 200],
    ]])
    #expect(s.home == "/Users/me/.her" && !s.custom && s.total == 3000)
    #expect(s.bySize.map(\.kind) == ["conversations", "files", "memory"])
}

@Test func theModelsPageGroupsProviders() {
    let j: JSON = ["models": [
        ["id": "deepseek/deepseek-chat", "name": "DeepSeek Chat", "provider": "deepseek", "available": true, "shown": true],
        ["id": "deepseek/deepseek-reasoner", "name": "DeepSeek Reasoner", "provider": "deepseek", "available": true, "shown": false],
    ], "providers": [
        ["id": "deepseek", "name": "DeepSeek", "auth": "api_key", "credential": "stored", "listing": ["models": 2, "shown": 1, "refreshing": false, "source": "provider"]],
        ["id": "openai", "name": "OpenAI", "auth": "api_key", "credential": "missing"],
        ["id": "anthropic", "name": "Anthropic", "auth": "api_key", "credential": "env", "credential_source": "ANTHROPIC_API_KEY"],
        ["id": "ollama", "name": "Ollama", "auth": "none", "credential": "not_required"],
        ["id": "zhipu-coding-plan", "name": "GLM", "auth": "api_key", "credential": "stored", "plan": ["api": "openai-chat", "base_url": "https://x", "dynamic_models": false, "key_url": "https://k"]],
        ["id": "bailian-token-plan", "name": "Bailian", "auth": "api_key", "credential": "missing", "plan": ["api": "openai-chat", "base_url": "https://y", "dynamic_models": true, "key_url": "https://k2"]],
    ]]
    let p = HerProviders(j)
    #expect(p.keyed.map(\.id) == ["deepseek", "anthropic"])
    #expect(p.keyChoices.map(\.id) == ["openai"])
    #expect(p.plans.map(\.id) == ["bailian-token-plan", "zhipu-coding-plan"])
    #expect(p.addedPlans.map(\.id) == ["zhipu-coding-plan"] && p.plansLeft.map(\.id) == ["bailian-token-plan"])
    #expect(p.keyed[0].models == 2 && p.keyed[0].shown == 1 && p.keyed[0].removable && !p.keyed[1].removable && p.keyed[1].credentialSource == "ANTHROPIC_API_KEY")
    #expect(herProviderModels(j, provider: "deepseek").map(\.shown) == [true, false])
}

@Test func theChatGPTSignInIsRead() {
    let c = HerChatGPT(["state": "connected", "account": "me@example.com", "connection": ["state": "ready", "provider_id": "chatgpt"]])
    #expect(c.ready && !c.expired && c.account == "me@example.com" && c.provider == "chatgpt")
    #expect(HerChatGPT(["state": "idle", "connection": ["state": "needs_login"]]).expired)
    #expect(HerChatGPT(["state": "idle"]).connection == nil)
}

@Test func thePagesCallsAreHersWithTheConsolesParameters() async throws {
    let r = Recorder()
    let calls = HerAbilityCalls(call: r.calls_.call)
    let s = HerSkills(["skills": [skill("weekly-report")]]).skills[0]
    try await calls.setSkill(s, on: false)
    try await calls.removeSkill(s)
    try await calls.write(HerSearch.enabledEdits(false))
    try await calls.removeSearchKey("tavily")
    _ = try await calls.setKey("openai", key: "  sk-1 \n")
    #expect(r.calls.map(\.0) == ["skills/set_enabled", "skills/remove", "config/write", "credentials/delete", "auth/set"])
    #expect(r.calls[0].1 == ["path": "/Users/me/.her/skills/weekly-report", "enabled": false])
    #expect(r.calls[2].1 == ["layer": "user", "edits": [["key_path": "web_search.enabled", "value": false]]])
    #expect(r.calls[3].1 == ["name": "web-search.tavily"])
    #expect(r.calls[4].1 == ["provider": "openai", "api_key": "sk-1"])
}
