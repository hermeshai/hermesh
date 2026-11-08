import { ExtensionBrowser } from "./browser.js";
import { bridge } from "./methods.js";

// The Hermesh extension (browser-design §8.4): a Native Messaging port to `her browser
// native-host`, which relays to the her service, and an ExtensionBrowser for each browser
// the kernel starts on it. A new stream number is a new browser; the one before is let go.
// The open port keeps this worker alive; an alarm brings it back if Chrome stopped it.
//
// This is what knows which tab is whose (§8.4.7): the kernel lets go of a tab when a task ends
// and when a connection drops, and finds it here again by its session. The service hears of
// every change, so the app can show a session's tabs whoever holds them.
//
// The store updates this extension apart from Hermesh, so the hello says which bridge
// versions it speaks (D-219): `bridge` from methods.js, down to BRIDGE_MIN, the oldest whose
// frames and commands it still handles. Keep BRIDGE_MIN low: a Hermesh app updates later
// than its extension. A kept-alive worker never takes an update by itself; it reloads for
// one while the agent has no tab here. Loaded from its folder, the app's update changes the
// files under it: the same reload, once the files are seen to differ.

const HOST = "com.hermesh.browser";
const PROTOCOL = "1.3";
const BRIDGE_MIN = 1;
const GROUP_TITLE = "Hermesh";
/** Chrome cuts a tab group's name long before this. */
const TITLE_CHARS = 30;
const RETRY_MIN = 1000;
const RETRY_MAX = 30000;
/** Reconnecting takes seconds (§8.4.7). Past this, as long as the app waits, it is worth saying. */
const STUCK_AFTER = 10000;

const msg = (name, ...args) => chrome.i18n.getMessage(name, args.map(String)) || name;

/** @type {chrome.runtime.Port | null} */
let port = null;
/** @type {{ stream: number, browser: ExtensionBrowser } | null} */
let live = null;
/** Pieces of a message over Chrome's 1 MB limit, by stream. */
const pieces = new Map();
/** What the host said last: "connecting", "connected", "not_open", "missing" or "outdated". */
let hostState = "connecting";
/** While "outdated": which side is too old, "extension" or "her". */
let tooOld = null;
/** "connecting" has lasted past STUCK_AFTER. */
let stuck = false;
let stuckTimer = null;
/** Whether the agent uses this browser and profile; another may be in use. */
let inUse = true;
let retries = 0;
let retryTimer = null;
/** Whether this port has said who it is. */
let helloSent = false;

// ---- Which tab is whose ----

/** Tab → its session and what it shows. @type {Map<number, {session: string, url: string, title: string}>} */
const agentTabs = new Map();
/** Session → the tab the agent used last, for showing that one. */
const lastUsed = new Map();
/** Tab group by her session. */
const groups = new Map();
/** Tabs the user offered to a conversation, not given to a session yet. */
const offered = new Set();
/** What the service says about the sessions with tabs here. @type {Record<string, {title: string, state: string, handoff?: string}>} */
let sessions = {};
/** The window the agent's tabs open in: its own, so the user's tabs are never taken. */
let windowId = null;

const targetOf = (tabId) => `T${tabId}`;
const tabOf = (targetId) => Number(String(targetId).slice(1));

function snapshot() {
  return { agentTabs: [...agentTabs], groups: [...groups], windowId, sessions };
}

function restore(saved) {
  for (const [tabId, tab] of saved.agentTabs ?? []) agentTabs.set(tabId, tab);
  for (const [session, groupId] of saved.groups ?? []) groups.set(session, groupId);
  windowId = saved.windowId ?? null;
  sessions = saved.sessions ?? {};
}

/** Kept while the browser runs, across this worker stopping. A reload clears it, so one carries it over. */
let saving = null;
function save() {
  clearTimeout(saving);
  saving = setTimeout(() => void chrome.storage?.session.set({ her: snapshot() }).catch(() => {}), 100);
}

const ready = (async () => {
  try {
    const { herCarried } = await chrome.storage.local.get("herCarried");
    const { her } = await chrome.storage.session.get("her");
    if (herCarried) await chrome.storage.local.remove("herCarried");
    const saved = her ?? herCarried;
    if (saved) restore(saved);
    // Tabs closed while this worker slept.
    const open = new Set((await chrome.tabs.query({})).map((t) => t.id));
    for (const tabId of agentTabs.keys()) if (!open.has(tabId)) agentTabs.delete(tabId);
  } catch {
    // An extension loaded under an older manifest has no storage; it reloads for the new one.
  }
})();

