import { browserMethods, pageMethods } from "./methods.js";

// The browser end of the kernel's CDP stream, shared by the desktop app's embedded browser
// (apps/desktop/src/main/browser/shim.ts) and the Hermesh extension (browser.js), so the
// two answer the kernel the same way (browser-design §8.3.2, §8.4). A page's debugger only
// knows its own page, so the browser-level Target and Browser commands her-browser sends are
// answered here (D-210); page-level ones go to the page's debugger, and only those on the
// list (D-192). What a page is and how it opens, attaches and closes is the subclass's.
//
// This file loads as is in the extension, with no build step; the desktop app imports it
// from here. Its types are in relay.d.ts.

const browserLevel = new Set(browserMethods);
const pageLevel = new Set(pageMethods);

/**
 * @typedef {object} Target
 * @property {string} targetId
 * @property {string} [sessionId]  Set once the kernel attached.
 * @property {string} [herSession]
 * @property {string} [openerId]
 * @property {boolean} [byPerson]  Opened by the person, not by a page.
 */

export class TargetRelay {
  /**
   * @param {(message: object) => void} send  Delivers one CDP message (a response or an event) to the kernel.
   * @param {string} name  The browser, as errors name it: "the embedded browser".
   */
  constructor(send, name) {
    this.send = send;
    this.name = name;
    /** @type {Map<string, Target>} */
    this.targets = new Map();
    /** Sessions the kernel addresses: a page's own, or a child target of a page. @type {Map<string, {target: Target, child?: string}>} */
    this.sessions = new Map();
    this.next = 1;
    this.closed = false;
  }

  /** One message from the kernel: a command, as the transport parsed it. */
  receive(command) {
    if (typeof command !== "object" || command === null) return;
    if (typeof command.id !== "number" || typeof command.method !== "string") return;
    this.handle(command).then(
      (result) => this.emitRaw({ id: command.id, result: result ?? {} }),
      (e) => this.emitRaw({ id: command.id, error: { code: -32000, message: e instanceof Error ? e.message : String(e) } }),
    );
  }

  /** The kernel went away or closed the browser: every page is let go. */
  close() {
    this.closed = true;
    for (const t of [...this.targets.values()]) this.drop(t, "the browser closed", "shutdown");
  }

  async handle(c) {
    const params = c.params ?? {};
    if (c.sessionId !== undefined) {
      const s = this.sessions.get(c.sessionId);
      if (!s) throw new Error(`No session with given id ${c.sessionId}`);
      if (!pageLevel.has(c.method)) throw new Error(`${c.method} is not available in ${this.name}`);
      return this.forward(s.target, s.child, c.method, params);
    }
    if (!browserLevel.has(c.method)) throw new Error(`${c.method} is not available in ${this.name}`);
    switch (c.method) {
      case "Target.setDiscoverTargets":
        return {};
      case "Browser.setDownloadBehavior":
        this.downloadBehavior(params);
        return {};
      case "Browser.close":
        this.close();
        return {};
      case "Target.createTarget": {
        const url = typeof params.url === "string" ? params.url : "about:blank";
        const herSession = typeof params.herSession === "string" ? params.herSession : undefined;
        return { targetId: await this.createTarget(url, herSession) };
      }
      case "Target.attachToTarget": {
        const t = this.target(params.targetId);
        if (t.sessionId) throw new Error("the target is attached already");
        await this.attachPage(t);
        t.sessionId = `S${this.next++}-${t.targetId}`;
        this.sessions.set(t.sessionId, { target: t });
        this.emit("Target.attachedToTarget", { sessionId: t.sessionId, targetInfo: this.info(t), waitingForDebugger: false });
        return { sessionId: t.sessionId };
      }
      case "Target.detachFromTarget": {
        // The session that had this tab ended: the tab stays, and it may find it again.
        if (params.herRelease === true) {
          this.drop(this.target(params.targetId), "the session let it go", "release");
          return {};
        }
        const s = typeof params.sessionId === "string" ? this.sessions.get(params.sessionId) : undefined;
        // A child target (an iframe, a worker) is the page's to detach; a page stays attached
        // until it is closed.
        if (s?.child) return this.forward(s.target, undefined, "Target.detachFromTarget", { sessionId: s.child });
        return {};
      }
      case "Target.closeTarget":
        this.drop(this.target(params.targetId), "the tab was closed", "close");
        return { success: true };
      case "Target.activateTarget":
        this.activate(this.target(params.targetId));
        return {};
      case "Target.getTargets":
        return { targetInfos: this.listTargets(params) };
      case "Target.getTargetInfo":
        return { targetInfo: this.info(this.target(params.targetId)) };
    }
    throw new Error(`${c.method} is not available in ${this.name}`);
  }

