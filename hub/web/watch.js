// Página de um canal: player (HLS / LL-HLS / WebRTC), stats, QoE e chat.
import { $, $$, config, esc, header, since, fmtKbps, fmtBytes, avatarColor } from '/static/common.js';
import { draw } from '/static/diagram.js';

header('/watch');
const channel = location.pathname.split('/').pop();
const video = $('#video');
const cfg = await config();

const store = {
  get: (k, d) => { try { return localStorage.getItem(k) ?? d; } catch { return d; } },
  set: (k, v) => { try { localStorage.setItem(k, v); } catch {} },
};

// ============================================================ estado geral
let mode = new URLSearchParams(location.search).get('mode') || store.get('mode', 'hls');
let hls = null;           // instância hls.js
let pc = null;            // RTCPeerConnection (WebRTC)
let whepSession = null;   // URL da sessão WHEP (para DELETE)
let info = null;          // /api/channels/<canal>
let lastSdp = { offer: '', answer: '' };
let rtc = { kbps: 0, fps: 0, rtt: 0, jitter: 0, lost: 0, latency: 0, res: '', codec: '', lastBytes: 0, lastTs: 0 };
const segs = [];          // últimas requisições de segmento
let cache = { hit: 0, miss: 0 };

// ============================================================ QoE (beacons)
// O player é o único que sabe o que o espectador está vivendo. Mandamos
// eventos para o hub, que os transforma em métricas no Prometheus.
const sessionId = crypto.randomUUID();
const qoeQueue = [];
let qoe = { t0: 0, started: false, stallAt: 0, lastBitrate: 0 };
function beacon(event, data = {}) {
  qoeQueue.push({ session: sessionId, channel, mode, event, ...data });
}
function flushQoE(useBeacon = false) {
  if (!qoeQueue.length) return;
  const body = JSON.stringify(qoeQueue.splice(0));
  if (useBeacon && navigator.sendBeacon) navigator.sendBeacon('/api/qoe', body);
  else fetch('/api/qoe', { method: 'POST', body, keepalive: true }).catch(() => {});
}
setInterval(() => {
  if (qoe.started && !video.paused) {
    beacon('heartbeat', {
      latency_s: currentLatency() || 0,
      buffer_s: bufferAhead(),
      bitrate_kbps: currentBitrate(),
      dropped_frames: video.getVideoPlaybackQuality?.().droppedVideoFrames || 0,
      interval_s: 5,
    });
  }
  flushQoE();
}, 5000);
addEventListener('pagehide', () => flushQoE(true));

video.addEventListener('playing', () => {
  if (!qoe.started && qoe.t0) {
    qoe.started = true;
    beacon('startup', { startup_ms: performance.now() - qoe.t0 });
  }
  if (qoe.stallAt) {
    beacon('rebuffer', { rebuffer_ms: performance.now() - qoe.stallAt });
    qoe.stallAt = 0;
  }
});
video.addEventListener('waiting', () => {
  if (qoe.started && !qoe.stallAt) qoe.stallAt = performance.now();
});

// ============================================================ métricas do player
function bufferAhead() {
  const b = video.buffered;
  for (let i = 0; i < b.length; i++) {
    if (b.start(i) <= video.currentTime + 0.1 && b.end(i) >= video.currentTime) return +(b.end(i) - video.currentTime).toFixed(2);
  }
  return 0;
}
function currentLatency() {
  if (mode === 'webrtc') return rtc.latency;
  return hls?.latency && isFinite(hls.latency) ? +hls.latency.toFixed(2) : 0;
}
function currentBitrate() {
  if (mode === 'webrtc') return Math.round(rtc.kbps);
  const lvl = hls?.levels?.[hls.currentLevel];
  return lvl ? Math.round(lvl.bitrate / 1000) : 0;
}

