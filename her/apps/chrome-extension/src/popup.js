// The extension's page, from its toolbar icon (your-browser-design §3): one line on how things
// stand, what to do about it if anything, and the conversations with tabs here. The worker holds
// the state and says when it changes; details are in Hermesh's settings.

import { headline, isWaiting, listed, offersTab } from "./panel.js";

const msg = (name, ...args) => chrome.i18n.getMessage(name, args.map(String)) || name;
const root = document.getElementById("root");

function el(tag, props = {}, ...children) {
  const node = Object.assign(document.createElement(tag), props);
  node.append(...children);
  return node;
}

/** Lucide shapes, on its 24 grid; the stop is the composer's square. */
const ICONS = {
  chevron: '<path d="m9 18 6-6-6-6"/>',
  check: '<path d="M20 6 9 17l-5-5"/>',
  plus: '<path d="M5 12h14"/><path d="M12 5v14"/>',
  stop: '<rect width="24" height="24" rx="4"/>',
};

function icon(name, className = "") {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("aria-hidden", "true");
  if (className) svg.setAttribute("class", className);
  svg.innerHTML = ICONS[name];
  return svg;
}

const act = (action) => chrome.runtime.sendMessage(action).then(draw);
const closing = (action) => () => chrome.runtime.sendMessage(action).then(() => window.close());

function primary(label, onclick) {
  return el("div", { className: "acts" }, el("button", { className: "primary", onclick, textContent: label }));
}

const glyph = (className, ...children) => el("span", { className: `glyph ${className}` }, ...children);
const dot = (className) => el("i", { className: `dot ${className}` });

/** A spinner keeps turning across a redraw: each starts where the turn stands now. */
function spinner() {
  const node = el("i", { className: "spin" });
  node.style.animationDelay = `-${Date.now() % 900}ms`;
  return node;
}

function sessionGlyph(s) {
  if (isWaiting(s)) return glyph("", dot("warn"));
  return s.state === "running" ? glyph("", spinner()) : glyph("done", icon("check"));
}

function sessionLine(s) {
  if (s.handoff) return s.handoff;
  if (s.state === "awaiting_approval") return msg("approval");
  const tabs = s.tabs === 1 ? msg("oneTab") : msg("tabs", s.tabs);
  return `${s.state === "running" ? msg("working") : msg("finished")} · ${tabs}`;
}

function session(s) {
  const text = el("span", { className: "text" }, el("b", { textContent: s.title }), el("span", { className: s.handoff ? "line whole" : "line", textContent: sessionLine(s) }));
  const row = el("div", { className: "row" }, el("button", { className: "open", title: msg("back"), onclick: closing({ do: "back", session: s.id }) }, sessionGlyph(s), text, icon("chevron", "arrow")));
  if (s.state === "running" && !s.handoff) {
    const stop = msg("stop");
    row.append(el("button", { className: "stop", title: stop, ariaLabel: stop, onclick: () => act({ do: "stop", session: s.id }) }, icon("stop")));
  }
  return s.handoff ? [row, primary(msg("done"), () => act({ do: "done", session: s.id }))] : [row];
}

/** A newer drawing that finishes first wins. */
let drawing = 0;
/** What is on the page: drawing it again would only lose the pointer's place and restart motion. */
let drawn = null;

async function draw() {
  const turn = ++drawing;
  const state = await chrome.runtime.sendMessage({ get: "state" });
  if (turn !== drawing) return;
  const said = JSON.stringify(state);
  if (said === drawn) return;
  const first = drawn === null;
  drawn = said;
  const line = headline(state);
  const parts = [
    el(
      "div",
      { className: "head", role: "status" },
      glyph("", dot(line.dot)),
      line.count === undefined ? msg(line.title) : msg(line.title, line.count),
    ),
  ];
  if (line.hint) parts.push(el("p", { className: "hint", textContent: msg(line.hint) }));
  if (line.action) {
    const action = { do: line.action.do };
    parts.push(primary(msg(line.action.label), line.action.closes ? closing(action) : () => act(action)));
  }
  const sessions = listed(state);
  if (sessions.length > 0) parts.push(el("div", { className: "sep" }));
  for (const s of sessions) parts.push(...session(s));
  if (offersTab(state)) {
    parts.push(el("div", { className: "sep" }));
    parts.push(el("div", { className: "row" }, el("button", { className: "open", title: msg("offerNote"), onclick: closing({ do: "offer" }) }, glyph("", icon("plus")), el("span", { className: "text", textContent: msg("offer") }))));
  }
  if (first) root.classList.add("in");
  root.replaceChildren(...parts);
}

chrome.runtime.onMessage.addListener((message) => {
  if (message.changed) void draw();
});
void draw();
