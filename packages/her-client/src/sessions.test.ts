import assert from "node:assert/strict";
import { test } from "node:test";
import { displayTitle, listView, mergeSessions, projectNames, projectsOf, recentProjects, searchFilter, searchSessions, sections, type ListSession } from "./sessions.ts";

const now = new Date(2026, 9, 28, 15, 0);
const at = (daysAgo: number, hour = 12) => new Date(2026, 9, 28 - daysAgo, hour).toISOString();

function session(id: string, o: Partial<ListSession> = {}): ListSession {
  return { session_id: id, cwd: "/w/app", created_at: at(0), updated_at: at(0), state: "idle", archived: false, loaded: true, busy: false, attached_clients: 0, ...o };
}

const list: ListSession[] = [
  session("idle-old", { updated_at: at(10) }),
  session("asks", { state: "awaiting_approval", updated_at: at(0, 9) }),
  session("runs", { state: "running", updated_at: at(0, 14) }),
  session("failed", { state: "failed", updated_at: at(1) }),
  session("idle-new", { updated_at: at(0, 13), cwd: "/w/web" }),
  session("old-busy", { busy: true, updated_at: at(3) }), // busy without a state: running
  session("gone", { archived: true, updated_at: at(0, 11) }),
];
const ids = (l: ListSession[]) => l.map((s) => s.session_id);

test("a view not chosen yet: by status, newest first, titles only", () => {
  assert.deepEqual(listView(), { group: "status", newestFirst: true, metadata: false });
});

test("by status: what needs the owner, then what runs, then the rest; archived ones only when shown", () => {
  const s = sections(list, listView(), { archived: false, now });
  assert.deepEqual(s.map((x) => x.key), ["attention", "active", "done"]);
  assert.deepEqual(ids(s[0].sessions), ["asks", "failed"]);
  assert.deepEqual(ids(s[1].sessions), ["runs", "old-busy"]);
  assert.deepEqual(ids(s[2].sessions), ["idle-new", "idle-old"]);
  const shown = sections(list, listView(), { archived: true, now });
  assert.deepEqual(shown.at(-1)?.key, "archived");
});

test("by date: today, yesterday, the six days before, earlier; reversed with oldest first", () => {
  const s = sections(list, listView({ group: "date" }), { archived: false, now });
  assert.deepEqual(s.map((x) => x.key), ["today", "yesterday", "week", "earlier"]);
  assert.deepEqual(ids(s[0].sessions), ["runs", "idle-new", "asks"]);
  const old = sections(list, listView({ group: "date", newestFirst: false }), { archived: false, now });
  assert.deepEqual(old.map((x) => x.key), ["earlier", "week", "yesterday", "today"]);
  assert.deepEqual(ids(old.at(-1)!.sessions), ["asks", "idle-new", "runs"]);
});

test("by project: projects by name, each by last activity", () => {
  const s = sections(list, listView({ group: "project" }), { archived: false, now });
  assert.deepEqual(s.map((x) => x.key), ["/w/app", "/w/web"]);
});

test("projects that share a name are told apart by the folder above", () => {
  const n = projectNames(["/a/app", "/b/app", "/c/web"]);
  assert.equal(n.get("/a/app"), "app · a");
  assert.equal(n.get("/b/app"), "app · b");
  assert.equal(n.get("/c/web"), "web");
});

test("search ranks what needs the owner first, then what runs, then by time; filters narrow it", () => {
  assert.deepEqual(ids(searchSessions(list, "", searchFilter())).slice(0, 4), ["asks", "failed", "runs", "old-busy"]);
  assert.deepEqual(ids(searchSessions(list, "", searchFilter({ project: "/w/web" }))), ["idle-new"]);
  assert.ok(!ids(searchSessions(list, "", searchFilter())).includes("gone"));
  assert.ok(ids(searchSessions(list, "", searchFilter({ archived: true }))).includes("gone"));
  assert.deepEqual(ids(searchSessions([session("t", { title: "修复审批规则" }), session("u", { title: "其他" })], "审批", searchFilter())), ["t"]);
});

test("changes replace a session or add it; deleted ones leave", () => {
  const next = mergeSessions(list, [session("runs", { state: "idle" }), session("new")], ["failed"]);
  assert.equal(next.find((s) => s.session_id === "runs")?.state, "idle");
  assert.ok(next.some((s) => s.session_id === "new"));
  assert.ok(!next.some((s) => s.session_id === "failed"));
  assert.equal(mergeSessions(list, []), list);
});

test("recent projects: the most recently used first, each once", () => {
  assert.deepEqual(recentProjects(list), ["/w/app", "/w/web"]);
});

test("sessions without a project: a group of their own after the projects, and never a recent project", () => {
  const chat = session("chat", { cwd: "/h/.her/chats/0193", no_project: true, updated_at: at(0, 14) });
  const all = [...list, chat];
  const s = sections(all, listView({ group: "project" }), { archived: false, now });
  assert.deepEqual(s.map((x) => x.key), ["/w/app", "/w/web", ""]);
  assert.deepEqual(ids(s[2].sessions), ["chat"]);
  assert.deepEqual(recentProjects(all), ["/w/app", "/w/web"]);
  assert.deepEqual(projectsOf(all), ["/w/app", "/w/web"]);
  assert.equal(displayTitle(chat), "", "not the folder's made-up name");
});