// ============================================================ HLS / LL-HLS
function playHLS(url, lowLatency) {
  hls = new Hls({
    lowLatencyMode: lowLatency,
    liveSyncDurationCount: lowLatency ? undefined : 3, // fica 3 segmentos atrás do "agora"
    backBufferLength: 30,
    // CMCD (CTA-5004): o player conta ao CDN o estado dele em cada requisição.
    // Veja no log do edge: docker compose logs -f edge
    cmcd: { sessionId, contentId: channel, useHeaders: false },
  });
  hls.loadSource(url);
  hls.attachMedia(video);
  hls.on(Hls.Events.MANIFEST_PARSED, () => { fillQualities(); video.play().catch(() => {}); });
  hls.on(Hls.Events.LEVEL_SWITCHED, (_, d) => {
    const br = hls.levels[d.level]?.bitrate || 0;
    if (qoe.lastBitrate && br !== qoe.lastBitrate) beacon('switch', { direction: br > qoe.lastBitrate ? 'up' : 'down' });
    qoe.lastBitrate = br;
    fillQualities();
  });
  hls.on(Hls.Events.FRAG_LOADED, (_, d) => logFragment(d));
  hls.on(Hls.Events.ERROR, (_, d) => {
    if (!d.fatal) return;
    beacon('error', { kind: d.type === Hls.ErrorTypes.NETWORK_ERROR ? 'network' : d.type === Hls.ErrorTypes.MEDIA_ERROR ? 'media' : 'fatal' });
    if (d.type === Hls.ErrorTypes.MEDIA_ERROR) hls.recoverMediaError();
    else setTimeout(() => start(), 3000);
  });
}

function logFragment(d) {
  const f = d.part || d.frag;
  const st = f.stats || d.frag.stats;
  const ms = st.loading.end - st.loading.start;
  let xcache = '';
  try { xcache = d.networkDetails?.getResponseHeader?.('X-Cache') || ''; } catch {}
  if (xcache === 'HIT') cache.hit++; else if (xcache) cache.miss++;
  const name = (f.url || d.frag.url || '').split('/').slice(-2).join('/').split('?')[0];
  segs.unshift({
    t: new Date().toISOString().slice(11, 19),
    name: d.part ? name + ` (part ${d.part.index})` : name,
    level: hls.levels[d.frag.level]?.height ? hls.levels[d.frag.level].height + 'p' : d.frag.level,
    dur: (f.duration || 0).toFixed(2) + 's',
    size: st.loaded,
    ms,
    kbps: ms > 0 ? (st.loaded * 8) / ms : 0,
    xcache,
  });
  segs.length = Math.min(segs.length, 60);
}

function fillQualities() {
  const sel = $('#quality');
  if (mode === 'webrtc' || !hls) { sel.innerHTML = '<option>original</option>'; sel.disabled = true; return; }
  sel.disabled = mode === 'llhls';
  const cur = hls.autoLevelEnabled ? -1 : hls.currentLevel;
  const opts = [`<option value="-1" ${cur === -1 ? 'selected' : ''}>Auto (ABR)${hls.autoLevelEnabled && hls.levels[hls.currentLevel] ? ' · ' + hls.levels[hls.currentLevel].height + 'p' : ''}</option>`];
  hls.levels.forEach((l, i) => opts.push(`<option value="${i}" ${cur === i ? 'selected' : ''}>${l.height}p · ${fmtKbps(l.bitrate / 1000)}</option>`));
  sel.innerHTML = opts.join('');
}
$('#quality').onchange = (e) => { if (hls) hls.currentLevel = +e.target.value; };

