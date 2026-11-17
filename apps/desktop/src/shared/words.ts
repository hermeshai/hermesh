const plural = /^(.*)_(zero|one|two|few|many|other)$/;
const variants = /_(win|linux)(_(zero|one|two|few|many|other))?$/;

/** A bundle of text with each key's variant for `system` in its place, and no variants left (i18n.ts). */
export function systemWords(o: Record<string, unknown>, system: "win" | "linux"): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(o)) {
    if (variants.test(k)) continue;
    const m = plural.exec(k);
    const variant = o[m ? `${m[1]}_${system}_${m[2]}` : `${k}_${system}`];
    out[k] = v && typeof v === "object" ? systemWords(v as Record<string, unknown>, system) : (variant ?? v);
  }
  return out;
}
