/**
 * Draws the sidebar's icons.
 *
 * ZenithDocs shows an icon beside a page only when the page names one from its
 * own set, and that set is smaller than this documentation has pages. Sections
 * name theirs in meta.json, which 0.2.1 draws beside an always-open group; the
 * pages are matched here on the URL they link to and given a mask, so the
 * drawing takes the text colour and the hover and current-page states keep
 * working untouched.
 *
 * The drawings are Quasar's own: 24×24, 2px round strokes, no fill — the same
 * geometry as web/static/templates/partials/icons.html, so the documentation
 * and the dashboard look like one product. Every icon is used once: two pages
 * wearing the same mark teaches the reader that the mark means nothing.
 *
 * Run `npm run gen:icons` after adding a page.
 */
import { writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

/** name -> the inside of a 24×24 <svg>, stroked. */
const ICONS = {
  // --- root pages ---
  home: '<path d="m3 11 9-8 9 8"/><path d="M5 9.5V20a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V9.5"/><path d="M10 21v-6h4v6"/>',
  compass: '<circle cx="12" cy="12" r="9"/><polygon points="16 8 14 14 8 16 10 10"/>',
  lifebuoy: '<circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="4"/><path d="m5.6 5.6 3.6 3.6M14.8 14.8l3.6 3.6M18.4 5.6l-3.6 3.6M9.2 14.8l-3.6 3.6"/>',

  // --- getting started ---
  install: '<path d="M12 3v10"/><path d="m8 11 4 4 4-4"/><path d="M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2"/>',
  checklist: '<rect x="5" y="4" width="14" height="18" rx="2"/><path d="M9 4V3h6v1"/><polyline points="9 12 11 14 15 10"/><path d="M9 18h6"/>',
  refresh: '<path d="M3 12a9 9 0 0 1 15.5-6.2L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-15.5 6.2L3 16"/><path d="M3 21v-5h5"/>',

  // --- applications ---
  deploy: '<path d="M12 21V8"/><path d="m8 12 4-4 4 4"/><path d="M5 5h14"/>',
  sitemap: '<rect x="9" y="2" width="6" height="5" rx="1"/><rect x="2" y="17" width="6" height="5" rx="1"/><rect x="16" y="17" width="6" height="5" rx="1"/><path d="M12 7v4"/><path d="M5 17v-2h14v2"/>',
  globe: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18"/><path d="M12 3a14 14 0 0 1 0 18a14 14 0 0 1 0-18z"/>',
  sliders: '<path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3"/><path d="M1 14h6M9 8h6M17 16h6"/>',
  database: '<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v14a9 3 0 0 0 18 0V5"/><path d="M3 12a9 3 0 0 0 18 0"/>',
  pulse: '<path d="M3 12h4l3-8 4 16 3-8h4"/>',
  lock: '<rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>',
  history: '<path d="M4 6h9M4 12h6M4 18h6"/><circle cx="17" cy="15" r="5"/><path d="M17 13v2l1.5 1.5"/>',
  bolt: '<polygon points="13 2 3 14 12 14 11 22 21 10 12 10"/>',

  // --- catalogue ---
  pick: '<path d="M12 3v3M5.6 5.6l2.1 2.1M3 12h3M5.6 18.4l2.1-2.1"/><polygon points="12 11 21 15 16.5 16.5 15 21"/>',
  plusSquare: '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M12 8v8M8 12h8"/>',
  fileCode: '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/><path d="m10 13-2 2 2 2M14 13l2 2-2 2"/>',

  // --- stations ---
  station: '<rect x="1.5" y="5" width="6" height="14" rx="1.5"/><rect x="16.5" y="5" width="6" height="14" rx="1.5"/><rect x="9.5" y="8.5" width="5" height="7" rx="2"/><path d="M7.5 12h2M14.5 12h2"/>',
  packageCheck: '<path d="M21 10V7a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 7v9a2 2 0 0 0 1 1.73l4 2.3"/><path d="m3.3 6 8.7 5 8.7-5"/><polyline points="13 18 15.5 20.5 21 15"/>',
  pencil: '<path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z"/>',
  key: '<circle cx="7.5" cy="15.5" r="4.5"/><path d="m10.5 12.5 9-9"/><path d="m16 7 3 3"/><path d="m13.5 9.5 3 3"/>',
  layout: '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M3 9h18"/><path d="M9 21V9"/>',
  code: '<polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/>',
  timer: '<path d="M9 2h6"/><circle cx="12" cy="14" r="8"/><path d="M12 10v4l2.5 2"/>',
  palette: '<path d="M12 3a9 9 0 0 0 0 18h1.5a2 2 0 0 0 1.4-3.4 2 2 0 0 1 1.4-3.4H19a3 3 0 0 0 3-3A9 9 0 0 0 12 3Z"/><circle cx="7.5" cy="11.5" r="1"/><circle cx="12" cy="7.5" r="1"/><circle cx="16.5" cy="11" r="1"/>',
  shieldCheck: '<path d="M12 2 4 6v6c0 5 3.5 8.5 8 10 4.5-1.5 8-5 8-10V6Z"/><polyline points="9 12 11 14 15 10"/>',

  // --- server ---
  gauge: '<path d="M3.5 18a9 9 0 1 1 17 0"/><path d="m12 14 4.5-4.5"/><circle cx="12" cy="14" r="1.5"/>',
  bell: '<path d="M18 8a6 6 0 0 0-12 0c0 7-3 9-3 9h18s-3-2-3-9"/><path d="M13.7 21a2 2 0 0 1-3.4 0"/>',
  archive: '<rect x="2" y="3" width="20" height="5" rx="1"/><path d="M4 8v11a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8"/><path d="M10 12h4"/>',
  disk: '<path d="M22 12H2"/><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/><path d="M6 16h.01M10 16h.01"/>',
  users: '<circle cx="9" cy="8" r="4"/><path d="M2 21a7 7 0 0 1 14 0"/><path d="M16 3.5a4 4 0 0 1 0 9"/><path d="M22 21a7 7 0 0 0-4.5-6.5"/>',
  card: '<rect x="2" y="5" width="20" height="14" rx="2"/><path d="M2 10h20"/><path d="M6 15h4"/>',
  eventLog: '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="m7 8 1.5 1.5L11 7"/><path d="M13 8h4M7 13h10M7 17h7"/>',

  // --- reference ---
  plug: '<path d="M9 2v6M15 2v6"/><path d="M6 8h12v3a6 6 0 0 1-12 0z"/><path d="M12 17v5"/>',
  list: '<path d="M8 6h13M8 12h13M8 18h13"/><path d="M3 6h.01M3 12h.01M3 18h.01"/>',
  inspect: '<rect x="3" y="3" width="12" height="12" rx="2"/><circle cx="16" cy="16" r="4"/><path d="m19 19 2 2"/>',
  send: '<path d="M22 2 11 13"/><path d="m22 2-7 20-4-9-9-4Z"/>',
  power: '<path d="M12 3v9"/><path d="M6.5 6.5a9 9 0 1 0 11 0"/>',
  cpu: '<rect x="4" y="4" width="16" height="16" rx="2"/><rect x="9" y="9" width="6" height="6"/><path d="M9 1v3M15 1v3M9 20v3M15 20v3M1 9h3M1 15h3M20 9h3M20 15h3"/>',
  link: '<path d="M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/>',
  cog: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.6 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.6a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/>',
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  keyhole: '<circle cx="12" cy="12" r="9"/><path d="M12 7.5v3.2"/><circle cx="12" cy="14" r="2"/>',
  terminal: '<rect x="2" y="4" width="20" height="16" rx="2"/><polyline points="6 9 9 12 6 15"/><path d="M12 15h6"/>',

  // --- tools ---
  wand: '<path d="m3 21 9-9"/><path d="M14 3v4M20 9v4M12 7h4M18 13h4"/><path d="m14.5 9.5 5-5"/>',
  toolbox: '<rect x="2" y="8" width="20" height="12" rx="2"/><path d="M8 8V6a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/><path d="M2 13h20"/><path d="M10 13v2M14 13v2"/>',
  shelfPlus: '<rect x="3" y="4" width="18" height="7" rx="1.5"/><path d="M3 15h9"/><path d="M3 19h9"/><path d="M17 14v7M13.5 17.5h7"/>',
  badgeCheck: '<path d="m12 2 2.4 2.1 3.2-.4.5 3.2L21 8.6 19.4 11.5 21 14.4l-2.9 1.7-.5 3.2-3.2-.4L12 21l-2.4-2.1-3.2.4-.5-3.2L3 14.4 4.6 11.5 3 8.6l2.9-1.7.5-3.2 3.2.4z"/><path d="m9 11.5 2 2 4-4"/>',
  // One source, three ways out. Filled rather than stroked at both ends: at
  // 16 pixels a 1.3px chevron turns to grey mush, while a solid head keeps its
  // shape — which is the whole of what this drawing has to say.
  // One source, three ways out. Three solid heads hanging off a bar, with no
  // stems between them: at 16 pixels a stem is a smudge and three thin chevrons
  // read as one grey block, while a filled triangle keeps its shape all the way
  // down. Fanned out by angle instead, the three heads collide in the middle.
  fanOut:
    '<rect x="2.5" y="8.5" width="7" height="7" rx="1.5" stroke-width="1.6"/>' +
    [5.5, 12, 18.5]
      .map(
        (y) =>
          `<path d="M10 ${y}h4" stroke-width="1.6"/><polygon points="13 ${y - 2.2} 18 ${y} 13 ${y + 2.2}" fill="#000" stroke="none"/>`,
      )
      .join(''),
  signpost: '<path d="M12 2v3M12 13v9"/><path d="M5 5h11l3 4-3 4H5z"/>',
  // Quasar's own hammer: this one builds a document rather than tightening
  // something, and a wrench read as "settings" to everybody who saw it.
  hammer: '<path d="m15 12-8.5 8.5a2.12 2.12 0 1 1-3-3L12 9"/><path d="M17.64 15 22 10.64"/><path d="m20.91 11.7-1.25-1.25c-.6-.6-.93-1.4-.93-2.25v-.86L16.01 4.6a5.56 5.56 0 0 0-3.94-1.64H9l.92.82A6.18 6.18 0 0 1 12 8.4v1.56l2 2h.47c.85 0 1.65.34 2.25.93l1.25 1.25"/>',
};

/**
 * What wears which drawing: a URL suffix, matched against the sidebar links, so
 * the base path the site is served from never appears here.
 */
const PAGES = {
  '/docs/': 'home',
  '/concepts/': 'compass',
  '/troubleshooting/': 'lifebuoy',

  '/getting-started/installation/': 'install',
  '/getting-started/first-steps/': 'checklist',
  '/getting-started/updating/': 'refresh',

  '/applications/deploying/': 'deploy',
  '/applications/compose/': 'sitemap',
  '/applications/domains-tls/': 'globe',
  '/applications/environment/': 'sliders',
  '/applications/storage/': 'database',
  '/applications/resources-health/': 'pulse',
  '/applications/protection/': 'lock',
  '/applications/logs-history/': 'history',
  '/applications/automation/': 'bolt',

  '/catalogue/using/': 'pick',
  '/catalogue/custom/': 'plusSquare',
  '/catalogue/format/': 'fileCode',

  '/stations/overview/': 'station',
  '/stations/installing/': 'packageCheck',
  '/stations/writing/': 'pencil',
  '/stations/permissions/': 'key',
  '/stations/interface/': 'layout',
  '/stations/script/': 'code',
  '/stations/hooks-series/': 'timer',
  '/stations/theme/': 'palette',
  '/stations/security/': 'shieldCheck',

  '/server/monitoring/': 'gauge',
  '/server/dashboard-log/': 'eventLog',
  '/server/notifications/': 'bell',
  '/server/backups/': 'archive',
  '/server/disk/': 'disk',
  '/server/users/': 'users',
  '/server/credentials/': 'card',

  '/reference/api/overview/': 'plug',
  '/reference/api/list-apps/': 'list',
  '/reference/api/get-app/': 'inspect',
  '/reference/api/deploy-app/': 'send',
  '/reference/api/restart-app/': 'power',
  '/reference/api/get-system/': 'cpu',
  '/reference/api/deploy-webhook/': 'link',
  '/reference/configuration/': 'cog',
  '/reference/server-layout/': 'folder',
  '/reference/security/': 'keyhole',
  '/reference/development/': 'terminal',

  '/tools/overview/': 'toolbox',
  '/tools/setup-assistant/': 'signpost',
  '/tools/compose-routing/': 'fanOut',
  '/tools/catalogue-builder/': 'shelfPlus',
  '/tools/station-checker/': 'badgeCheck',
  '/tools/station-builder/': 'hammer',
};

const mask = (body) => {
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="#000" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${body}</svg>`;
  return `url("data:image/svg+xml,${encodeURIComponent(svg)}")`;
};

const missing = Object.values(PAGES).filter((n) => !ICONS[n]);
if (missing.length) throw new Error(`No drawing for: ${missing.join(', ')}`);

const used = Object.values(PAGES);
const twice = used.filter((n, i) => used.indexOf(n) !== i);
if (twice.length) throw new Error(`Used by two pages: ${twice.join(', ')}`);

const lines = [
  '/* Generated by scripts/gen-icons.mjs — edit that file, then `npm run gen:icons`. */',
  '',
  '.zd-sidebar-link[href]::before {',
  '  content: "";',
  '  flex: none;',
  '  width: 1rem;',
  '  height: 1rem;',
  '  background: currentColor;',
  '  mask: var(--sb-icon) center / contain no-repeat;',
  '  -webkit-mask: var(--sb-icon) center / contain no-repeat;',
  '  opacity: 0.85;',
  '}',
  '',
];

for (const [url, icon] of Object.entries(PAGES)) {
  lines.push(`.zd-sidebar-link[href$="${url}"], a.zd-card[href$="${url}"] { --sb-icon: ${mask(ICONS[icon])}; }`);
}
/* A card linking to a page wears that page's drawing. ZenithDocs draws the
   tile, the size and the accent itself — only what is printed on it changes,
   so a card keeps looking like every other card on the site. */
const cards = Object.keys(PAGES).map((url) => `a.zd-card[href$="${url}"]`);
const sel = (suffix) => cards.map((c) => `${c} ${suffix}`).join(',\n');

lines.push(
  '',
  `${sel('.zd-card-icon::before')} {`,
  '  content: "";',
  '  width: 1rem;',
  '  height: 1rem;',
  '  background: currentColor;',
  '  mask: var(--sb-icon) center / contain no-repeat;',
  '  -webkit-mask: var(--sb-icon) center / contain no-repeat;',
  '}',
  '',
  '/* The built-in drawing gives way to it, rather than sitting beside it. */',
  `${sel('.zd-card-icon > svg')} {`,
  '  display: none;',
  '}',
  '',
);

const out = fileURLToPath(new URL('../theme/icons.css', import.meta.url));
writeFileSync(out, lines.join('\n'));
console.log(`gen-icons: ${Object.keys(PAGES).length} pages → theme/icons.css`);
