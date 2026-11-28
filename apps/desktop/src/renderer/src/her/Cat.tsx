import { useEffect, useId, useRef, useState } from "react";
import "./cat.css";

// Her's cat (the avatar her calls "mark"), on a 120 grid of which the box 8 16 104 104 shows: a round body with
// two ears behind it, a lighter face, two eyes, a cat's mouth and a blush. No outline: soft shading only. Small,
// her body, ears and larger eyes alone. She breathes and blinks; entering her conversation, she waves; busy, she looks around and bobs (cat.css).
const catBody = "M60 30C84 30 100 48 103 72C106 96 92 110 60 110C28 110 14 96 17 72C20 48 36 30 60 30Z";
const catFace = "M46 47H74C86 47 92 55 92 65.5C92 80 82 88 60 88C38 88 28 80 28 65.5C28 55 34 47 46 47Z";
const catWavePaw = "M88.5 77.5C90 69 94.5 59 97.4 54.4C100.4 49.4 108 48.8 111.8 53.6C115.4 58.2 113.3 65.6 108.6 72.6C105.2 77.6 101.4 82.5 96.4 83.5C91.4 84.5 87.5 82.4 88.5 77.5Z";
const catInk = "#3c2f3a";
const catBlush = "#ffa3be";

const canGreet = (small: boolean, mood?: "asleep" | "wake") =>
  !small && !mood && (typeof matchMedia !== "function" || !matchMedia("(prefers-reduced-motion: reduce)").matches);

/**
 * mood: "asleep", eyes shut and breathing slowly, until she has a model; "wake", opening her eyes and hopping, once
 * (the first run, docs/cold-start-prd.md §5.5.3). `greet`: waving hello once when entering a conversation with Her.
 */
