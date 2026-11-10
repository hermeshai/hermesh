import { useId } from "react";
import "./cat.css";

// Her's cat (the avatar her calls "mark"), on a 120 grid of which the box 8 16 104 104 shows: a round body with
// two ears behind it, a lighter face, two eyes, a cat's mouth and a blush. No outline: soft shading only. Small,
// her body, ears and larger eyes alone. She breathes and blinks; busy, she looks around and bobs (cat.css).
const catBody = "M60 30C84 30 100 48 103 72C106 96 92 110 60 110C28 110 14 96 17 72C20 48 36 30 60 30Z";
const catFace = "M46 47H74C86 47 92 55 92 65.5C92 80 82 88 60 88C38 88 28 80 28 65.5C28 55 34 47 46 47Z";
const catInk = "#3c2f3a";
const catBlush = "#ffa3be";

export function Cat({ small, busy = false }: { small: boolean; busy?: boolean }) {
  const id = useId().replace(/:/g, "");
  const fill = `url(#${id}g)`;
  const soft = (n: 1 | 2 | 3) => `url(#${id}b${n})`;
  const ear = (
    <>
      <path d="M30 53L26 25L50 37Z" fill="#cbd3fb" stroke="#cbd3fb" strokeWidth={8} strokeLinejoin="round" />
      {!small && <path d="M33 45L31.5 33L42.5 38.5Z" fill={catBlush} stroke={catBlush} strokeWidth={3} strokeLinejoin="round" opacity={0.55} filter={soft(1)} />}
    </>
  );
  const eye = (x: number) => (
    <g className="eye">
      <ellipse cx={x} cy={64} rx={small ? 5.2 : 4.1} ry={small ? 5.9 : 4.7} fill={catInk} />
      {!small && <circle cx={x + 1.3} cy={62.4} r={1.25} fill="#fff" />}
    </g>
  );
  const apart = small ? 12.5 : 11.5;
  return (
    <svg className={`cat ${busy ? "busy" : ""}`} viewBox="8 16 104 104" aria-hidden="true">
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
      {!small && <ellipse cx={60} cy={111} rx={34} ry={3.6} fill="#4b4660" opacity={0.12} filter={soft(2)} />}
      <g className="cat-body">
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
        <g className="cat-eyes">
          {eye(60 - apart)}
          {eye(60 + apart)}
        </g>
        {!small && <path d="M55.2 71.2q2.4 3.3 4.8 0q2.4 3.3 4.8 0" fill="none" stroke={catInk} strokeWidth={2.1} strokeLinecap="round" strokeLinejoin="round" />}
      </g>
    </svg>
  );
}
