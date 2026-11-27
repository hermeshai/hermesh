import { useMemo, type CSSProperties } from "react";
import { pointOnRoundedSquare } from "./orbitPath";

const ORBIT_FRAMES = 60;
const ORBIT_SIZE = 20;

let lastOrbitKey = "";

function parseHexOrRgb(color: string): [number, number, number] {
  const probe = document.createElement("canvas");
  probe.width = 1;
  probe.height = 1;
  const ctx = probe.getContext("2d");
  if (!ctx) return [61, 91, 255];
  ctx.fillStyle = color;
  ctx.fillRect(0, 0, 1, 1);
  const d = ctx.getImageData(0, 0, 1, 1).data;
  return [d[0], d[1], d[2]];
}

function lerpRgb(a: [number, number, number], b: [number, number, number], t: number): [number, number, number] {
  return [
    Math.round(a[0] + (b[0] - a[0]) * t),
    Math.round(a[1] + (b[1] - a[1]) * t),
    Math.round(a[2] + (b[2] - a[2]) * t),
  ];
}

function rgbaStr(rgb: [number, number, number], alpha: number): string {
  return `rgba(${rgb[0]}, ${rgb[1]}, ${rgb[2]}, ${alpha.toFixed(4)})`;
}

/**
 * Renders the 20×20 zero-gap bezel comet ring once into a 60-frame sprite strip
 * on `--slogo-orbit-strip` whenever the theme or display scale changes, so every
 * running sidebar row animates with an axis-aligned `translate3d` (`CALayer`
 * direct composition on macOS) instead of per-frame `-webkit-mask-composite`.
 */
export function syncOrbitStrip(): void {
  if (typeof document === "undefined") return;
  const root = document.documentElement;
  const cs = getComputedStyle(root);
  const accent = cs.getPropertyValue("--accent").trim() || "#3d5bff";
  const dark = cs.getPropertyValue("color-scheme").trim() === "dark";
  const dpr = Math.min(3, Math.max(1, Math.ceil(window.devicePixelRatio || 2)));
  const key = `${accent}|${dark ? 1 : 0}|${dpr}`;
  if (key === lastOrbitKey) return;
  lastOrbitKey = key;

  const canvas = document.createElement("canvas");
  canvas.width = ORBIT_SIZE * ORBIT_FRAMES * dpr;
  canvas.height = ORBIT_SIZE * dpr;
  const ctx = canvas.getContext("2d");
  if (!ctx) return;
  ctx.scale(dpr, dpr);

  const scratch = document.createElement("canvas");
  scratch.width = ORBIT_SIZE * dpr;
  scratch.height = ORBIT_SIZE * dpr;
  const sctx = scratch.getContext("2d");
  if (!sctx) return;

  const accentRgb = parseHexOrRgb(accent);
  const crestRgb: [number, number, number] = dark
    ? lerpRgb(accentRgb, [255, 255, 255], 0.46)
    : [2, 132, 199];
  const track = dark ? "rgba(255, 255, 255, 0.065)" : "rgba(0, 0, 0, 0.05)";

  // V2 zero-gap concentric bezel around the unscaled 16×16 provider tile
  const cx = ORBIT_SIZE / 2;
  const cy = ORBIT_SIZE / 2;
  const iconSize = 16;
  const iconR = iconSize * 0.255;
  const gap = -0.08;
  const sw = 1.15;
  const centerSpan = iconSize + 2 * gap + sw;
  const rCenter = iconR + gap + sw / 2;
  const arcFrac = 0.36;
  const steps = 64;

  const colorAt = (t: number): [number, number, number] => {
    if (t < 0.68) return accentRgb;
    if (t < 0.90) return lerpRgb(accentRgb, crestRgb, (t - 0.68) / 0.22);
    return lerpRgb(crestRgb, accentRgb, ((t - 0.90) / 0.10) * 0.4);
  };

  for (let f = 0; f < ORBIT_FRAMES; f++) {
    const uHead = f / ORBIT_FRAMES;

    sctx.setTransform(1, 0, 0, 1, 0, 0);
    sctx.clearRect(0, 0, scratch.width, scratch.height);
    sctx.scale(dpr, dpr);

    sctx.beginPath();
    for (let i = 0; i <= steps; i++) {
      const p = pointOnRoundedSquare(cx, cy, centerSpan, rCenter, uHead - arcFrac + (i / steps) * arcFrac);
      if (i === 0) sctx.moveTo(p.x, p.y);
      else sctx.lineTo(p.x, p.y);
    }
    sctx.strokeStyle = "#ffffff";
    sctx.lineWidth = sw;
    sctx.lineCap = "round";
    sctx.lineJoin = "round";
    sctx.stroke();

    sctx.globalCompositeOperation = "source-in";
    const pT = pointOnRoundedSquare(cx, cy, centerSpan, rCenter, uHead - arcFrac);
    const pH = pointOnRoundedSquare(cx, cy, centerSpan, rCenter, uHead);
    const aT = Math.atan2(pT.y - cy, pT.x - cx);
    const aH = Math.atan2(pH.y - cy, pH.x - cx);
    const span = ((((aH - aT) % (Math.PI * 2)) + Math.PI * 2) % (Math.PI * 2)) / (Math.PI * 2);

    const cg = sctx.createConicGradient(aT, cx, cy);
    cg.addColorStop(0, rgbaStr(colorAt(0), 0));
    const samples = 10;
    for (let s = 1; s <= samples; s++) {
      const t = s / samples;
      const pos = span * t;
      if (pos <= 1) cg.addColorStop(pos, rgbaStr(colorAt(t), Math.pow(t, 1.55)));
    }
    const capPad = Math.min(0.032, (1 - span) * 0.45);
    if (span + capPad <= 1) cg.addColorStop(span + capPad, rgbaStr(colorAt(0.94), 1));
    if (span + capPad + 0.015 <= 1) cg.addColorStop(span + capPad + 0.015, rgbaStr(colorAt(1), 0));

    sctx.fillStyle = cg;
    sctx.fillRect(0, 0, ORBIT_SIZE, ORBIT_SIZE);
    sctx.globalCompositeOperation = "source-over";

    ctx.save();
    ctx.translate(f * ORBIT_SIZE, 0);
    ctx.beginPath();
    ctx.roundRect(cx - centerSpan / 2, cy - centerSpan / 2, centerSpan, centerSpan, rCenter);
    ctx.strokeStyle = track;
    ctx.lineWidth = sw;
    ctx.stroke();
    ctx.drawImage(scratch, 0, 0, ORBIT_SIZE, ORBIT_SIZE);
    ctx.restore();
  }

  root.style.setProperty("--slogo-orbit-strip", `url(${canvas.toDataURL()})`);
}