export function Cat({
  small,
  busy = false,
  mood,
  greet = false,
  onGreetEnd,
}: {
  small: boolean;
  busy?: boolean;
  mood?: "asleep" | "wake";
  greet?: boolean;
  onGreetEnd?: () => void;
}) {
  const id = useId().replace(/:/g, "");
  const [waving, setWaving] = useState(() => greet && canGreet(small, mood));
  const endRef = useRef(onGreetEnd);
  endRef.current = onGreetEnd;

  useEffect(() => {
    if (greet) {
      if (canGreet(small, mood)) setWaving(true);
      else endRef.current?.();
    } else {
      setWaving(false);
    }
  }, [greet, small, mood]);

  useEffect(() => {
    if (!waving) return;
    const t = setTimeout(() => {
      setWaving(false);
      endRef.current?.();
    }, 1300);
    return () => clearTimeout(t);
  }, [waving]);

  const fill = `url(#${id}g)`;
  const soft = (n: 1 | 2 | 3) => `url(#${id}b${n})`;
  const ear = (
    <>
      <path d="M30 53L26 25L50 37Z" fill="#cbd3fb" stroke="#cbd3fb" strokeWidth={8} strokeLinejoin="round" />
      {!small && <path d="M33 45L31.5 33L42.5 38.5Z" fill={catBlush} stroke={catBlush} strokeWidth={3} strokeLinejoin="round" opacity={0.55} filter={soft(1)} />}
    </>
  );
  const eyeShape = (x: number) => (
    <>
      <ellipse cx={x} cy={64} rx={small ? 5.2 : 4.1} ry={small ? 5.9 : 4.7} fill={catInk} />
      {!small && <circle cx={x + 1.3} cy={62.4} r={1.25} fill="#fff" />}
    </>
  );
  const apart = small ? 12.5 : 11.5;
  return (
    <span className={["cat", busy && "busy", waving && "greet", mood].filter(Boolean).join(" ")} aria-hidden="true">
      {!small && (
        <svg key="shadow" viewBox="8 16 104 104" aria-hidden="true">
          <defs>
            <filter id={`${id}s`} x="-50%" y="-50%" width="200%" height="200%">
              <feGaussianBlur stdDeviation={1.1} />
            </filter>
          </defs>
          <ellipse cx={60} cy={111} rx={34} ry={3.6} fill="#4b4660" opacity={0.12} filter={`url(#${id}s)`} />
        </svg>
      )}
      <span
        className="cat-body"
        onAnimationEnd={(e) => {
          if (e.animationName === "cat-wave-body") {
            setWaving(false);
            endRef.current?.();
          }
        }}
      >
        {waving ? (
          <>
            <span className="cat-ear-l">
              <svg viewBox="8 16 104 104" aria-hidden="true">
                <defs>
                  <filter id={`${id}el`} x="-50%" y="-50%" width="200%" height="200%">
                    <feGaussianBlur stdDeviation={0.8} />
                  </filter>
                </defs>
                <path d="M30 53L26 25L50 37Z" fill="#cbd3fb" stroke="#cbd3fb" strokeWidth={8} strokeLinejoin="round" />
                <path d="M33 45L31.5 33L42.5 38.5Z" fill={catBlush} stroke={catBlush} strokeWidth={3} strokeLinejoin="round" opacity={0.55} filter={`url(#${id}el)`} />
              </svg>
            </span>
            <span className="cat-ear-r">
              <svg viewBox="8 16 104 104" aria-hidden="true">
                <defs>
                  <filter id={`${id}er`} x="-50%" y="-50%" width="200%" height="200%">
                    <feGaussianBlur stdDeviation={0.8} />
                  </filter>
                </defs>
                <g transform="translate(120 0) scale(-1 1)">
                  <path d="M30 53L26 25L50 37Z" fill="#cbd3fb" stroke="#cbd3fb" strokeWidth={8} strokeLinejoin="round" />
                  <path d="M33 45L31.5 33L42.5 38.5Z" fill={catBlush} stroke={catBlush} strokeWidth={3} strokeLinejoin="round" opacity={0.55} filter={`url(#${id}er)`} />
                </g>
              </svg>
            </span>
            <span className="cat-paw-r">
              <svg viewBox="8 16 104 104" aria-hidden="true">
                <defs>
                  <radialGradient id={`${id}pr`} gradientUnits="userSpaceOnUse" cx={44} cy={50} r={72}>
                    <stop offset={0} stopColor="#f1f3ff" />
                    <stop offset={0.55} stopColor="#d7ddff" />
                    <stop offset={1} stopColor="#b3bcf2" />
                  </radialGradient>
                </defs>
                <circle cx={104} cy={86} r={7.5} fill={`url(#${id}pr)`} />
              </svg>
            </span>
            <svg viewBox="8 16 104 104" aria-hidden="true">
              <defs>
                <radialGradient id={`${id}g`} gradientUnits="userSpaceOnUse" cx={44} cy={50} r={72}>
                  <stop offset={0} stopColor="#f1f3ff" />
                  <stop offset={0.55} stopColor="#d7ddff" />
                  <stop offset={1} stopColor="#b3bcf2" />
                </radialGradient>
                {([0.8, 1.1] as const).map((blur, i) => (
                  <filter key={blur} id={`${id}b${i + 1}`} x="-50%" y="-50%" width="200%" height="200%">
                    <feGaussianBlur stdDeviation={blur} />
                  </filter>
                ))}
              </defs>
              <circle cx={16} cy={86} r={7.5} fill={fill} />
              <path d={catBody} fill={fill} />
              <ellipse cx={37} cy={47} rx={7} ry={3.6} fill="#fff" opacity={0.5} filter={soft(2)} transform="rotate(-30 37 47)" />
              <path d={catFace} fill="#fafbff" filter={soft(1)} />
              <path d="M55.2 71.2q2.4 3.3 4.8 0q2.4 3.3 4.8 0" fill="none" stroke={catInk} strokeWidth={2.1} strokeLinecap="round" strokeLinejoin="round" />
            </svg>
            <span className="cat-blush">
              <svg viewBox="8 16 104 104" aria-hidden="true">
                <defs>
                  <filter id={`${id}b3`} x="-50%" y="-50%" width="200%" height="200%">
                    <feGaussianBlur stdDeviation={2.4} />
                  </filter>
                </defs>
                <ellipse cx={37} cy={73} rx={6.5} ry={4.4} fill={catBlush} filter={soft(3)} />
                <ellipse cx={83} cy={73} rx={6.5} ry={4.4} fill={catBlush} filter={soft(3)} />
              </svg>
            </span>
          </>
        ) : (
          <svg viewBox="8 16 104 104" aria-hidden="true">
            <defs>
              <radialGradient id={`${id}g`} gradientUnits="userSpaceOnUse" cx={44} cy={50} r={72}>
                <stop offset={0} stopColor="#f1f3ff" />
                <stop offset={0.55} stopColor="#d7ddff" />
                <stop offset={1} stopColor="#b3bcf2" />
              </radialGradient>
              {!small &&
                ([0.8, 1.1, 2.4] as const).map((blur, i) => (
                  <filter key={blur} id={`${id}b${i + 1}`} x="-50%" y="-50%" width="200%" height="200%">
                    <feGaussianBlur stdDeviation={blur} />
                  </filter>
                ))}
            </defs>
            {ear}
            <g transform="translate(120 0) scale(-1 1)">{ear}</g>
            {!small && (
              <>
                <circle cx={16} cy={86} r={7.5} fill={fill} />
                <circle cx={104} cy={86} r={7.5} fill={fill} />
              </>
            )}
            <path d={catBody} fill={fill} />
            {!small && <ellipse cx={37} cy={47} rx={7} ry={3.6} fill="#fff" opacity={0.5} filter={soft(2)} transform="rotate(-30 37 47)" />}
            <path d={catFace} fill="#fafbff" filter={small ? undefined : soft(1)} />
            {!small && (
              <>
                <ellipse cx={37} cy={73} rx={6.5} ry={4.4} fill={catBlush} opacity={0.6} filter={soft(3)} />
                <ellipse cx={83} cy={73} rx={6.5} ry={4.4} fill={catBlush} opacity={0.6} filter={soft(3)} />
              </>
            )}
            {!small && <path d="M55.2 71.2q2.4 3.3 4.8 0q2.4 3.3 4.8 0" fill="none" stroke={catInk} strokeWidth={2.1} strokeLinecap="round" strokeLinejoin="round" />}
          </svg>
        )}
        <span key="eyes" className="cat-eyes">
          <span className="eye">
            <svg viewBox="8 16 104 104" aria-hidden="true">
              {eyeShape(60 - apart)}
              {eyeShape(60 + apart)}
            </svg>
          </span>
          {waving && (
            <span className="eye-happy">
              <svg viewBox="8 16 104 104" aria-hidden="true">
                <path d="M44.1 65.2Q48.5 59.4 52.9 65.2" fill="none" stroke={catInk} strokeWidth={2.35} strokeLinecap="round" />
                <path d="M67.1 65.2Q71.5 59.4 75.9 65.2" fill="none" stroke={catInk} strokeWidth={2.35} strokeLinecap="round" />
              </svg>
            </span>
          )}
        </span>
        {waving && (
          <span className="cat-wave-paw">
            <svg viewBox="8 16 104 104" aria-hidden="true">
              <defs>
                <linearGradient id={`${id}wp`} gradientUnits="userSpaceOnUse" x1={106} y1={50} x2={91} y2={83}>
                  <stop offset={0} stopColor="#ecefff" />
                  <stop offset={0.52} stopColor="#d4dcff" />
                  <stop offset={1} stopColor="#c3cbf6" />
                </linearGradient>
                <linearGradient id={`${id}wmf`} gradientUnits="userSpaceOnUse" x1={104} y1={68} x2={93} y2={83.5}>
                  <stop offset={0} stopColor="#fff" />
                  <stop offset={0.72} stopColor="#fff" />
                  <stop offset={1} stopColor="#000" />
                </linearGradient>
                <mask id={`${id}wm`}>
                  <rect x={70} y={36} width={54} height={56} fill={`url(#${id}wmf)`} />
                </mask>
                <filter id={`${id}ws`} x="-50%" y="-50%" width="200%" height="200%">
                  <feGaussianBlur stdDeviation={1.3} />
                </filter>
                <filter id={`${id}wpad`} x="-50%" y="-50%" width="200%" height="200%">
                  <feGaussianBlur stdDeviation={0.3} />
                </filter>
              </defs>
              <ellipse cx={97.5} cy={59.5} rx={4.2} ry={6.8} transform="rotate(24 97.5 59.5)" fill="#4b4660" opacity={0.09} filter={`url(#${id}ws)`} />
              <g mask={`url(#${id}wm)`}>
                <path d={catWavePaw} fill={`url(#${id}wp)`} />
                <g transform="translate(104.8 59.4) rotate(22)" filter={`url(#${id}wpad)`}>
                  <ellipse cx={0} cy={2.3} rx={3.4} ry={2.5} fill={catBlush} opacity={0.82} />
                  <ellipse cx={-3.3} cy={-2} rx={1.3} ry={1.6} transform="rotate(-18 -3.3 -2)" fill={catBlush} opacity={0.82} />
                  <ellipse cx={0} cy={-3.2} rx={1.35} ry={1.7} fill={catBlush} opacity={0.82} />
                  <ellipse cx={3.3} cy={-2} rx={1.3} ry={1.6} transform="rotate(18 3.3 -2)" fill={catBlush} opacity={0.82} />
                </g>
              </g>
            </svg>
          </span>
        )}
      </span>
    </span>
  );
}
