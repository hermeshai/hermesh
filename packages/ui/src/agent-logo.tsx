import { agentLogos } from "./agent-logo-data";
import { agentLogoImages } from "./agent-logo-images";

/** A coding agent's logo in a size×size box; a monogram tile for agents without one. */
export function AgentLogo({ agent, size = 16 }: { agent: string; size?: number }) {
  const picture = agentLogoImages[agent];
  if (picture) return <img src={picture} width={size} height={size} alt="" aria-hidden="true" style={{ flex: "none", display: "block" }} />;
  const logo = agentLogos[agent];
  if (!logo) {
    return (
      <span
        aria-hidden="true"
        style={{
          width: size, height: size, borderRadius: size * 0.25, background: "#8E8E93", color: "#fff",
          display: "inline-flex", alignItems: "center", justifyContent: "center", fontSize: size * 0.6, fontWeight: 600, flex: "none",
        }}
      >
        {agent.slice(0, 1).toUpperCase()}
      </span>
    );
  }
  return (
    <svg width={size} height={size} viewBox={logo.viewBox} aria-hidden="true" style={{ flex: "none", display: "block" }}>
      {logo.layers.map((l, i) => (
        <path
          key={i}
          d={l.d}
          fill={l.fill}
          fillOpacity={l.opacity}
          fillRule={logo.evenOdd ? "evenodd" : undefined}
          stroke={logo.stroke ? l.fill : undefined}
          strokeWidth={logo.stroke || undefined}
          strokeLinejoin={logo.stroke ? "round" : undefined}
        />
      ))}
    </svg>
  );
}

/**
 * A coding or personal agent's logo on a tile, the one way it is shown as an
 * icon: a white rounded tile with the logo inside, or a logo that is already
 * an app icon at the tile's size.
 */
export function AgentTile({ agent, size = 32 }: { agent: string; size?: number }) {
  if (agentLogoImages[agent]) return <AgentLogo agent={agent} size={size} />;
  return (
    <span
      aria-hidden="true"
      style={{
        width: size, height: size, borderRadius: size * 0.28, background: "#fff", color: "#000", flex: "none",
        boxShadow: "0 0 0 .5px rgba(0,0,0,.1), 0 1px 2px rgba(0,0,0,.08)", display: "inline-flex", alignItems: "center", justifyContent: "center",
      }}
    >
      <AgentLogo agent={agent} size={Math.round(size * 0.58)} />
    </span>
  );
}