/** Tells the service each session's tabs, a moment after the last change. */
let reporting = null;
function report() {
  save();
  clearTimeout(reporting);
  reporting = setTimeout(reportNow, 100);
}

function reportNow() {
  const out = {};
  for (const [tabId, tab] of agentTabs) {
    (out[tab.session] ??= []).push({
      target_id: targetOf(tabId),
      url: tab.url,
      title: tab.title,
      attached: !!live?.browser.byTab.get(tabId)?.sessionId,
    });
  }
  postControl({ kind: "tabs", sessions: out });
  tellPage();
}

/** The extension's page, when it is open, draws again. */
function tellPage() {
  chrome.runtime.sendMessage({ changed: true }).catch(() => {});
}

function postControl(control) {
  // The hello comes first, for the host to tell the service who this is.
  if (!helloSent) return;
  try {
    port?.postMessage({ control });
  } catch {
    // The port just closed; what it would have said is said again on the next one.
  }
}

function remember(tabId, session, url = "", title = "") {
  const known = agentTabs.get(tabId);
  agentTabs.set(tabId, { session, url: url || known?.url || "", title: title || known?.title || "" });
  report();
}

function forget(tabId) {
  if (agentTabs.delete(tabId)) report();
}

// ---- The tabs of the agent ----

async function agentWindow() {
  if (windowId !== null) {
    try {
      await chrome.windows.get(windowId);
      return windowId;
    } catch {
      windowId = null;
    }
  }
  return null;
}

function groupLook(session) {
  const known = sessions[session];
  const title = (known?.title || GROUP_TITLE).slice(0, TITLE_CHARS);
  const waiting = known?.state === "awaiting_approval";
  // Orange asks for the user, blue is at work, grey has finished: the tabs stay theirs.
  const color = waiting ? "orange" : known?.state === "running" ? "blue" : "grey";
  return { title: waiting ? msg("groupWaiting", title) : title, color };
}

async function group(tabId, label) {
  const key = label ?? "";
  const known = groups.get(key);
  try {
    if (known !== undefined) {
      await chrome.tabs.group({ groupId: known, tabIds: [tabId] });
      return;
    }
  } catch {
    groups.delete(key);
  }
  try {
    const groupId = await chrome.tabs.group({ tabIds: [tabId] });
    groups.set(key, groupId);
    save();
    await chrome.tabGroups.update(groupId, groupLook(key));
  } catch {
    // Tab groups are a nicety; a tab outside one works the same.
  }
}

/** @type {import("./browser.js").Tabs} */
const tabs = {
  async create(url, label) {
    let tab;
    const existing = await agentWindow();
    if (existing === null) {
      const win = await chrome.windows.create({ url, focused: false, type: "normal" });
      windowId = win.id;
      tab = win.tabs[0];
    } else {
      tab = await chrome.tabs.create({ windowId: existing, url, active: true });
    }
    if (label) remember(tab.id, label, tab.pendingUrl ?? tab.url ?? url, tab.title);
    await group(tab.id, label);
    return tab.id;
  },
  adopt(tabId, label) {
    if (label) remember(tabId, label);
    void group(tabId, label);
  },
  close(tabId) {
    forget(tabId);
    chrome.tabs.remove(tabId).catch(() => {});
  },
  activate(tabId) {
    chrome.tabs.update(tabId, { active: true }).catch(() => {});
  },
  async attach(tabId) {
    await chrome.debugger.attach({ tabId }, PROTOCOL);
    report();
  },
  detach(tabId) {
    chrome.debugger.detach({ tabId }).catch(() => {});
    report();
  },
  send(tabId, child, method, params) {
    const tab = agentTabs.get(tabId);
    if (tab) lastUsed.set(tab.session, tabId);
    const target = child ? { tabId, sessionId: child } : { tabId };
    return chrome.debugger.sendCommand(target, method, params);
  },
  released(label) {
    return [...agentTabs].filter(([, tab]) => tab.session === label).map(([tabId, tab]) => ({ tabId, url: tab.url, title: tab.title }));
  },
};

