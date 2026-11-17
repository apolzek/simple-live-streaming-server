// Desenhos da interface: todos em Mermaid, com as cores do tema.
import mermaid from 'https://cdn.jsdelivr.net/npm/mermaid@11.12.0/dist/mermaid.esm.min.mjs';

mermaid.initialize({
  startOnLoad: false,
  securityLevel: 'strict',
  theme: 'base',
  fontFamily: '"Noto Sans", Helvetica, Arial, sans-serif',
  themeVariables: {
    darkMode: true,
    background: '#313338',
    primaryColor: '#2b2d31',
    primaryBorderColor: '#5865f2',
    primaryTextColor: '#f2f3f5',
    secondaryColor: '#383a40',
    tertiaryColor: '#1e1f22',
    lineColor: '#949ba4',
    textColor: '#dbdee1',
    mainBkg: '#2b2d31',
    nodeBorder: '#5865f2',
    clusterBkg: '#1e1f22',
    clusterBorder: '#3f4147',
    edgeLabelBackground: '#313338',
    actorBkg: '#2b2d31',
    actorBorder: '#5865f2',
    actorTextColor: '#f2f3f5',
    signalColor: '#dbdee1',
    signalTextColor: '#dbdee1',
    noteBkgColor: '#383a40',
    noteTextColor: '#dbdee1',
    noteBorderColor: '#5865f2',
    fontSize: '15px',
    xyChart: {
      backgroundColor: '#2b2d31',
      titleColor: '#f2f3f5',
      xAxisLabelColor: '#dbdee1',
      yAxisLabelColor: '#dbdee1',
      xAxisTitleColor: '#949ba4',
      yAxisTitleColor: '#949ba4',
      xAxisLineColor: '#6d6f78',
      yAxisLineColor: '#6d6f78',
      xAxisTickColor: '#6d6f78',
      yAxisTickColor: '#6d6f78',
      plotColorPalette: '#5865f2',
    },
  },
  flowchart: { curve: 'basis', htmlLabels: true, nodeSpacing: 28, rankSpacing: 36, padding: 10 },
});

let seq = 0;
const last = new WeakMap();

// Renderiza `code` dentro de `el`. Só redesenha se o texto mudou.
export async function draw(el, code) {
  if (last.get(el) === code) return;
  last.set(el, code);
  try {
    const { svg } = await mermaid.render(`mmd-${++seq}`, code);
    el.innerHTML = svg;
  } catch (e) {
    el.textContent = 'erro no diagrama: ' + e.message;
  }
}

// Renderiza todos os <pre class="mermaid"> estáticos da página.
export async function drawAll(root = document) {
  for (const el of root.querySelectorAll('pre.mermaid')) {
    const div = document.createElement('div');
    div.className = 'diagram';
    el.replaceWith(div);
    await draw(div, el.textContent);
  }
}