  // ---- For subclasses to fill in ----

  /** Sends a command to a page's debugger; `child` for a child target. @returns {Promise<unknown>} */
  forward(_t, _child, _method, _params) {
    throw new Error("not implemented");
  }

  /** Opens a page for `Target.createTarget`; its target id. @returns {Promise<string> | string} */
  createTarget(_url, _herSession) {
    throw new Error("not implemented");
  }

  /** Before the kernel's session on a page starts: attach its debugger, where that is needed. */
  async attachPage(_t) {}

  /** Lets a page go: `close` closes it, `shutdown` is the browser ending, `release` is the
   * session that had it ending, `gone` is a page that went by itself (closed, crashed, detached). */
  release(_t, _how) {}

  activate(_t) {}

  downloadBehavior(_params) {}

  /** `Target.getTargets`: pages handed to the kernel; none by default. */
  listTargets(_params) {
    return [];
  }

  /** The page's URL and title. @returns {{url: string, title: string}} */
  describe(_t) {
    return { url: "", title: "" };
  }

  // ---- Shared ----

  target(id) {
    const t = typeof id === "string" ? this.targets.get(id) : undefined;
    if (!t) throw new Error(`No target with given id ${String(id)}`);
    return t;
  }

  /** Registers a page and tells the kernel. */
  add(t) {
    this.targets.set(t.targetId, t);
    this.emit("Target.targetCreated", { targetInfo: this.info(t) });
    return t;
  }

  info(t) {
    const { url, title } = this.describe(t);
    return {
      targetId: t.targetId,
      type: "page",
      title,
      url,
      attached: !!t.sessionId,
      canAccessOpener: false,
      ...(t.openerId ? { openerId: t.openerId } : {}),
      // So the kernel tells the model who opened it.
      ...(t.byPerson ? { herOpenedBy: "person" } : {}),
    };
  }

  /** The URL or title of a page changed. */
  changed(t) {
    if (this.targets.has(t.targetId)) this.emit("Target.targetInfoChanged", { targetInfo: this.info(t) });
  }

  /** An event of a page's debugger, passed on under the session the kernel knows; `child`
   * for a child target the page attached (an iframe). */
  pageEvent(t, method, params, child) {
    if (!t.sessionId || !this.targets.has(t.targetId)) return;
    if (method === "Target.attachedToTarget" && typeof params.sessionId === "string") {
      this.sessions.set(params.sessionId, { target: t, child: params.sessionId });
    }
    if (method === "Target.detachedFromTarget" && typeof params.sessionId === "string") {
      this.sessions.delete(params.sessionId);
    }
    // A child's session id is the page's debugger's own.
    if (child && !this.sessions.has(child)) this.sessions.set(child, { target: t, child });
    this.emit(method, params, child ?? t.sessionId);
  }

  /** Forgets a page and tells the kernel; `how` as for `release`. */
  drop(t, reason, how) {
    if (!this.targets.delete(t.targetId)) return;
    for (const [id, s] of this.sessions) if (s.target === t) this.sessions.delete(id);
    if (how === "close") this.release(t, how);
    if (t.sessionId) this.emit("Target.detachedFromTarget", { sessionId: t.sessionId, targetId: t.targetId, reason });
    this.emit("Target.targetDestroyed", { targetId: t.targetId });
    if (how !== "close") this.release(t, how);
  }

  emit(method, params, sessionId) {
    this.emitRaw(sessionId ? { method, params, sessionId } : { method, params });
  }

  emitRaw(message) {
    if (!this.closed || "id" in message) this.send(message);
  }
}