chrome.debugger.onEvent.addListener((source, method, params) => {
  if (source.tabId !== undefined) live?.browser.event(source.tabId, source.sessionId, method, params ?? {});
});
chrome.debugger.onDetach.addListener((source, reason) => {
  if (source.tabId === undefined) return;
  live?.browser.detached(source.tabId, reason);
  // The user closed the "is debugging this browser" bar: the session keeps the tab, but may
  // not use it until they allow it again.
  const tab = agentTabs.get(source.tabId);
  if (reason === "canceled_by_user" && tab) postControl({ kind: "revoked", sessions: [tab.session] });
  report();
});
chrome.tabs.onCreated.addListener((tab) => live?.browser.tabCreated(tab.id, tab.openerTabId, tab.pendingUrl ?? tab.url));
chrome.tabs.onUpdated.addListener((tabId, change) => {
  if (change.url !== undefined || change.title !== undefined) {
    live?.browser.tabUpdated(tabId, change);
    const tab = agentTabs.get(tabId);
    if (tab) remember(tabId, tab.session, change.url, change.title);
  }
});
chrome.tabs.onRemoved.addListener((tabId) => {
  live?.browser.tabRemoved(tabId);
  offered.delete(tabId);
  forget(tabId);
});
chrome.windows.onRemoved.addListener((id) => {
  if (id === windowId) windowId = null;
});

// ---- What the service asks for ----

/** A session's tab in front: the one named, else the one the agent used last. */
async function reveal(sessionId, targetId) {
  let tabId = targetId ? tabOf(targetId) : lastUsed.get(sessionId);
  if (!agentTabs.has(tabId)) tabId = tabs.released(sessionId)[0]?.tabId;
  if (tabId === undefined) return;
  const tab = await chrome.tabs.update(tabId, { active: true });
  await chrome.windows.update(tab.windowId, { focused: true });
}

async function assign(targetId, sessionId) {
  const tabId = tabOf(targetId);
  if (!offered.delete(tabId)) return;
  const tab = await chrome.tabs.get(tabId);
  remember(tabId, sessionId, tab.url, tab.title);
  await group(tabId, sessionId);
}

function forgetSession(sessionId) {
  const ids = tabs.released(sessionId).map((t) => t.tabId);
  chrome.tabs.ungroup(ids).catch(() => {});
  for (const tabId of ids) forget(tabId);
  groups.delete(sessionId);
  lastUsed.delete(sessionId);
  delete sessions[sessionId];
  save();
}

function closeTabs(sessionId, targetId) {
  const ids = targetId ? [tabOf(targetId)] : tabs.released(sessionId).map((t) => t.tabId);
  for (const tabId of ids) tabs.close(tabId);
}

function onControl(control) {
  switch (control.kind) {
    case "reveal":
      reveal(control.session_id, control.target_id).catch(() => {});
      break;
    case "close_tabs":
      closeTabs(control.session_id, control.target_id);
      break;
    case "assign_tab":
      assign(control.target_id, control.session_id).catch(() => {});
      break;
    case "forget":
      forgetSession(control.session_id);
      break;
    case "sessions":
      sessions = control.sessions ?? {};
      save();
      for (const [session, groupId] of groups) chrome.tabGroups.update(groupId, groupLook(session)).catch(() => groups.delete(session));
      show();
      break;
    case "in_use":
      inUse = control.in_use;
      show();
      break;
  }
}

// ---- The host ----

/** Starts the wait of a "connecting" that goes on across retries, and ends it with any other state. */
function timeConnecting() {
  if (hostState !== "connecting") {
    clearTimeout(stuckTimer);
    stuckTimer = null;
    stuck = false;
  } else if (stuckTimer === null) {
    stuckTimer = setTimeout(() => {
      stuck = true;
      show();
    }, STUCK_AFTER);
  }
}

/** Only what asks something of the user shows on the icon: Hermesh not being open is fine. */
function show() {
  timeConnecting();
  const waiting = Object.values(sessions).filter((s) => s.state === "awaiting_approval").length;
  const text = waiting > 0 ? String(waiting) : hostState === "outdated" || stuck ? "!" : "";
  const title = waiting > 0 ? msg("titleWaiting") : msg({
    connected: inUse ? "titleConnected" : "titleStandby",
    connecting: stuck ? "titleStuck" : "titleConnecting",
    not_open: "titleNotOpen",
    missing: "titleMissing",
    outdated: "titleOutdated",
  }[hostState]);
  chrome.action.setTitle({ title }).catch(() => {});
  chrome.action.setBadgeText({ text }).catch(() => {});
  chrome.action.setBadgeBackgroundColor({ color: hostState === "outdated" && waiting === 0 ? "#d93025" : "#e8710a" }).catch(() => {});
  tellPage();
}

