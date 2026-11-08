// Line icons on a 16×16 grid: the Mac app's own (apps/desktop/src/renderer/src/icons.tsx),
// and the few the console adds, drawn the same way.

const paths = {
  grid: '<rect x="2.8" y="2.8" width="3.9" height="3.9" rx="1.1"/><rect x="9.3" y="2.8" width="3.9" height="3.9" rx="1.1"/><rect x="2.8" y="9.3" width="3.9" height="3.9" rx="1.1"/><rect x="9.3" y="9.3" width="3.9" height="3.9" rx="1.1"/>',
  search: '<circle cx="7" cy="7" r="4.4"/><path d="M10.3 10.3l3.4 3.4"/>',
  plus: '<path d="M8 3v10M3 8h10"/>',
  minus: '<path d="M3 8h10"/>',
  more: '<circle cx="3.6" cy="8" r="1.15" fill="currentColor" stroke="none"/><circle cx="8" cy="8" r="1.15" fill="currentColor" stroke="none"/><circle cx="12.4" cy="8" r="1.15" fill="currentColor" stroke="none"/>',
  x: '<path d="M4.3 4.3l7.4 7.4M11.7 4.3l-7.4 7.4"/>',
  check: '<path d="M3.4 8.4l3 3 6.2-6.8"/>',
  chevD: '<path d="M4.5 6.3L8 9.8l3.5-3.5"/>',
  chevR: '<path d="M6.3 4.5L9.8 8l-3.5 3.5"/>',
  info: '<circle cx="8" cy="8" r="6"/><path d="M8 7.3v3.9M8 5v.1"/>',
  terminal: '<rect x="1.8" y="2.8" width="12.4" height="10.4" rx="2"/><path d="M4.6 6.3l2 1.7-2 1.7M8.2 10h3.2"/>',
  copy: '<rect x="5.2" y="5.2" width="8" height="8" rx="1.6"/><path d="M10.8 5.2V3.8c0-.6-.5-1.1-1.1-1.1H3.9c-.6 0-1.1.5-1.1 1.1v5.8c0 .6.5 1.1 1.1 1.1h1.3"/>',
  ext: '<path d="M6.5 3.2H4c-.7 0-1.2.5-1.2 1.2V12c0 .7.5 1.2 1.2 1.2h7.6c.7 0 1.2-.5 1.2-1.2V9.5M9.3 2.8h3.9v3.9M13.2 2.8L7.5 8.5"/>',
  read: '<path d="M2 3.6c2-.8 4-.8 6 .6 2-1.4 4-1.4 6-.6v9c-2-.8-4-.8-6 .6-2-1.4-4-1.4-6-.6z"/><path d="M8 4.2v9"/>',
  edit: '<path d="M10.6 2.7l2.7 2.7-7.6 7.6-3.2.5.5-3.2z"/>',
  web: '<circle cx="8" cy="8" r="6"/><path d="M2 8h12M8 2c-2 2.2-2 9.8 0 12M8 2c2 2.2 2 9.8 0 12"/>',
  lock: '<rect x="3.2" y="7" width="9.6" height="6.8" rx="1.6"/><path d="M5.3 7V5.2a2.7 2.7 0 0 1 5.4 0V7"/>',
  warn: '<path d="M8 2.2l6.2 11H1.8z"/><path d="M8 6.4v3.2M8 11.4v.1"/>',
  toggles: '<rect x="1.8" y="2.8" width="12.4" height="4.4" rx="2.2"/><rect x="1.8" y="8.8" width="12.4" height="4.4" rx="2.2"/><circle cx="11.9" cy="5" r="1.2"/><circle cx="4.1" cy="11" r="1.2"/>',
  chip: '<rect x="3.8" y="3.8" width="8.4" height="8.4" rx="1.8"/><rect x="6.4" y="6.4" width="3.2" height="3.2" rx=".6"/><path d="M6.2 1.8v2M9.8 1.8v2M6.2 12.2v2M9.8 12.2v2M1.8 6.2h2M1.8 9.8h2M12.2 6.2h2M12.2 9.8h2"/>',
  // The console's own.
  plug: '<path d="M6 1.8v3M10 1.8v3"/><path d="M4.2 4.8h7.6v2.6a3.8 3.8 0 0 1-7.6 0z"/><path d="M8 11.2v3"/>',
  upload: '<path d="M8 10.2V2.6M5 5.4l3-3 3 3M3 11v2.8h10V11"/>',
  download: '<path d="M8 2.6v7.6M5 7.4l3 3 3-3M3 11v2.8h10V11"/>',
  database: '<ellipse cx="8" cy="4.2" rx="5.2" ry="2"/><path d="M2.8 4.2v7.6c0 1.1 2.3 2 5.2 2s5.2-.9 5.2-2V4.2M2.8 8c0 1.1 2.3 2 5.2 2s5.2-.9 5.2-2"/>',
  trash: '<path d="M2.8 4.3h10.4M6.3 4.3V2.9h3.4v1.4M4.1 4.3l.6 8.5c.1.7.6 1.2 1.3 1.2h4c.7 0 1.2-.5 1.3-1.2l.6-8.5"/>',
  retry: '<path d="M13.2 8a5.2 5.2 0 1 1-1.6-3.75"/><path d="M12.2 1.9v2.8H9.4"/>',
  logout: '<path d="M6.4 13.2H3.9c-.6 0-1.1-.5-1.1-1.1V3.9c0-.6.5-1.1 1.1-1.1h2.5M10.4 11l3-3-3-3M13.2 8H6.2"/>',
  eye: '<path d="M1.6 8s2.3-4.6 6.4-4.6S14.4 8 14.4 8s-2.3 4.6-6.4 4.6S1.6 8 1.6 8z"/><circle cx="8" cy="8" r="2"/>',
  code: '<path d="M5.4 4.6L2 8l3.4 3.4M10.6 4.6L14 8l-3.4 3.4M9.2 3l-2.4 10"/>',
  file: '<path d="M9.2 1.8H4.4c-.6 0-1.1.5-1.1 1.1v10.2c0 .6.5 1.1 1.1 1.1h7.2c.6 0 1.1-.5 1.1-1.1V5.3z"/><path d="M9.2 1.8v3.5h3.5"/>',
  updown: '<path d="M5.2 6L8 3.2 10.8 6M5.2 10L8 12.8 10.8 10"/>',
  unlock: '<rect x="3.2" y="7" width="9.6" height="6.8" rx="1.6"/><path d="M5.3 7V5.2a2.7 2.7 0 0 1 5.2-1"/>',
  grip: '<g fill="currentColor" stroke="none"><circle cx="6" cy="4" r=".95"/><circle cx="10" cy="4" r=".95"/><circle cx="6" cy="8" r=".95"/><circle cx="10" cy="8" r=".95"/><circle cx="6" cy="12" r=".95"/><circle cx="10" cy="12" r=".95"/></g>',
  text: '<path d="M2.8 3.6h10.4M2.8 6.5h10.4M2.8 9.4h10.4M2.8 12.3h6.4"/>',
};

export type IconName = keyof typeof paths;

export function Icon({ name, className = "" }: { name: IconName; className?: string }) {
  return <svg className={`ic ${className}`} viewBox="0 0 16 16" aria-hidden="true" dangerouslySetInnerHTML={{ __html: paths[name] }} />;
}