// ============================================================ WebRTC (WHEP)
// WHEP = "WebRTC-HTTP Egress Protocol": um único POST com a oferta SDP e a
// resposta é a answer SDP. Depois disso, mídia flui por RTP/UDP (SRTP).
async function playWebRTC() {
  pc = new RTCPeerConnection();
  pc.addTransceiver('video', { direction: 'recvonly' });
  pc.addTransceiver('audio', { direction: 'recvonly' });
  pc.ontrack = (e) => { if (video.srcObject !== e.streams[0]) { video.srcObject = e.streams[0]; video.play().catch(() => {}); } };
  pc.onconnectionstatechange = () => {
    if (pc && ['failed'].includes(pc.connectionState)) { beacon('error', { kind: 'ice' }); setTimeout(() => start(), 3000); }
  };
  const offer = await pc.createOffer();
  await pc.setLocalDescription(offer);
  // espera os candidatos ICE locais (até 1s) para mandar tudo em um POST só
  await new Promise((r) => { if (pc.iceGatheringState === 'complete') r(); pc.onicegatheringstatechange = () => pc.iceGatheringState === 'complete' && r(); setTimeout(r, 1000); });
  lastSdp.offer = pc.localDescription.sdp;
  const res = await fetch(`${cfg.webrtc}/${channel}/whep`, { method: 'POST', headers: { 'Content-Type': 'application/sdp' }, body: lastSdp.offer });
  if (!res.ok) throw new Error('WHEP ' + res.status);
  const loc = res.headers.get('Location');
  whepSession = loc ? new URL(loc, `${cfg.webrtc}/${channel}/whep`).href : null;
  lastSdp.answer = await res.text();
  await pc.setRemoteDescription({ type: 'answer', sdp: lastSdp.answer });
}

async function pollRTCStats() {
  if (!pc) return;
  const stats = await pc.getStats();
  let inV, pair, codecs = {};
  stats.forEach((s) => {
    if (s.type === 'inbound-rtp' && s.kind === 'video') inV = s;
    if (s.type === 'candidate-pair' && s.nominated && s.state === 'succeeded') pair = s;
    if (s.type === 'codec') codecs[s.id] = s.mimeType;
  });
  if (inV) {
    const now = inV.timestamp;
    if (rtc.lastTs) rtc.kbps = ((inV.bytesReceived - rtc.lastBytes) * 8) / (now - rtc.lastTs);
    rtc.lastBytes = inV.bytesReceived; rtc.lastTs = now;
    rtc.fps = inV.framesPerSecond || 0;
    rtc.jitter = (inV.jitter || 0) * 1000;
    rtc.lost = inV.packetsLost || 0;
    rtc.res = inV.frameWidth ? `${inV.frameWidth}x${inV.frameHeight}` : '';
    rtc.codec = codecs[inV.codecId] || '';
    const jb = inV.jitterBufferEmittedCount ? inV.jitterBufferDelay / inV.jitterBufferEmittedCount : 0;
    rtc.rtt = pair?.currentRoundTripTime ? pair.currentRoundTripTime * 1000 : 0;
    // estimativa: metade do RTT (rede) + jitter buffer. Não inclui encoder/captura.
    rtc.latency = +(jb + rtc.rtt / 2000).toFixed(3);
  }
}
setInterval(pollRTCStats, 1000);

// ============================================================ ciclo de vida
async function stop() {
  if (hls) { hls.destroy(); hls = null; }
  if (pc) { pc.close(); pc = null; }
  if (whepSession) { fetch(whepSession, { method: 'DELETE' }).catch(() => {}); whepSession = null; }
  video.removeAttribute('src'); video.srcObject = null; video.load();
  segs.length = 0; cache = { hit: 0, miss: 0 };
  rtc = { kbps: 0, fps: 0, rtt: 0, jitter: 0, lost: 0, latency: 0, res: '', codec: '', lastBytes: 0, lastTs: 0 };
}

async function start() {
  await stop();
  flushQoE();
  qoe = { t0: performance.now(), started: false, stallAt: 0, lastBitrate: 0 };
  $$('[data-mode]').forEach((b) => b.classList.toggle('on', b.dataset.mode === mode));
  if (!info?.live) return;
  try {
    if (mode === 'webrtc') await playWebRTC();
    else if (mode === 'llhls') playHLS(`${cfg.ll_hls}/${channel}/index.m3u8`, true);
    else playHLS(`${cfg.edge_hls}/${channel}/master.m3u8`, false);
  } catch (e) {
    console.error(e);
    beacon('error', { kind: 'other' });
    setTimeout(start, 3000);
  }
  fillQualities();
  renderCLI();
}

