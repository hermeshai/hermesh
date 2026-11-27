// The first run's particles (docs/cold-start-prd.md §4.4, §5): one canvas over the window, that
// takes no clicks, drawn "lighter". Speeds are per frame at 60fps and lives in frames; a particle
// fades in over its first sixth and out over its last 30%. Nothing at all under reduced motion.

import { isWindowHidden, onWindowHiddenChange } from "../visibility";

type Shape = "dot" | "bar" | "ring";

type P = {
  x: number;
  y: number;
  vx: number;
  vy: number;
  g: number;
  drag: number;
  r: number;
  vr: number;
  z: number;
  l: number;
  c: string;
  s: Shape;
  age: number;
  life: number;
  wait: number;
  // Flying to a point (the mark), instead of moving freely.
  tx?: number;
  ty?: number;
  // Running along a wire.
  path?: SVGPathElement;
  rev?: boolean;
  u?: number;
  len?: number;
  ox?: number;
  oy?: number;
};

type Ambient = { x: number; y: number; vx: number; vy: number; z: number; a: number; t: number };

/** Her's colours (her/Cat.tsx). */
export const catColors = ["#cbd3fb", "#ffa3be", "#f1f3ff", "#b3bcf2"];

const rnd = (a: number, b: number) => a + Math.random() * (b - a);
const pick = <T>(a: T[]) => a[Math.floor(Math.random() * a.length)];

let canvas: HTMLCanvasElement | null = null;
let ctx: CanvasRenderingContext2D | null = null;
let root: HTMLElement | null = null;
let rootRO: ResizeObserver | null = null;
let cachedW = 0;
let cachedH = 0;
let ambColor = "#ffffff";
let ps: P[] = [];
let amb: Ambient[] = [];
let ambient = false;
let raf = 0;
let last = 0;
let timers: ReturnType<typeof setTimeout>[] = [];

const still = () => typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
const css = (v: string) => (root ? getComputedStyle(root).getPropertyValue(v).trim() : "");

/** The brand's three colours, as the first run's palette has them. */
export const aurora = () => [css("--aurora-0"), css("--aurora-1"), css("--aurora-2")];

function size(): [number, number] {
  if (!canvas || !root || !ctx) return [0, 0];
  const w = cachedW || root.clientWidth;
  const h = cachedH || root.clientHeight;
  const d = window.devicePixelRatio || 1;
  const pw = Math.round(w * d);
  const ph = Math.round(h * d);
  if (canvas.width !== pw || canvas.height !== ph) {
    canvas.width = pw;
    canvas.height = ph;
    canvas.style.width = `${w}px`;
    canvas.style.height = `${h}px`;
    ctx.setTransform(d, 0, 0, d, 0, 0);
  }
  return [w, h];
}

function draw(p: { x: number; y: number; z: number; c: string; s: Shape; r?: number; l?: number }, a: number) {
  if (!ctx) return;
  ctx.globalAlpha = Math.max(0, a);
  ctx.fillStyle = ctx.strokeStyle = p.c;
  if (p.s === "bar") {
    ctx.save();
    ctx.translate(p.x, p.y);
    ctx.rotate(p.r ?? 0);
    ctx.lineWidth = p.z;
    ctx.lineCap = "round";
    ctx.beginPath();
    ctx.moveTo(-(p.l ?? 0) / 2, 0);
    ctx.lineTo((p.l ?? 0) / 2, 0);
    ctx.stroke();
    ctx.restore();
  } else if (p.s === "ring") {
    ctx.lineWidth = 1.2;
    ctx.beginPath();
    ctx.arc(p.x, p.y, p.z, 0, 7);
    ctx.stroke();
  } else {
    ctx.beginPath();
    ctx.arc(p.x, p.y, p.z, 0, 7);
    ctx.fill();
  }
}

