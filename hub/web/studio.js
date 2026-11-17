import { $, config, esc, header, fmtKbps } from '/static/common.js';

header('/studio');
const cfg = await config();

const KEYS = 'streamlab.keys';
const loadKeys = () => { try { return JSON.parse(localStorage.getItem(KEYS) || '{}'); } catch { return {}; } };
const saveKey = (name, key) => { const k = loadKeys(); k[name] = key; try { localStorage.setItem(KEYS, JSON.stringify(k)); } catch {} };

let current = null; // { name, key }

function refreshMine() {
  const keys = loadKeys();
  const names = Object.keys(keys);
  $('#mine').innerHTML = '<option value="">—</option>' + names.map((n) => `<option ${current?.name === n ? 'selected' : ''}>${esc(n)}</option>`).join('');
}
$('#mine').onchange = (e) => e.target.value && select(e.target.value, loadKeys()[e.target.value]);

function select(name, key) {
  current = { name, key };
  $('#settings').hidden = false;
  $('#browser').hidden = false;
  $('#watch-link').href = `/watch/${name}`;
  $('#watch-link').textContent = `${location.origin}/watch/${name}`;
  $('#key').value = key;
  $('#obs-server').value = cfg.rtmp;
  $('#obs-key').value = `${name}?key=${key}`;
  $('#whip-url').value = `${cfg.webrtc}/${name}/whip`;
  $('#whip-token').value = key;
  $('#ffmpeg-rtmp').textContent = `ffmpeg -re -f lavfi -i testsrc2=size=1280x720:rate=30 -f lavfi -i sine \\
  -c:v libx264 -preset veryfast -tune zerolatency -b:v 3000k -g 60 -keyint_min 60 \\
  -c:a aac -b:a 128k -f flv "${cfg.rtmp}/${name}?key=${key}"`;
  $('#ffmpeg-srt').textContent = `ffmpeg -re -f lavfi -i testsrc2=size=1280x720:rate=30 -f lavfi -i sine \\
  -c:v libx264 -preset veryfast -tune zerolatency -b:v 3000k -g 60 -keyint_min 60 \\
  -c:a aac -b:a 128k -f mpegts "${cfg.srt}?streamid=publish:live/${name}:${name}:${key}&pkt_size=1316"`;
  refreshMine();
}

// ================================================================ recuperar
async function loadAllChannels() {
  const list = await fetch('/api/channels').then((r) => r.json()).catch(() => []);
  const sel = $('#rec-name');
  const cur = sel.value;
  sel.innerHTML = list.map((c) => `<option value="${esc(c.name)}">${esc(c.name)}${c.live ? ' (ao vivo)' : ''}</option>`).join('');
  if (cur) sel.value = cur;
}
loadAllChannels();
addEventListener('streamlab:channels', loadAllChannels);

async function recover(rotate) {
  const name = $('#rec-name').value;
  if (!name) return;
  if (rotate && !confirmRotate(name)) return;
  $('#rec-msg').textContent = rotate ? 'gerando nova chave…' : 'recuperando…';
  const r = await fetch(`/api/channels/${encodeURIComponent(name)}/key`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password: $('#rec-pass').value, rotate }),
  });
  const body = await r.json().catch(() => ({}));
  if (!r.ok) { $('#rec-msg').textContent = body.error || `erro ${r.status}`; return; }
  saveKey(body.name, body.key);
  select(body.name, body.key);
  $('#rec-msg').textContent = rotate
    ? `Nova stream key gerada para ${body.name}. Atualize o OBS: a antiga não funciona mais.`
    : `Stream key de ${body.name} recuperada. A configuração do encoder está ao lado.`;
}
// confirmação sem diálogo do navegador: o botão pede um segundo clique
let rotateArmed = '';
function confirmRotate(name) {
  if (rotateArmed === name) { rotateArmed = ''; $('#rotate').textContent = 'Gerar nova stream key'; return true; }
  rotateArmed = name;
  $('#rotate').textContent = 'Clique de novo para confirmar';
  $('#rec-msg').textContent = `Isso invalida a chave atual de ${name}.`;
  setTimeout(() => { if (rotateArmed === name) { rotateArmed = ''; $('#rotate').textContent = 'Gerar nova stream key'; } }, 5000);
  return false;
}
// excluir (também pede um segundo clique para confirmar)
let deleteArmed = '';
$('#delete').onclick = async () => {
  const name = $('#rec-name').value;
  if (!name) return;
  if (deleteArmed !== name) {
    deleteArmed = name;
    $('#delete').textContent = `Confirmar exclusão de ${name}`;
    $('#rec-msg').textContent = `Clique de novo para excluir ${name} para sempre.`;
    setTimeout(() => { if (deleteArmed === name) { deleteArmed = ''; $('#delete').textContent = 'Excluir canal'; } }, 5000);
    return;
  }
  deleteArmed = '';
  $('#delete').textContent = 'Excluir canal';
  const r = await fetch(`/api/channels/${encodeURIComponent(name)}`, {
    method: 'DELETE',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password: $('#rec-pass').value }),
  });
  const body = await r.json().catch(() => ({}));
  if (!r.ok) { $('#rec-msg').textContent = body.error || `erro ${r.status}`; return; }
  const keys = loadKeys();
  delete keys[name];
  try { localStorage.setItem(KEYS, JSON.stringify(keys)); } catch {}
  if (current?.name === name) { current = null; $('#settings').hidden = true; $('#browser').hidden = true; }
  refreshMine();
  $('#rec-msg').textContent = `Canal ${name} excluído.`;
  dispatchEvent(new Event('streamlab:channels'));
};

