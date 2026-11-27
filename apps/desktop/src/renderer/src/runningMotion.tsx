import { useMemo, type CSSProperties } from "react";

const ORBIT_FRAMES = 36;
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

/**
 * Renders the 20×20 squircle comet ring once into a 36-frame sprite strip on
 * `--slogo-orbit-strip` whenever the theme or display scale changes, so every
 * running sidebar row animates with an axis-aligned `translate3d` (`CALayer`
 * direct composition on macOS) instead of per-frame `-webkit-mask-composite`.
 */
export function syncOrbitStrip(): void {
  if (typeof document === "undefined") return;
  const root = document.documentElement;
  const cs = getComputedStyle(root);
  const accent = cs.getPropertyValue("--accent").trim() || "#3d5bff";
  const track = cs.getPropertyValue("--l4").trim() || "rgba(128,128,128,0.16)";
  const dpr = Math.min(3, Math.max(1, Math.ceil(window.devicePixelRatio || 2)));
  const key = `${accent}|${track}|${dpr}`;
  if (key === lastOrbitKey) return;
  lastOrbitKey = key;

  const canvas = document.createElement("canvas");
  canvas.width = ORBIT_SIZE * ORBIT_FRAMES * dpr;
  canvas.height = ORBIT_SIZE * dpr;
  const ctx = canvas.getContext("2d");
  if (!ctx) return;
  ctx.scale(dpr, dpr);

  const [ar, ag, ab] = parseHexOrRgb(accent);
  const crest = `rgb(${Math.round(ar * 0.62 + 255 * 0.38)}, ${Math.round(ag * 0.62 + 255 * 0.38)}, ${Math.round(ab * 0.62 + 255 * 0.38)})`;

  for (let f = 0; f < ORBIT_FRAMES; f++) {
    ctx.save();
    ctx.translate(f * ORBIT_SIZE, 0);
    ctx.beginPath();
    ctx.roundRect(0, 0, ORBIT_SIZE, ORBIT_SIZE, 6);
    ctx.roundRect(1.5, 1.5, ORBIT_SIZE - 3, ORBIT_SIZE - 3, 4.5);
    ctx.clip("evenodd");

    ctx.fillStyle = track;
    ctx.fillRect(0, 0, ORBIT_SIZE, ORBIT_SIZE);

    const angle = (f / ORBIT_FRAMES) * Math.PI * 2 - Math.PI / 2;
    const g = ctx.createConicGradient(angle, ORBIT_SIZE / 2, ORBIT_SIZE / 2);
    g.addColorStop(0, "transparent");
    g.addColorStop(205 / 360, "transparent");
    g.addColorStop(275 / 360, accent);
    g.addColorStop(325 / 360, crest);
    g.addColorStop(355 / 360, accent);
    g.addColorStop(1, "transparent");
    ctx.fillStyle = g;
    ctx.fillRect(0, 0, ORBIT_SIZE, ORBIT_SIZE);
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
