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

export function header(active) {
  const links = [
    ['/', 'Ao vivo'],
    ['/studio', 'Estúdio (transmitir)'],
    ['/learn', 'Aprenda'],
  ];
  const el = document.createElement('header');
  el.className = 'top';
  el.innerHTML = `
    <a class="brand" href="/">Stream<span>Lab</span></a>
    <nav>${links.map(([h, t]) => `<a href="${h}" class="${h === active ? 'active' : ''}">${t}</a>`).join('')}</nav>
    <div class="spacer"></div>
    <nav><a id="lnk-grafana" target="_blank">Grafana ↗</a><a id="lnk-prom" target="_blank">Prometheus ↗</a></nav>`;
  document.body.prepend(el);
  config().then((c) => {
    $('#lnk-grafana').href = c.grafana;
    $('#lnk-prom').href = c.prometheus;
  });
}