export function initRunningMotion(): void {
  if (typeof window === "undefined") return;
  const refresh = () => syncOrbitStrip();
  refresh();
  window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", refresh);
  window.addEventListener("resize", refresh, { passive: true });
  if (typeof MutationObserver !== "undefined") {
    new MutationObserver(refresh).observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["class", "data-dark-style", "data-light-style"],
    });
  }
}

const graphemeSegmenter =
  typeof Intl !== "undefined" && "Segmenter" in Intl ? new Intl.Segmenter(undefined, { granularity: "grapheme" }) : null;

const MAX_SHINE_CHUNKS = 6;

function splitShineChunks(raw: string): string[] {
  const text = raw.replace(/\s+/g, " ");
  if (!text) return [];
  const segs = graphemeSegmenter ? Array.from(graphemeSegmenter.segment(text), (s) => s.segment) : Array.from(text);
  const chunks: string[] = [];
  let cur = "";
  let weight = 0;
  for (let i = 0; i < segs.length; i++) {
    const g = segs[i];
    if (chunks.length === MAX_SHINE_CHUNKS - 1) {
      chunks.push(cur + segs.slice(i).join(""));
      return chunks;
    }
    cur += g;
    weight += (g.codePointAt(0) ?? 0) > 0x2e7f ? 2 : 1;
    if (weight >= 4) {
      chunks.push(cur);
      cur = "";
      weight = 0;
    }
  }
  if (cur) chunks.push(cur);
  return chunks;
}

/**
 * Sweeps a fainter opacity wave across a running title using pure `CALayer`
 * opacity on grapheme-safe pseudo-element chunks (`::before` keeps `.stitle`'s
 * `textContent` and native `text-overflow: ellipsis` intact without masks).
 */
export function TitleShine({ text }: { text: string }) {
  const chunks = useMemo(() => splitShineChunks(text), [text]);
  const span = Math.max(chunks.length, 6);
  return (
    <span className="shine" aria-hidden="true">
      {chunks.map((c, i) => (
        <i key={i} data-t={c} style={{ "--i": (i / span).toFixed(3) } as CSSProperties} />
      ))}
    </span>
  );
}