$('#recover').onsubmit = (e) => { e.preventDefault(); recover(false); };
$('#rotate').onclick = () => recover(true);

$('#show').onclick = () => { const k = $('#key'); k.type = k.type === 'password' ? 'text' : 'password'; };

$('#create').onsubmit = async (e) => {
  e.preventDefault();
  const r = await fetch('/api/channels', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: $('#name').value, title: $('#ctitle').value }) });
  const body = await r.json();
  if (!r.ok) { $('#create-msg').textContent = body.error; return; }
  saveKey(body.name, body.key);
  dispatchEvent(new Event('streamlab:channels'));
  $('#create-msg').textContent = 'Canal criado! Guarde a stream key: ela não será mostrada de novo pelo servidor.';
  select(body.name, body.key);
};

refreshMine();
const first = Object.entries(loadKeys())[0];
if (first) select(...first);

// ================================================================ WHIP
// WHIP (WebRTC-HTTP Ingestion Protocol, RFC 9725): o "RTMP do WebRTC".
let pc = null, stream = null, session = null, statsTimer = null, last = { b: 0, t: 0 };

async function publish(kind) {
  if (!current) return;
  await stopPub();
  try {
    stream = kind === 'screen'
      ? await navigator.mediaDevices.getDisplayMedia({ video: { frameRate: 30 }, audio: true })
      : await navigator.mediaDevices.getUserMedia({ video: { width: 1280, height: 720, frameRate: 30 }, audio: true });
  } catch (e) { $('#pub-state').textContent = 'permissão negada: ' + e.message; return; }
  $('#preview').srcObject = stream;

  pc = new RTCPeerConnection();
  for (const track of stream.getTracks()) {
    const tr = pc.addTransceiver(track, { direction: 'sendonly', streams: [stream] });
    if (track.kind === 'video') {
      // prefere H.264: o MediaMTX consegue entregar H.264 também em HLS sem dor
      const caps = RTCRtpSender.getCapabilities('video')?.codecs || [];
      const h264 = caps.filter((c) => c.mimeType === 'video/H264');
      if (h264.length && tr.setCodecPreferences) tr.setCodecPreferences([...h264, ...caps.filter((c) => c.mimeType !== 'video/H264')]);
      const p = tr.sender.getParameters();
      p.encodings = [{ maxBitrate: 2_500_000 }];
      tr.sender.setParameters(p).catch(() => {});
    }
  }
  pc.onconnectionstatechange = () => ($('#pub-state').textContent = 'WebRTC: ' + pc.connectionState);
  await pc.setLocalDescription(await pc.createOffer());
  await new Promise((r) => { if (pc.iceGatheringState === 'complete') r(); pc.onicegatheringstatechange = () => pc.iceGatheringState === 'complete' && r(); setTimeout(r, 1000); });

  const url = `${cfg.webrtc}/${current.name}/whip`;
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/sdp', Authorization: `Bearer ${current.key}` },
    body: pc.localDescription.sdp,
  });
  if (!res.ok) { $('#pub-state').textContent = `WHIP recusado (${res.status}): stream key errada?`; await stopPub(); return; }
  session = res.headers.get('Location') ? new URL(res.headers.get('Location'), url).href : null;
  await pc.setRemoteDescription({ type: 'answer', sdp: await res.text() });
  $('#go-stop').disabled = false;
  statsTimer = setInterval(pubStats, 1000);
}

async function pubStats() {
  if (!pc) return;
  const s = await pc.getStats();
  let out = '', rtt = '', codecs = {};
  s.forEach((x) => { if (x.type === 'codec') codecs[x.id] = x.mimeType; });
  s.forEach((x) => {
    if (x.type === 'outbound-rtp' && x.kind === 'video') {
      const kbps = last.t ? ((x.bytesSent - last.b) * 8) / (x.timestamp - last.t) : 0;
      last = { b: x.bytesSent, t: x.timestamp };
      out += `codec: ${codecs[x.codecId] || '?'}<br>resolução: ${x.frameWidth}x${x.frameHeight}<br>fps: ${x.framesPerSecond ?? '-'}<br>bitrate: ${fmtKbps(kbps)}<br>`;
      out += `keyframes: ${x.keyFramesEncoded ?? '-'}<br>limitação: ${x.qualityLimitationReason}<br>NACKs recebidos: ${x.nackCount}<br>PLIs recebidos: ${x.pliCount}<br>`;
    }
    if (x.type === 'candidate-pair' && x.nominated && x.state === 'succeeded') rtt = `RTT: ${((x.currentRoundTripTime || 0) * 1000).toFixed(1)} ms<br>`;
  });
  $('#pub-stats').innerHTML = out + rtt || '—';
}

async function stopPub() {
  clearInterval(statsTimer);
  if (session) { fetch(session, { method: 'DELETE' }).catch(() => {}); session = null; }
  if (pc) { pc.close(); pc = null; }
  stream?.getTracks().forEach((t) => t.stop());
  stream = null;
  $('#preview').srcObject = null;
  $('#go-stop').disabled = true;
  $('#pub-state').textContent = 'parado';
  last = { b: 0, t: 0 };
}

$('#go-cam').onclick = () => publish('cam');
$('#go-screen').onclick = () => publish('screen');
$('#go-stop').onclick = stopPub;
