const plural = /^(.*)_(zero|one|two|few|many|other)$/;

/** A bundle of text with each key's Windows variant in its place, and no variants left (i18n.ts). */
export function windowsWords(o: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(o)) {
    if (/_win(_(zero|one|two|few|many|other))?$/.test(k)) continue;
    const m = plural.exec(k);
    const variant = o[m ? `${m[1]}_win_${m[2]}` : `${k}_win`];
    out[k] = v && typeof v === "object" ? windowsWords(v as Record<string, unknown>) : (variant ?? v);
  }
  return out;
}
