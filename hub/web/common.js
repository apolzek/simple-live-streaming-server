// Utilidades compartilhadas por todas as páginas.
export const $ = (sel, el = document) => el.querySelector(sel);
export const $$ = (sel, el = document) => [...el.querySelectorAll(sel)];

let cfgPromise;
export function config() {
  cfgPromise ??= fetch('/api/config').then((r) => r.json());
  return cfgPromise;
}

export function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

export function fmtKbps(k) {
  if (!k && k !== 0) return '—';
  return k >= 1000 ? (k / 1000).toFixed(2) + ' Mbps' : Math.round(k) + ' kbps';
}

export function fmtBytes(b) {
  if (b > 1e6) return (b / 1e6).toFixed(2) + ' MB';
  if (b > 1e3) return (b / 1e3).toFixed(1) + ' KB';
  return b + ' B';
}

export function since(iso) {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), ss = Math.floor(s % 60);
  return (h ? h + ':' : '') + String(m).padStart(2, '0') + ':' + String(ss).padStart(2, '0');
}

// Cor estável a partir de um texto (avatar de canal / de quem fala no chat).
const AVATAR_COLORS = ['#5865f2', '#3ba55c', '#faa61a', '#ed4245', '#eb459e', '#00a8fc', '#9b84ee', '#43b581'];
export function avatarColor(text) {
  let h = 0;
  for (const ch of String(text)) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return AVATAR_COLORS[h % AVATAR_COLORS.length];
}

// Ícones SVG simples (sem emoji): herdam a cor do texto.
const icon = (d) => `<svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${d}</svg>`;
const ICONS = {
  live: icon('<path d="M7 5l12 7-12 7z" fill="currentColor"/>'),
  plus: icon('<path d="M12 5v14M5 12h14"/>'),
  help: icon('<circle cx="12" cy="12" r="9"/><path d="M9.5 9.5a2.5 2.5 0 1 1 3.5 2.3c-.6.3-1 .9-1 1.6V14"/><path d="M12 17.5h.01"/>'),
  chart: icon('<path d="M4 19h16"/><path d="M7 15l4-5 3 3 5-7"/>'),
};

// Trilho lateral: um "ícone" redondo por canal ao vivo (vira quadrado
// arredondado no hover), com bolinha vermelha de AO VIVO.
function railItem({ href, label, content, active, cls = '', style = '', dot = false }) {
  return `<div class="rail-item ${cls} ${active ? 'active' : ''}">
    <span class="pill"></span>
    <a href="${href}" aria-label="${esc(label)}" data-tip="${esc(label)}" style="${style}">${content}</a>
    ${dot ? '<span class="dot"></span>' : ''}
  </div>`;
}

async function renderRail(rail) {
  const here = location.pathname;
  // todos os canais: ao vivo primeiro (com bolinha vermelha), offline depois
  const channels = await fetch('/api/channels').then((r) => r.json()).catch(() => []);
  const cfg = await config();
  rail.innerHTML = [
    railItem({ href: '/', label: 'Ao vivo', content: ICONS.live, active: here === '/' }),
    '<div class="sep"></div>',
    ...channels.map((c) => railItem({
      href: `/watch/${encodeURIComponent(c.name)}`,
      label: c.live ? `${c.name} · ao vivo · ${c.viewers} assistindo` : `${c.name} · offline`,
      content: esc(c.name[0].toUpperCase()),
      active: here === `/watch/${c.name}`,
      cls: c.live ? 'live' : 'offline',
      style: `background-color:${avatarColor(c.name)};color:#fff`,
      dot: c.live,
    })),
    channels.length ? '<div class="sep"></div>' : '',
    railItem({ href: '/studio', label: 'Estúdio: criar canal e transmitir', content: ICONS.plus, active: here === '/studio', cls: 'green' }),
    railItem({ href: '/learn', label: 'Aprenda', content: ICONS.help, active: here === '/learn', cls: 'green' }),
    railItem({ href: cfg.grafana, label: 'Grafana (abre em nova aba)', content: ICONS.chart }).replace('<a ', '<a target="_blank" rel="noopener" '),
  ].join('');
}

export function header(active) {
  const links = [
    ['/', 'Ao vivo'],
    ['/studio', 'Estúdio'],
    ['/learn', 'Aprenda'],
  ];
  const el = document.createElement('header');
  el.className = 'top';
  el.innerHTML = `
    <a class="brand" href="/">Stream<span>Lab</span></a>
    <nav>${links.map(([h, t]) => `<a href="${h}" class="${h === active ? 'active' : ''}">${t}</a>`).join('')}</nav>
    <div class="spacer"></div>
    <nav class="ext"><a id="lnk-grafana" target="_blank">Grafana ↗</a><a id="lnk-prom" target="_blank">Prometheus ↗</a></nav>`;
  document.body.prepend(el);

  const rail = document.createElement('nav');
  rail.className = 'rail';
  rail.setAttribute('aria-label', 'Canais');
  document.body.prepend(rail);
  renderRail(rail);
  setInterval(() => renderRail(rail), 10000);
  // o Estúdio avisa quando cria um canal: o ícone aparece na hora
  addEventListener('streamlab:channels', () => renderRail(rail));

  // tooltip fixo (o trilho tem overflow, então o balão não pode ficar dentro dele)
  const tip = document.createElement('div');
  tip.className = 'rail-tip';
  tip.hidden = true;
  document.body.append(tip);
  rail.addEventListener('mouseover', (e) => {
    const a = e.target.closest('a[data-tip]');
    if (!a) return;
    const r = a.getBoundingClientRect();
    tip.textContent = a.dataset.tip;
    tip.style.left = r.right + 14 + 'px';
    tip.style.top = r.top + r.height / 2 + 'px';
    tip.hidden = false;
  });
  rail.addEventListener('mouseout', (e) => { if (!e.relatedTarget?.closest?.('a[data-tip]')) tip.hidden = true; });

  config().then((c) => {
    $('#lnk-grafana').href = c.grafana;
    $('#lnk-prom').href = c.prometheus;
  });
}