function letGo() {
  live?.browser.close();
  live = null;
  pieces.clear();
}

// ---- Updates ----

/** Set when the store has a newer version waiting, or the files under this folder changed. */
let updateWaiting = false;
/** What this worker's code was built from; for a folder the app keeps up to date. */
let build = null;

/** Restarts into the waiting update, unless the agent is using a tab here. */
function takeUpdate() {
  if (!updateWaiting || (live && live.browser.byTab.size > 0)) return;
  // A reload clears what is kept for the session: this tells the next worker. New files under the
  // manifest Chrome loaded earlier may have no storage yet; the reload is what brings it.
  const carried = chrome.storage?.local.set({ herCarried: snapshot() }) ?? Promise.resolve();
  carried.catch(() => {}).finally(() => chrome.runtime.reload());
}

chrome.runtime.onUpdateAvailable.addListener(() => {
  updateWaiting = true;
  takeUpdate();
});

const readJson = (name) => fetch(chrome.runtime.getURL(name), { cache: "no-store" }).then((r) => (r.ok ? r.json() : null)).catch(() => null);

/** The app replaces the folder when it updates: Chrome runs the old code until told. */
async function checkFiles() {
  if (chrome.runtime.getManifest().update_url) return; // From the store, which says so itself.
  const [manifest, built] = await Promise.all([readJson("manifest.json"), readJson("build.json")]);
  const before = build;
  build = built?.build ?? null;
  if ((manifest && manifest.version !== chrome.runtime.getManifest().version) || (before && build && before !== build)) {
    updateWaiting = true;
    takeUpdate();
  }
}

function onMessage(message) {
  if (message.control) {
    onControl(message.control);
    return;
  }
  if (typeof message.status === "string") {
    // The service came or went: a browser of the connection before is gone, and the new
    // connection numbers its streams from the start again. The tabs stay this session's.
    letGo();
    if (message.status === "outdated") {
      hostState = "outdated";
      tooOld = message.update ?? null;
      // Too old for this Hermesh: ask the store now rather than within hours.
      if (message.update === "extension") chrome.runtime.requestUpdateCheck?.().catch(() => {});
    } else if (message.status === "connected") {
      hostState = "connected";
      retries = 0;
      report();
    } else {
      hostState = "not_open";
    }
    show();
    takeUpdate();
    return;
  }
  const stream = message.stream;
  if (typeof stream !== "number") return;
  // Streams count up while the host stays connected (a reconnect says so first, which lets
  // the browser go): a late message of a browser that has ended is dropped.
  if (live && stream < live.stream) return;
  let data = message.data;
  if (typeof message.part === "string") {
    const soFar = (pieces.get(stream) ?? "") + message.part;
    if (message.more) {
      pieces.set(stream, soFar);
      return;
    }
    pieces.delete(stream);
    // Pieces are of the message's JSON text.
    try {
      data = JSON.parse(soFar);
    } catch {
      return;
    }
  }
  if (typeof data !== "object" || data === null) return;
  if (live?.stream !== stream) {
    letGo();
    const out = (message) => port?.postMessage({ stream, data: message });
    live = { stream, browser: new ExtensionBrowser(tabs, out) };
  }
  live.browser.receive(data);
}

/** Which browser and profile this is, kept so the app can tell them apart. */
async function instance() {
  const { herInstance } = await chrome.storage.local.get("herInstance");
  if (herInstance) return herInstance;
  const id = crypto.randomUUID();
  await chrome.storage.local.set({ herInstance: id });
  return id;
}

