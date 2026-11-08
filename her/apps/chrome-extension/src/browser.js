import { TargetRelay } from "./relay.js";

// The browser end of the kernel's CDP stream in the user's Chrome (browser-design §8.4).
// chrome.debugger only attaches to a tab; TargetRelay answers the browser-level commands, and
// this class says how a tab of the agent's opens, attaches and goes.
//
// Only tabs the agent opened, and tabs those tabs opened, are ever attached: the user's
// other tabs are never seen. Nothing here touches the chrome.* APIs: they come in as a
// `Tabs`, so tests can stand in for them.

/**
 * @typedef {object} Tabs
 * @property {(url: string, label?: string) => Promise<number>} create  A new tab of the agent's; its id.
 * @property {(tabId: number, label?: string) => void} adopt  A tab an agent tab opened joins its group.
 * @property {(tabId: number) => void} close
 * @property {(tabId: number) => void} activate
 * @property {(tabId: number) => Promise<void>} attach  Attaches the debugger.
 * @property {(tabId: number) => void} detach
 * @property {(tabId: number, child: string | undefined, method: string, params: object) => Promise<unknown>} send
 * @property {(label: string) => {tabId: number, url: string, title: string}[]} released  A session's tabs, as kept across browsers.
 */

/** @typedef {import("./relay.js").Target & {tabId: number, url: string, title: string}} Tab */

export class ExtensionBrowser extends TargetRelay {
  /**
   * @param {Tabs} tabs
   * @param {(message: object) => void} send  Delivers one CDP message (a response or an event) to the kernel.
   */
  constructor(tabs, send) {
    super(send, "the Hermesh extension");
    this.tabs = tabs;
    /** @type {Map<number, Tab>} */
    this.byTab = new Map();
  }

  // ---- What Chrome reports ----

  /** A debugger event of a tab; `child` for a child target the tab attached (an iframe). */
  event(tabId, child, method, params) {
    const t = this.byTab.get(tabId);
    if (t) this.pageEvent(t, method, params, child);
  }

  /** The debugger let go: the user closed the "is debugging this browser" bar, or the tab
   * crashed or went somewhere Chrome does not let extensions in. The tab stays open, but
   * it is no longer the agent's. */
  detached(tabId, reason) {
    const t = this.byTab.get(tabId);
    if (!t) return;
    const why = reason === "canceled_by_user" ? "the user stopped the agent's control of the tab" : `the debugger detached (${reason})`;
    this.drop(t, why, "gone");
  }

  /** A tab was opened. One that a tab of the agent's opened is the agent's too, owned like
   * its opener (D-205); any other is the user's and stays unseen. */
  tabCreated(tabId, openerTabId, url) {
    if (this.closed || this.byTab.has(tabId)) return;
    const opener = openerTabId === undefined ? undefined : this.byTab.get(openerTabId);
    if (!opener) return;
    const t = this.register(tabId, url || "about:blank", opener.herSession, opener.targetId);
    this.tabs.adopt(tabId, t.herSession);
  }

  tabUpdated(tabId, { url, title }) {
    const t = this.byTab.get(tabId);
    if (!t) return;
    if (typeof url === "string") t.url = url;
    if (typeof title === "string") t.title = title;
    this.changed(t);
  }

  tabRemoved(tabId) {
    const t = this.byTab.get(tabId);
    if (t) this.drop(t, "the tab was closed", "gone");
  }

  // ---- TargetRelay ----

  forward(t, child, method, params) {
    return this.tabs.send(t.tabId, child, method, params);
  }

  async createTarget(url, herSession) {
    const tabId = await this.tabs.create(url, herSession);
    if (this.closed) {
      this.tabs.close(tabId);
      throw new Error("the browser closed");
    }
    return (this.byTab.get(tabId) ?? this.register(tabId, url, herSession)).targetId;
  }

  async attachPage(t) {
    await this.tabs.attach(t.tabId);
    if (!this.targets.has(t.targetId)) {
      this.tabs.detach(t.tabId);
      throw new Error("the tab was closed");
    }
  }

  /** A tab the kernel closes is closed; at the end, and when a session lets a tab go, the
   * debugger lets go and leaves it open for the user; one that went by itself needs nothing. */
  release(t, how) {
    this.byTab.delete(t.tabId);
    if (how === "close") this.tabs.close(t.tabId);
    else if ((how === "shutdown" || how === "release") && t.sessionId) this.tabs.detach(t.tabId);
  }

  activate(t) {
    this.tabs.activate(t.tabId);
  }

  // Downloads go where the user's Chrome puts them; the kernel is not told.

  /** A session's tabs that no browser of the kernel holds now: the ones it let go, those of a
   * connection that dropped, and the ones the user gave it (§8.4.7). */
  listTargets(params) {
    const label = typeof params.herSession === "string" ? params.herSession : undefined;
    if (!label) return [];
    for (const { tabId, url, title } of this.tabs.released(label)) {
      if (!this.byTab.has(tabId)) this.register(tabId, url, label).title = title;
    }
    return [...this.byTab.values()].filter((t) => t.herSession === label && !t.sessionId).map((t) => this.info(t));
  }

  describe(t) {
    return { url: t.url, title: t.title };
  }

  register(tabId, url, herSession, openerId) {
    /** @type {Tab} */
    const t = { targetId: `T${tabId}`, tabId, url, title: "", herSession, openerId };
    this.byTab.set(tabId, t);
    return this.add(t);
  }
}
