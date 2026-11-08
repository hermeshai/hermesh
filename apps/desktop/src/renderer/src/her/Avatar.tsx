import { useEffect, useState, type CSSProperties, type ReactNode } from "react";
import { initialOf } from "@hermesh/her-client";
import type { Avatar as AvatarSpec, AvatarHue, AvatarShape, BotSummary } from "@hermesh/her-protocol";
import { Mark } from "@hermesh/ui";
import { useHer } from "./context";

// An assistant's face: a circle of
// one clear colour with a faint drift in it, with a pair of eyes on it, the
// first character of its name, the Hermesh mark (Her's), or an uploaded
// picture. Its colour is its own, the same in both themes, like a photo.
// While it is busy the eyes look around and blink, a mark or letter breathes.

/** Clear but not loud, each the middle of its avatar's gradient. The eyes and letter are a deep shade of it (her.css). */
export const hues: Record<AvatarHue, string> = {
  slate: "#6ba6e2",
  sage: "#81cc91",
  clay: "#e68f61",
  ochre: "#e7c771",
  plum: "#a88fd6",
  teal: "#65c3c0",
  rose: "#e88ba1",
  stone: "#9b9fa5",
};

/** Each colour's drift, in degrees of hue: yellow and orange run the other way round, or yellow would turn green. */
const paint = (hue: AvatarHue) => ({ "--h": hues[hue], "--lean": hue === "ochre" || hue === "clay" ? -13 : 13 }) as CSSProperties;

export const shapes: AvatarShape[] = ["tall", "dot", "smile", "wink", "lid", "bar", "side", "odd"];

// The eyes on the 36 grid: where the two sit, then each kind at its x.
const L = 12.1;
const R = 23.9;
const Y = 17.4;

const tall = (x: number) => <rect x={x - 2} y={Y - 4.4} width={4} height={8.8} rx={2} />;
const dot = (x: number, r = 3) => <circle cx={x} cy={Y} r={r} />;
const arc = (x: number) => <path d={`M${x - 3.5} ${Y + 1.5}Q${x} ${Y - 3.7} ${x + 3.5} ${Y + 1.5}`} fill="none" strokeWidth={2.7} />;
const lid = (x: number) => <path d={`M${x - 3.6} ${Y - 2}h7.2a3.6 3.6 0 0 1-7.2 0z`} strokeWidth={0.8} />;
const bar = (x: number) => <rect x={x - 3.6} y={Y - 1.1} width={7.2} height={2.8} rx={1.4} />;

const faces: Record<AvatarShape, ReactNode> = {
  tall: <>{tall(L)}{tall(R)}</>,
  dot: <>{dot(L)}{dot(R)}</>,
  smile: <>{arc(L)}{arc(R)}</>,
  wink: <>{tall(L)}{arc(R)}</>,
  lid: <>{lid(L)}{lid(R)}</>,
  bar: <>{bar(L)}{bar(R)}</>,
  side: <>{tall(L + 4)}{tall(R + 4)}</>,
  odd: <>{dot(L, 3.9)}{dot(R, 2.3)}</>,
};

export type AvatarSize = 22 | 28 | 36 | 64 | 72;

/** Uploaded pictures, by environment, assistant and version: fetched once (`bots/avatar`). */
const pictures = new Map<string, Promise<string>>();

function usePicture(botId: string | undefined, rev: number | undefined): string | null {
  const her = useHer();
  const [src, setSrc] = useState<string | null>(null);
  const machine = her.machine.machineId;
  const connected = her.env.rpc !== null;
  useEffect(() => {
    if (!botId || rev === undefined) return;
    const key = `${machine}:${botId}:${rev}`;
    let got = pictures.get(key);
    if (!got && connected) {
      got = her.call("bots/avatar", { bot_id: botId }).then((r) => r.data);
      pictures.set(key, got);
      got.catch(() => pictures.delete(key));
    }
    let stop = false;
    void got?.then((data) => !stop && setSrc(data), () => {});
    return () => {
      stop = true;
    };
  }, [machine, botId, rev, connected]);
  return src;
}

/** The avatar of `spec`; `name` gives a letter avatar its character, `bot` an uploaded one its picture. `preview`: a picture not uploaded yet. */
export function Avatar({ spec, name, size, busy = false, bot, preview }: { spec: AvatarSpec; name: string; size: AvatarSize; busy?: boolean; bot?: string; preview?: string }) {
  const picture = usePicture(spec.kind === "image" && !preview ? bot : undefined, spec.kind === "image" ? spec.rev : undefined);
  const cls = `bav s${size} ${busy ? "busy" : ""}`;
  switch (spec.kind) {
    case "mark":
      return (
        <span className={`${cls} mark hm-dark`} aria-hidden="true">
          <Mark size={Math.round(size * 0.62)} />
        </span>
      );
    case "image": {
      const src = preview ?? picture;
      return <span className={`${cls} pic`} aria-hidden="true">{src && <img src={src} alt="" draggable={false} />}</span>;
    }
    case "mono":
      return (
        <span className={cls} style={paint(spec.hue)} aria-hidden="true">
          <b className="ch">{initialOf(name) || "·"}</b>
        </span>
      );
    case "char":
      return (
        <span className={cls} style={paint(spec.hue)} aria-hidden="true">
          <svg viewBox="0 0 36 36">
            <g className="eyes" fill="currentColor" stroke="currentColor" strokeWidth={0} strokeLinecap="round" strokeLinejoin="round">
              {faces[spec.shape]}
            </g>
          </svg>
        </span>
      );
  }
}

/** An assistant's own avatar, moving while it is busy. */
export function BotAvatar({ bot, size, busy }: { bot: BotSummary; size: AvatarSize; busy?: boolean }) {
  return <Avatar spec={bot.avatar} name={bot.name} size={size} busy={busy} bot={bot.id} />;
}