/** Tells the host which browser this is, so Hermesh can show it. */
async function hello() {
  let browser = navigator.userAgent;
  try {
    // Brands name Edge, Brave and Chrome apart; the generic ones ("Chromium", "Not A;Brand") last.
    const data = await navigator.userAgentData.getHighEntropyValues(["fullVersionList"]);
    const brands = (data.fullVersionList ?? []).filter((b) => !/Not.?A.?Brand/i.test(b.brand));
    const named = brands.find((b) => b.brand !== "Chromium") ?? brands[0];
    if (named) browser = `${named.brand} ${named.version}`;
  } catch {
    // The user agent string says enough.
  }
  const account = await chrome.identity?.getProfileUserInfo({ accountStatus: "ANY" }).then((u) => u.email, () => "");
  port?.postMessage({
    hello: {
      browser,
      extension_version: chrome.runtime.getManifest().version,
      bridge: { min: BRIDGE_MIN, max: bridge },
      instance: await instance().catch(() => undefined),
      account: account || undefined,
    },
  });
  helloSent = true;
}

function connect() {
  clearTimeout(retryTimer);
  if (port) return;
  try {
    port = chrome.runtime.connectNative(HOST);
  } catch {
    hostState = "missing";
    show();
    return;
  }
  helloSent = false;
  port.onMessage.addListener(onMessage);
  void hello();
  port.onDisconnect.addListener(() => {
    const error = chrome.runtime.lastError?.message;
    port = null;
    helloSent = false;
    letGo();
    // Not there at all, or it stopped. A host the service refused exits on purpose: an update
    // replaced the program it runs, and the next one is the current one. So try again soon.
    hostState = error?.includes("not found") ? "missing" : "connecting";
    show();
    clearTimeout(retryTimer);
    retryTimer = setTimeout(connect, Math.min(RETRY_MAX, RETRY_MIN * 2 ** retries++));
  });
  // A host not installed stays that until one answers: each retry is not news.
  if (hostState !== "missing") hostState = "connecting";
  show();
}

// ---- This extension's page ----

/** The page's button: from a folder the files are already new; the store is asked, else the user. */
async function updateExtension() {
  if (!chrome.runtime.getManifest().update_url) {
    updateWaiting = true;
    takeUpdate();
    return;
  }
  const { status } = await chrome.runtime.requestUpdateCheck();
  if (status !== "update_available") await chrome.tabs.create({ url: `chrome://extensions/?id=${chrome.runtime.id}` });
}

async function pageState() {
  const [current] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
  const list = [...new Set([...agentTabs.values()].map((t) => t.session))].map((id) => ({
    id,
    title: sessions[id]?.title || GROUP_TITLE,
    state: sessions[id]?.state ?? "idle",
    handoff: sessions[id]?.handoff,
    tabs: tabs.released(id).length,
  }));
  return {
    host: hostState,
    inUse,
    stuck,
    update: tooOld,
    sessions: list,
    // A page of the user's that the agent may be given; chrome:// and the like cannot be attached.
    offerable: !!current && !agentTabs.has(current.id) && /^https?:/.test(current.url ?? ""),
  };
}

function pageAction(action) {
  switch (action.do) {
    case "back":
      postControl({ kind: "open_app" });
      postControl({ kind: "open_session", session_id: action.session });
      break;
    case "done":
      postControl({ kind: "done", session_id: action.session });
      break;
    case "stop":
      postControl({ kind: "stop", session_id: action.session });
      break;
    case "open_app":
      postControl({ kind: "open_app" });
      connect();
      break;
    case "use_this":
      postControl({ kind: "use_this" });
      break;
    case "retry":
      // The wait starts over, so the page says it is trying.
      clearTimeout(stuckTimer);
      stuckTimer = null;
      stuck = false;
      retries = 0;
      connect();
      show();
      break;
    case "update":
      return updateExtension();
    case "offer":
      return chrome.tabs.query({ active: true, lastFocusedWindow: true }).then(([tab]) => {
        if (!tab) return;
        offered.add(tab.id);
        postControl({ kind: "open_app" });
        postControl({ kind: "offer_tab", target_id: targetOf(tab.id), url: tab.url ?? "", title: tab.title ?? "" });
      });
  }
  return undefined;
}

chrome.runtime.onMessage.addListener((message, _sender, reply) => {
  if (message.get === "state") {
    ready.then(pageState).then(reply);
    return true;
  }
  Promise.resolve(pageAction(message)).then(() => reply({}));
  return true;
});

chrome.alarms.create("her-connect", { periodInMinutes: 0.5 });
chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === "her-connect") {
    connect();
    void checkFiles();
    takeUpdate();
  }
});
chrome.runtime.onStartup.addListener(() => ready.then(connect));
chrome.runtime.onInstalled.addListener(() => ready.then(connect));
void checkFiles();
ready.then(connect);