function tick(t: number) {
  raf = 0;
  if (!ctx) return;
  const dt = Math.min(2.5, (t - last) / 16.7 || 1);
  last = t;
  const [w, h] = size();
  ctx.clearRect(0, 0, w, h);
  ctx.globalCompositeOperation = "lighter";
  if (ambient) {
    const c = ambColor;
    for (const p of amb) {
      p.x += p.vx * dt;
      p.y += p.vy * dt;
      p.t += 0.02 * dt;
      if (p.y < -5) {
        p.y = h + 5;
        p.x = rnd(0, w);
      }
      draw({ x: p.x, y: p.y, z: p.z, c, s: "dot" }, p.a * (0.55 + 0.45 * Math.sin(p.t)));
    }
  }
  const born: P[] = [];
  ps = ps.filter((p) => {
    if (p.wait > 0) {
      p.wait -= dt;
      return true;
    }
    p.age += dt;
    if (p.path) {
      p.u! += dt / 50;
      if (p.u! >= 1) return false;
      const q = p.path.getPointAtLength(p.len! * (p.rev ? 1 - p.u! : p.u!));
      p.x = p.ox! + q.x;
      p.y = p.oy! + q.y;
      born.push({ x: p.x, y: p.y, vx: 0, vy: 0, g: 0, drag: 1, r: 0, vr: 0, z: 1.6, l: 0, c: p.c, s: "dot", age: 0, life: 16, wait: 0 });
      draw(p, 1);
      return true;
    }
    if (p.tx !== undefined && p.ty !== undefined) {
      p.x += (p.tx - p.x) * 0.085 * dt;
      p.y += (p.ty - p.y) * 0.085 * dt;
    } else {
      const k = Math.pow(p.drag, dt);
      p.vx *= k;
      p.vy = p.vy * k + p.g * dt;
      p.x += p.vx * dt;
      p.y += p.vy * dt;
      p.r += p.vr * dt;
    }
    const k = p.age / p.life;
    if (k >= 1) return false;
    draw(p, Math.min(1, k * 6) * (k > 0.7 ? (1 - k) / 0.3 : 1));
    return true;
  });
  ps.push(...born);
  ctx.globalAlpha = 1;
  if ((ps.length || ambient) && !isWindowHidden()) raf = requestAnimationFrame(tick);
}

function run() {
  if (raf || still() || !ctx || isWindowHidden()) return;
  last = 0;
  raf = requestAnimationFrame(tick);
}

/** Where el's centre is in the window, and its width. */
function at(el: Element): [number, number, number] {
  const a = el.getBoundingClientRect();
  const b = root!.getBoundingClientRect();
  return [a.left - b.left + a.width / 2, a.top - b.top + a.height / 2, a.width];
}

const particle = (o: Partial<P> & Pick<P, "x" | "y" | "z" | "c" | "s" | "life">): P => ({ vx: 0, vy: 0, g: 0, drag: 1, r: 0, vr: 0, l: 0, age: 0, wait: 0, ...o });

if (typeof document !== "undefined") {
  onWindowHiddenChange((hidden) => {
    if (hidden) {
      if (raf) cancelAnimationFrame(raf);
      raf = 0;
    } else if (ps.length || ambient) {
      run();
    }
  });
}

