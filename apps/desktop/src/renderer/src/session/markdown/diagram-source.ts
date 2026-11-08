export const MAX_DIAGRAM_LENGTH = 20000;

export function validDiagram(source: string) {
  // Theme/configuration comes from the host, never from model output. Only local
  // SVG geometry is displayed, with no URL, HTML label or click callback support.
  return source.length <= MAX_DIAGRAM_LENGTH && !/%%\s*\{|^\s*---|\bclick\s|https?:|data:|javascript:|url\s*\(|\bimg\s*:|\bicon\s*:|@import|<\/?[a-z]/im.test(source);
}

