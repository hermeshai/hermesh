import { useId, useLayoutEffect, useRef, useState, type ReactNode } from "react";

// Liquid glass, drawn by the page: the top bar of a conversation, which the
// conversation scrolls under. What lies under the bar is blurred and lit
// through it, and at its bottom edge bends as through the rim of a lens,
// each colour by a slightly different amount. Chromium (and so the app) takes
// an SVG filter as a backdrop-filter; the filter's displacement map is drawn
// to the bar's own size. The system's own glass (NSGlassEffectView) can sit
// only behind the whole page, and this sees only the page, not the desktop.

/** How far the bottom edge pulls what is under it upwards, in px; green and blue a step further each. */
const bend = 22;
const split = 3;
/** How tall the bending edge is. */
const rim = 14;

/** The displacement map: mid grey (no shift) everywhere but the bottom rim, where blue falls to 0 (a pull from above, inside the bar). */
function displacementMap(w: number, h: number): string {
  const from = Math.max(0, (h - rim) / h);
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}">
<defs><linearGradient id="y" x1="0" y1="0" x2="0" y2="1"><stop offset="${from}" stop-color="rgb(128,128,128)"/><stop offset="1" stop-color="rgb(128,128,0)"/></linearGradient></defs>
<rect width="${w}" height="${h}" fill="url(#y)"/>
</svg>`;
  return "data:image/svg+xml;utf8," + encodeURIComponent(svg);
}

/** One colour of the blurred backdrop, shifted by the map. */
function Channel({ scale, keep }: { scale: number; keep: "r" | "g" | "b" }) {
  // Each output channel takes only its own input channel, so the three blend back together by screen.
  const values = [
    keep === "r" ? "1 0 0 0 0" : "0 0 0 0 0",
    keep === "g" ? "0 1 0 0 0" : "0 0 0 0 0",
    keep === "b" ? "0 0 1 0 0" : "0 0 0 0 0",
    "0 0 0 1 0",
  ].join(" ");
  return (
    <>
      <feDisplacementMap in="soft" in2="map" scale={scale} xChannelSelector="R" yChannelSelector="B" result={`${keep}d`} />
      <feColorMatrix in={`${keep}d`} type="matrix" values={values} result={keep} />
    </>
  );
}

/** A bar of liquid glass: its own backdrop filter once measured (frosted until then, by its CSS). */
export function GlassBar({ children, className = "" }: { children: ReactNode; className?: string }) {
  const id = "lg" + useId().replace(/[^a-zA-Z0-9]/g, "");
  const ref = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState<{ w: number; h: number } | null>(null);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => {
      const w = Math.round(el.offsetWidth);
      const h = Math.round(el.offsetHeight);
      setSize((old) => (w <= 0 || h <= 0 ? null : old && old.w === w && old.h === h ? old : { w, h }));
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return (
    <div ref={ref} className={`glassbar ${className}`} style={size ? { backdropFilter: `url(#${id})` } : undefined}>
      {size && (
        <svg className="glass-defs" aria-hidden width="0" height="0">
          <filter id={id} x="0" y="0" width={size.w} height={size.h} filterUnits="userSpaceOnUse" colorInterpolationFilters="sRGB">
            <feImage x="0" y="0" width={size.w} height={size.h} preserveAspectRatio="none" href={displacementMap(size.w, size.h)} result="map" />
            <feGaussianBlur in="SourceGraphic" stdDeviation="9" edgeMode="duplicate" result="blurred" />
            <feColorMatrix in="blurred" type="saturate" values="1.8" result="soft" />
            <Channel scale={bend} keep="r" />
            <Channel scale={bend + split} keep="g" />
            <Channel scale={bend + 2 * split} keep="b" />
            <feBlend in="r" in2="g" mode="screen" result="rg" />
            <feBlend in="rg" in2="b" mode="screen" />
          </filter>
        </svg>
      )}
      {children}
    </div>
  );
}