export const fx = {
  /** The canvas to draw on, over root (the first run's window); null when it goes. */
  attach(c: HTMLCanvasElement | null, r: HTMLElement | null) {
    rootRO?.disconnect();
    rootRO = null;
    canvas = c;
    root = r;
    ctx = c?.getContext("2d") ?? null;
    cachedW = r?.clientWidth ?? 0;
    cachedH = r?.clientHeight ?? 0;
    if (r && typeof ResizeObserver === "function") {
      rootRO = new ResizeObserver(() => {
        cachedW = r.clientWidth;
        cachedH = r.clientHeight;
      });
      rootRO.observe(r);
    }
    if (!c) {
      this.reset();
      ambient = false;
      if (raf) cancelAnimationFrame(raf);
      raf = 0;
    } else {
      ambColor = css("--amb") || "#ffffff";
    }
  },
  /** Runs f after ms, unless the page changes first (reset). */
  later(f: () => void, ms: number) {
    timers.push(setTimeout(f, ms));
  },
  /** Drops what is in flight and what was to come: a page changed. */
  reset() {
    timers.forEach(clearTimeout);
    timers = [];
    ps = [];
  },
  /** 34 white lights that rise slowly behind the first run (§5.2). */
  ambient(on: boolean) {
    ambient = on && !still();
    if (ambient) {
      ambColor = css("--amb") || "#ffffff";
      if (!amb.length) {
        amb = Array.from({ length: 34 }, () => ({ x: rnd(0, 720), y: rnd(0, 660), vx: rnd(-0.06, 0.06), vy: rnd(-0.2, -0.05), z: rnd(0.6, 1.5), a: rnd(0.15, 0.5), t: rnd(0, 6) }));
      }
    }
    run();
  },
  /** 108 dots fly in from around the mark and settle along its three beams (§5.3). */
  assemble(mark: Element | null) {
    if (!mark || still() || !root) return;
    const host = mark.closest(".hm-mark") ?? mark;
    const svg = mark instanceof SVGSVGElement ? mark : host.querySelector("svg");
    const beams = [...host.querySelectorAll<SVGPathElement>("path.hm-beam:not(.hm-glint)")];
    const [cx, cy] = at(host);
    const box = host.getBoundingClientRect();
    const b = root.getBoundingClientRect();
    const m = svg?.getScreenCTM() ?? null;
    const col = aurora();
    beams.forEach((beam, i) => {
      const len = beam.getTotalLength();
      for (let j = 0; j < 36; j++) {
        const u = j / 35;
        const q = beam.getPointAtLength(len * u);
        const sx = m ? m.a * q.x + m.c * q.y + m.e - b.left : box.left - b.left + box.width / 2;
        const sy = m ? m.b * q.x + m.d * q.y + m.f - b.top : box.top - b.top + box.height / 2;
        const a = rnd(0, 6.28);
        const d = rnd(240, 420);
        ps.push(particle({ x: cx + Math.cos(a) * d, y: cy + Math.sin(a) * d, tx: sx + rnd(-2.5, 2.5), ty: sy + rnd(-2.5, 2.5), z: rnd(1, 2.2), c: col[u < 0.34 ? 0 : u < 0.67 ? 1 : 2], s: "dot", life: rnd(95, 125), wait: rnd(0, 22) + i * 6 }));
      }
    });
    run();
  },
  /** n particles thrown from el's centre at up to speed (§5.5.3). */
  burst(el: Element | null, colors: string[], n: number, speed = 5) {
    if (!el || still() || !root) return;
    const [x, y] = at(el);
    for (let i = 0; i < n; i++) {
      const a = rnd(0, 6.28);
      const sp = rnd(speed * 0.35, speed);
      const s = Math.random();
      ps.push(particle({ x, y, vx: Math.cos(a) * sp, vy: Math.sin(a) * sp, g: 0.03, drag: 0.95, r: a, vr: rnd(-0.1, 0.1), z: s < 0.3 ? 2 : rnd(1.2, 2.6), l: rnd(6, 11), c: pick(colors), s: s < 0.3 ? "bar" : s < 0.5 ? "ring" : "dot", life: rnd(45, 85) }));
    }
    run();
  },
  /** A light that runs along a wire, from its start or (rev) back to it, with a trail (§5.5.2). */
  travel(path: SVGPathElement | null, rev = false) {
    if (!path || still() || !root || !path.ownerSVGElement) return;
    const a = path.ownerSVGElement.getBoundingClientRect();
    const b = root.getBoundingClientRect();
    ps.push(particle({ path, rev, u: 0, len: path.getTotalLength(), ox: a.left - b.left, oy: a.top - b.top, x: 0, y: 0, z: 3, c: css("--aurora-0"), s: "dot", life: 999 }));
    run();
  },
  /** The finish: 160 beams, rings and dots thrown up from both lower corners (§5.7). */
  confetti() {
    if (still() || !root) return;
    const [w, h] = size();
    const col = [...aurora(), "#cbd3fb", "#ffa3be"];
    for (const side of [0, 1]) {
      for (let i = 0; i < 80; i++) {
        const a = rnd(0.85, 1.4);
        const sp = rnd(9, 18);
        const s = Math.random();
        ps.push(particle({ x: side ? w + 6 : -6, y: h - 30, vx: Math.cos(a) * sp * (side ? -1 : 1), vy: -Math.sin(a) * sp, g: 0.2, drag: 0.985, r: rnd(0, 6.28), vr: rnd(-0.25, 0.25), z: s < 0.55 ? 2.6 : rnd(1.5, 3), l: rnd(8, 15), c: pick(col), s: s < 0.55 ? "bar" : s < 0.75 ? "ring" : "dot", life: rnd(115, 175), wait: rnd(0, 16) }));
      }
    }
    run();
  },
  /** How many particles are in flight (tests). */
  count: () => ps.length,
};