$$('[data-mode]').forEach((b) => (b.onclick = () => { mode = b.dataset.mode; store.set('mode', mode); start(); }));
$('#btn-unmute').onclick = () => { video.muted = false; video.play(); };
let nerdOn = store.get('nerd', '1') === '1';
const syncNerd = () => { $('#nerd').hidden = !nerdOn; $('#btn-nerd').classList.toggle('on', nerdOn); };
$('#btn-nerd').onclick = () => { nerdOn = !nerdOn; store.set('nerd', nerdOn ? '1' : '0'); syncNerd(); };
syncNerd();

$$('#tabs button').forEach((b) => (b.onclick = () => {
  $$('#tabs button').forEach((x) => x.classList.toggle('active', x === b));
  $$('.tab').forEach((t) => t.classList.toggle('active', t.id === 'tab-' + b.dataset.tab));
}));

// ============================================================ info do canal
async function loadInfo() {
  const r = await fetch(`/api/channels/${channel}`);
  if (!r.ok) { $('#title').textContent = 'Canal não encontrado'; $('#offline').hidden = false; return; }
  const was = info?.live;
  info = await r.json();
  $('#title').textContent = info.title;
  $('#chname').textContent = info.name;
  $('#avatar').textContent = info.name[0].toUpperCase();
  $('#avatar').style.background = avatarColor(info.name);
  document.title = `${info.name} · StreamLab`;
  $('#live-badge').innerHTML = info.live ? '<span class="badge live">AO VIVO</span>' : '<span class="badge proto">OFFLINE</span>';
  $('#offline').hidden = info.live;
  $('#uptime').textContent = info.live ? 'no ar há ' + since(info.started_at) : '';
  if (info.live && !was) start();
  if (!info.live && was) stop();
}
await loadInfo();
setInterval(loadInfo, 3000);
setInterval(() => { $('#clock').textContent = new Date().toISOString().slice(11, 21); }, 100);

// ============================================================ renderização
function row(k, v) { return `<tr><td>${k}</td><td>${v}</td></tr>`; }
function renderNerd() {
  if (!nerdOn) return;
  const q = video.getVideoPlaybackQuality?.() || {};
  let rows = row('modo', { hls: 'HLS ABR via edge', llhls: 'LL-HLS (MediaMTX)', webrtc: 'WebRTC / WHEP' }[mode]);
  rows += row('resolução', `${video.videoWidth}x${video.videoHeight}`);
  rows += row('latência', currentLatency() ? currentLatency().toFixed(2) + ' s' + (mode === 'webrtc' ? ' (estimada)' : '') : '—');
  if (mode !== 'webrtc' && hls) {
    const lvl = hls.levels[hls.currentLevel];
    rows += row('qualidade', lvl ? `${lvl.height}p · ${fmtKbps(lvl.bitrate / 1000)}${hls.autoLevelEnabled ? ' (auto)' : ''}` : '—');
    rows += row('banda estimada', fmtKbps(hls.bandwidthEstimate / 1000));
    rows += row('buffer', bufferAhead().toFixed(2) + ' s');
    rows += row('alvo de latência', hls.targetLatency ? hls.targetLatency.toFixed(1) + ' s' : '—');
    rows += row('taxa de reprodução', video.playbackRate.toFixed(2) + 'x');
  } else if (pc) {
    rows += row('codec', rtc.codec);
    rows += row('bitrate', fmtKbps(rtc.kbps));
    rows += row('fps', rtc.fps);
    rows += row('RTT', rtc.rtt.toFixed(1) + ' ms');
    rows += row('jitter', rtc.jitter.toFixed(1) + ' ms');
    rows += row('pacotes perdidos', rtc.lost);
    rows += row('ICE', pc.iceConnectionState);
  }
  rows += row('quadros perdidos', `${q.droppedVideoFrames ?? 0} / ${q.totalVideoFrames ?? 0}`);
  $('#nerd-t').innerHTML = rows;
}

