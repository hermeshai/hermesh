import type { ModelsListResult } from "@hermesh/her-protocol";
import { updatedAt, type ListSession } from "./sessions.ts";

/**
 * The model a new session asks for: the one chosen on the page while its provider is there, else
 * `null`, the default. A chosen model's provider goes when its connection is removed or
 * disconnected; asking for it then would only fail the send. Before the list comes there is
 * nothing to check against, and the choice stands.
 */
export function draftModel(models: ModelsListResult, chosen: string | null): string | null {
  if (!chosen || models.providers.length === 0) return chosen;
  const provider = chosen.split("/")[0];
  return models.providers.some((p) => p.id === provider) ? chosen : null;
}

/** A model menu's group: one provider's models, under its name. */
export type ModelGroup<M> = { provider: string; name: string; models: M[] };

/**
 * A model menu's rows, by provider in the order of `providers` (a provider not among them, after),
 * each provider's in the order given. With `query`, only the models whose name or reference holds
 * it, and every model of a provider whose name holds it, ignoring case.
 */
export function modelGroups<M extends { id: string; name: string; provider: string }>(models: M[], providers: { id: string; name: string }[], query = ""): ModelGroup<M>[] {
  const q = query.trim().toLowerCase();
  const order = new Map(providers.map((p, i) => [p.id, i]));
  const groups = new Map<string, ModelGroup<M>>();
  for (const m of models) {
    const name = providers.find((p) => p.id === m.provider)?.name ?? m.provider;
    if (q && !name.toLowerCase().includes(q) && !m.name.toLowerCase().includes(q) && !m.id.toLowerCase().includes(q)) continue;
    const g = groups.get(m.provider) ?? { provider: m.provider, name, models: [] };
    g.models.push(m);
    groups.set(m.provider, g);
  }
  const at = (id: string) => order.get(id) ?? order.size;
  return [...groups.values()].sort((a, b) => at(a.provider) - at(b.provider));
}

/** The models these sessions use, each once, the one used last first. */
export function recentModels(sessions: ListSession[], limit = 3): string[] {
  const latest = new Map<string, number>();
  for (const s of sessions) if (s.model) latest.set(s.model, Math.max(latest.get(s.model) ?? 0, updatedAt(s)));
  return [...latest].sort((a, b) => b[1] - a[1]).slice(0, limit).map(([id]) => id);
}