// O caminho do vídeo, desenhado em Mermaid com os números ao vivo.
// As três rotas aparecem; a rota do modo atual fica destacada.
const q = (t) => String(t ?? '').replace(/["<>]/g, ''); // texto seguro dentro de ["..."]
function pipelineDiagram() {
  const tc = info.transcoder;
  const tot = cache.hit + cache.miss;
  const lat = currentLatency();
  const kbps = Math.round((info.ingest_kbps || 0) / 100) * 100;
  const L = {
    enc: 'Encoder<br/>OBS · ffmpeg<br/>navegador',
    ing: `MediaMTX: ingest<br/><b>${q(info.ingest || '?')}</b><br/>${q(fmtKbps(kbps))} · ${info.width || '?'}x${info.height || '?'}<br/>${q((info.tracks || []).join(' + '))}`,
    tc: `Transcoder FFmpeg<br/><b>${(info.renditions || []).length} qualidades</b><br/>${q((info.renditions || []).join(' · '))}<br/>${tc ? `${Math.round(tc.fps)} fps · ${tc.speed}x tempo real` : 'iniciando'}`,
    pkg: 'Packager<br/><b>HLS / CMAF</b><br/>fMP4 de 2s',
    edge: `Edge / CDN<br/><b>${tot ? Math.round((cache.hit / tot) * 100) + '% HIT' : 'sem dados'}</b>`,
    ll: 'Remux LL-HLS<br/>partes de 200ms',
    rtc: 'Remux WebRTC<br/>RTP/SRTP · UDP',
    ply: `Player<br/><b>${lat ? (Math.round(lat * 2) / 2).toFixed(1) + ' s de latência' : 'latência —'}</b><br/>${video.videoWidth}x${video.videoHeight} · buffer ${(Math.round(bufferAhead() * 2) / 2).toFixed(1)}s`,
  };
  const route = { hls: ['tc', 'pkg', 'edge'], llhls: ['ll'], webrtc: ['rtc'] }[mode];
  const on = new Set(['enc', 'ing', 'ply', ...route]);
  const cls = (n) => (on.has(n) ? 'on' : 'off');
  // índice das arestas para destacar a rota ativa (linkStyle)
  const edges = [
    ['enc', 'ing', 'RTMP · SRT · WHIP'],
    ['ing', 'tc', 'RTSP'],
    ['tc', 'pkg', ''],
    ['pkg', 'edge', 'm3u8 + m4s'],
    ['edge', 'ply', 'HLS'],
    ['ing', 'll', ''],
    ['ll', 'ply', 'LL-HLS'],
    ['ing', 'rtc', ''],
    ['rtc', 'ply', 'WHEP'],
  ];
  const lines = [
    'flowchart LR',
    ...Object.entries(L).map(([k, v]) => `  ${k}["${v}"]:::${cls(k)}`),
    ...edges.map(([a, b, t]) => `  ${a} -->${t ? `|"${t}"|` : ''} ${b}`),
    '  classDef on fill:#2b2d31,stroke:#5865f2,stroke-width:2px,color:#f2f3f5',
    '  classDef off fill:#232428,stroke:#3f4147,color:#6d6f78',
  ];
  edges.forEach(([a, b], i) => {
    const active = on.has(a) && on.has(b);
    lines.push(`  linkStyle ${i} stroke:${active ? '#5865f2' : '#3f4147'},stroke-width:${active ? 3 : 1}px`);
  });
  return lines.join('\n');
}

function renderPipeline() {
  if (!info || !$('#tab-pipeline').classList.contains('active')) return;
  if (!info.live) { $('#pipeline').textContent = 'Canal offline.'; return; }
  draw($('#pipeline'), pipelineDiagram());
  $('#pipeline-note').innerHTML = {
    hls: 'HLS ABR: o vídeo é re-codificado em várias qualidades, cortado em arquivos de 2s e servido por HTTP com cache. Escala para milhões, mas a latência fica em ~6–10s (o player precisa de alguns segmentos no buffer).',
    llhls: 'LL-HLS: cada segmento é publicado em "partes" de ~200ms e a playlist é bloqueante (o player pede a próxima parte antes dela existir). Latência ~2–3s, ainda sobre HTTP/CDN.',
    webrtc: 'WebRTC: nada de arquivos — pacotes RTP chegam por UDP assim que o encoder os produz. Latência &lt; 1s, mas cada espectador é uma conexão com estado no servidor (escalar é caro).',
  }[mode];
}

function renderSegments() {
  $('#seglog').innerHTML = segs.map((s) => `<tr><td>${s.t}</td><td>${esc(s.name)}</td><td>${esc(s.level)}</td><td>${s.dur}</td><td>${fmtBytes(s.size)}</td><td>${Math.round(s.ms)} ms</td><td>${fmtKbps(s.kbps)}</td>
    <td class="${s.xcache === 'HIT' ? 'hit' : 'miss'}">${esc(s.xcache || (mode === 'llhls' ? 'origem' : '—'))}</td></tr>`).join('');
  const tot = cache.hit + cache.miss;
  $('#cache-ratio').textContent = mode === 'hls' ? `Cache hit ratio desta sessão: ${tot ? Math.round((cache.hit / tot) * 100) : 0}% (${cache.hit} HIT / ${cache.miss} MISS). Abra a mesma live em outra aba e veja os HITs subirem.` : mode === 'webrtc' ? 'WebRTC não usa segmentos: veja a aba "Playlist / SDP".' : 'LL-HLS aqui vem direto do MediaMTX (sem edge).';
}

const TAGS = {
  '#EXTM3U': 'Assinatura: todo arquivo HLS começa assim.',
  '#EXT-X-VERSION': 'Versão do protocolo (7 = suporta fMP4/CMAF e EXT-X-MAP).',
  '#EXT-X-STREAM-INF': 'Master playlist: uma variante (qualidade). BANDWIDTH é o pico em bits/s, RESOLUTION e CODECS ajudam o player a escolher.',
  '#EXT-X-TARGETDURATION': 'Duração máxima de um segmento. O player recarrega a playlist mais ou menos nesse intervalo.',
  '#EXT-X-MEDIA-SEQUENCE': 'Número do primeiro segmento da lista. Numa live ele sobe sem parar (janela deslizante).',
  '#EXT-X-INDEPENDENT-SEGMENTS': 'Todo segmento começa com keyframe: dá para trocar de qualidade em qualquer fronteira.',
  '#EXT-X-MAP': 'Init segment (fMP4): cabeçalho com codec/resolução, baixado uma vez.',
  '#EXTINF': 'Duração exata do próximo segmento.',
  '#EXT-X-PROGRAM-DATE-TIME': 'Hora real em que o segmento foi gravado. É assim que o player calcula a latência.',
  '#EXT-X-PART': 'LL-HLS: uma "parte" (~200ms) de um segmento ainda em produção.',
  '#EXT-X-PART-INF': 'LL-HLS: duração alvo das partes.',
  '#EXT-X-SERVER-CONTROL': 'LL-HLS: CAN-BLOCK-RELOAD permite pedir a playlist "futura" (_HLS_msn) e o servidor segura a resposta até ela existir.',
  '#EXT-X-PRELOAD-HINT': 'LL-HLS: o próximo arquivo que vai existir. O player já pede antes para economizar um round-trip.',
  '#EXT-X-RENDITION-REPORT': 'LL-HLS: onde as outras qualidades estão, para trocar sem recarregar tudo.',
  '#EXT-X-SKIP': 'LL-HLS: delta update, pula segmentos que o player já conhece.',
  '#EXT-X-MEDIA': 'Trilha alternativa (ex.: áudio separado do vídeo).',
};
const SDP = {
  'v=': 'Versão do SDP.',
  'o=': 'Origem da sessão.',
  'm=': 'Uma trilha de mídia (audio/video), porta e lista de payload types.',
  'a=rtpmap': 'Mapeia payload type → codec (ex.: 96 H264/90000).',
  'a=fmtp': 'Parâmetros do codec (perfil H.264, packetization-mode…).',
  'a=candidate': 'Candidato ICE: um IP:porta onde o outro lado pode tentar se conectar (host, srflx via STUN, relay via TURN).',
  'a=ice-ufrag': 'Credenciais ICE para as checagens de conectividade (STUN).',
  'a=fingerprint': 'Hash do certificado DTLS: a mídia é sempre criptografada (SRTP).',
  'a=setup': 'Quem inicia o handshake DTLS.',
  'a=recvonly': 'Esta ponta só recebe (WHEP = assistir).',
  'a=sendonly': 'Esta ponta só envia.',
  'a=rtcp-fb': 'Feedback RTCP: NACK (pedir retransmissão), PLI (pedir keyframe), transport-cc (controle de congestionamento).',
  'a=mid': 'Identificador da trilha dentro do BUNDLE.',
  'a=group:BUNDLE': 'Todas as trilhas usam uma única conexão/porta.',
};
function highlight(text, isSDP) {
  return text.split('\n').map((l) => {
    const e = esc(l);
    if (isSDP) return /^a=candidate/.test(l) ? `<span class="uri">${e}</span>` : /^m=/.test(l) ? `<span class="tag">${e}</span>` : e;
    if (l.startsWith('#')) return `<span class="tag">${e}</span>`;
    if (l.trim()) return `<span class="uri">${e}</span>`;
    return e;
  }).join('\n');
}
let plBusy = false;
async function renderPlaylist() {
  if (!$('#tab-playlist').classList.contains('active') || plBusy) return;
  plBusy = true;
  try {
    if (mode === 'webrtc') {
      $('#pl-title').textContent = 'SDP answer do servidor (WHEP)';
      $('#playlist').innerHTML = highlight(lastSdp.answer || '(conectando…)', true);
      $('#pl-help').innerHTML = Object.entries(SDP).map(([k, v]) => `<p><code>${esc(k)}</code> ${esc(v)}</p>`).join('') +
        '<p class="muted">A oferta (offer) enviada pelo navegador está no console: <code>copy(window.lastSdp.offer)</code>.</p>';
      window.lastSdp = lastSdp;
    } else if (hls) {
      const master = hls.url;
      const lvl = hls.levels[hls.currentLevel];
      const url = mode === 'llhls' ? master : lvl?.url?.[0] || master;
      const [m, p] = await Promise.all([
        mode === 'hls' ? fetch(master).then((r) => r.text()) : Promise.resolve(''),
        fetch(url).then((r) => r.text()),
      ]);
      $('#pl-title').textContent = mode === 'hls' ? `master.m3u8 + playlist da qualidade atual (${url.split('/').slice(-2).join('/')})` : 'Playlist LL-HLS';
      $('#playlist').innerHTML = (m ? highlight(m) + '\n<span class="cmt"># ------------------------------------------- media playlist</span>\n' : '') + highlight(p);
      const used = new Set([...(m + p).matchAll(/^(#EXT[A-Z0-9-]*)/gm)].map((x) => x[1]));
      $('#pl-help').innerHTML = [...used].filter((t) => TAGS[t]).map((t) => `<p><code>${t}</code> ${esc(TAGS[t])}</p>`).join('');
    }
  } catch (e) { $('#playlist').textContent = 'erro: ' + e.message; }
  plBusy = false;
}

function renderCLI() {
  $('#cli').textContent = `# 1) a master playlist (lista de qualidades)
curl -s ${cfg.edge_hls}/${channel}/master.m3u8

# 2) a playlist de uma qualidade (lista de segmentos, muda a cada 2s)
curl -s ${cfg.edge_hls}/${channel}/480p/index.m3u8

# 3) veja o cache do edge funcionando (rode duas vezes: MISS e depois HIT)
curl -sI ${cfg.edge_hls}/${channel}/source/init_0.mp4 | grep -i x-cache

# 4) assistir com ffplay / mpv / VLC
ffplay ${cfg.edge_hls}/${channel}/master.m3u8         # HLS ABR (via CDN)
ffplay ${cfg.ll_hls}/${channel}/index.m3u8            # LL-HLS
ffplay -fflags nobuffer ${cfg.rtmp}/${channel}        # RTMP direto (só para teste)
ffplay "${cfg.srt}?streamid=read:live/${channel}"     # SRT

# 5) inspecionar o sinal original
ffprobe -hide_banner ${cfg.rtmp}/${channel}

# 6) logs do edge com CMCD (o que cada player conta para o CDN)
docker compose logs -f edge | grep ${channel}`;
}

setInterval(() => { renderNerd(); renderSegments(); }, 1000);
setInterval(renderPipeline, 2000);
setInterval(renderPlaylist, 2000);
$$('#tabs button').forEach((b) => b.addEventListener('click', () => setTimeout(() => { renderPlaylist(); renderPipeline(); }, 50)));

// ============================================================ chat
const nickIn = $('#nick');
nickIn.value = store.get('nick', 'viewer' + Math.floor(Math.random() * 9000 + 1000));
let ws;
let lastMsg = { nick: '', ts: 0 };
function connectChat() {
  lastMsg = { nick: '', ts: 0 };
  $('#msgs').innerHTML = '';
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  ws = new WebSocket(`${proto}://${location.host}/ws/chat/${channel}?nick=${encodeURIComponent(nickIn.value)}`);
  ws.onopen = () => ($('#ws-state').textContent = 'conectado via WebSocket');
  ws.onclose = () => { $('#ws-state').textContent = 'reconectando…'; setTimeout(connectChat, 2000); };
  ws.onmessage = (e) => {
    const m = JSON.parse(e.data);
    if (m.type === 'viewers') { $('#viewers').textContent = `${m.count} assistindo`; return; }
    const box = $('#msgs');
    const stick = box.scrollTop + box.clientHeight >= box.scrollHeight - 30;
    const div = document.createElement('div');
    // (não usar a classe .chat na mensagem: é a do painel)
    if (m.type === 'chat') {
      const d = new Date(m.ts);
      const hhmm = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
      // mensagens seguidas da mesma pessoa (em até 5 min) são agrupadas sem repetir avatar/nome
      const grouped = lastMsg.nick === m.nick && m.ts - lastMsg.ts < 5 * 60 * 1000;
      div.className = 'msg msg-chat' + (grouped ? '' : ' first');
      div.innerHTML = grouped
        ? `<span class="hover-time">${hhmm}</span><div class="text">${esc(m.text)}</div>`
        : `<div class="ava" style="background:${avatarColor(m.nick)}">${esc(m.nick[0].toUpperCase())}</div>
           <div class="who"><span class="nick" style="color:${esc(m.color)}">${esc(m.nick)}</span><span class="time">hoje às ${hhmm}</span></div>
           <div class="text">${esc(m.text)}</div>`;
      lastMsg = { nick: m.nick, ts: m.ts };
    } else {
      div.className = 'msg msg-system';
      div.textContent = m.text;
      lastMsg = { nick: '', ts: 0 };
    }
    box.append(div);
    while (box.children.length > 200) box.firstChild.remove();
    if (stick) box.scrollTop = box.scrollHeight;
  };
}
connectChat();
nickIn.onchange = () => { store.set('nick', nickIn.value); ws.onclose = null; ws.close(); connectChat(); };
$('#chat-form').onsubmit = (e) => {
  e.preventDefault();
  const t = $('#chat-input').value.trim();
  if (t && ws.readyState === 1) ws.send(JSON.stringify({ text: t }));
  $('#chat-input').value = '';
};
