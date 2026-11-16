# StreamLab — uma "Twitch didática" para aprender live streaming

Uma plataforma de live streaming completa, pequena o bastante para ler numa tarde e
grande o bastante para mostrar **todas** as peças de um serviço como a Twitch:
ingest com stream key, transcodificação em várias qualidades (ABR), empacotamento HLS/CMAF,
CDN com cache, três formas de assistir (HLS, LL-HLS e WebRTC), chat em tempo real e
**observabilidade de ponta a ponta** — do encoder até o olho do espectador.

> Este projeto começou como um servidor NGINX-RTMP de 20 linhas gravado para o
> [canal no YouTube](https://youtu.be/CrEzeBwLZPU). Essa versão continua em [`classic/`](classic/)
> para comparação: é ótima para entender o básico, e o resto deste README mostra o que
> falta para chegar a algo parecido com uma plataforma de verdade.

```
 OBS / ffmpeg / navegador
        │  RTMP · SRT · WHIP
        ▼
 ┌─────────────┐  auth (stream key)   ┌──────────────────────────────────────────┐
 │  MediaMTX   │ ───────────────────► │ hub (Go)                                 │
 │  ingest +   │                      │  • valida stream key                     │
 │  servidor   │ ◄── RTSP (interno) ─ │  • FFmpeg: escada ABR → HLS/CMAF         │
 │  de mídia   │                      │  • UI web, chat (WebSocket)              │
 └─────┬───────┘                      │  • recebe QoE dos players → /metrics     │
       │ LL-HLS :8888                 └──────────────┬───────────────────────────┘
       │ WebRTC/WHEP :8889                           │ arquivos .m3u8/.m4s
       │                                             ▼
       │                              ┌──────────────────────────────┐
       │                              │ edge (NGINX): origin + cache │ :8081
       │                              └──────────────┬───────────────┘
       ▼                                             ▼
   ┌──────────────────────── navegador (hls.js / WebRTC) ──────────────────────┐
   │  player · stats para nerds · inspetor de playlist/SDP · chat · beacons QoE │
   └────────────────────────────────────────────────────────────────────────────┘
                 Prometheus :9090  ◄── scrape ── MediaMTX, hub, nginx-exporter
                 Grafana    :3000  ◄── dashboard "pipeline de live streaming"
```

---

## Sumário

1. [Subindo em 1 minuto](#1-subindo-em-1-minuto)
2. [O que tem dentro](#2-o-que-tem-dentro)
3. [Transmitindo](#3-transmitindo)
4. [Assistindo (e comparando protocolos)](#4-assistindo-e-comparando-protocolos)
5. [Laboratório: experimentos guiados](#5-laboratório-experimentos-guiados)
6. [Observabilidade de streaming](#6-observabilidade-de-streaming)
7. [Fundamentos: como o vídeo ao vivo funciona](#7-fundamentos-como-o-vídeo-ao-vivo-funciona)
8. [Protocolos em detalhe](#8-protocolos-em-detalhe)
9. [Streaming em 2027: o panorama](#9-streaming-em-2027-o-panorama)
10. [Ferramentas do ecossistema](#10-ferramentas-do-ecossistema)
11. [Do laboratório para a produção](#11-do-laboratório-para-a-produção)
12. [Estrutura do repositório e configuração](#12-estrutura-do-repositório-e-configuração)
13. [Problemas comuns](#13-problemas-comuns)
14. [Glossário](#14-glossário)

---

## 1. Subindo em 1 minuto

Requisitos: **Docker** com Compose v2. Opcional: [OBS Studio](https://obsproject.com/pt-br/download) e `ffmpeg`/`ffplay`.

```bash
# plataforma + um "streamer" de teste (barras coloridas + relógio + bip)
docker compose --profile demo up -d --build
```

| O quê | Onde |
|---|---|
| **Interface (home, player, chat, estúdio)** | http://localhost:8000 |
| Canal de demonstração | http://localhost:8000/watch/demo |
| Grafana (sem login) | http://localhost:3000 |
| Prometheus | http://localhost:9090 |

Sem o canal de demo: `docker compose up -d --build`. Para desligar tudo: `docker compose --profile demo down`
(adicione `-v` para apagar canais criados e o histórico de métricas).

> Alguma porta ocupada? `EDGE_PORT=18081 GRAFANA_PORT=13000 PROMETHEUS_PORT=19090 docker compose --profile demo up -d`

---

## 2. O que tem dentro

| Serviço | Imagem | Papel na "Twitch" | Portas |
|---|---|---|---|
| `mediamtx` | `bluenviron/mediamtx:1.21.4` | **Ingest** (RTMP/SRT/WHIP) e servidor de mídia: converte protocolos sem re-encode, entrega LL-HLS e WebRTC | 1935 RTMP · 8890/udp SRT · 8888 LL-HLS · 8889 WebRTC · 8189 ICE |
| `hub` | build de [`hub/`](hub/) (Go + FFmpeg) | **Plano de controle**: stream keys, liga um transcoder por canal ao vivo, UI, chat, coleta de QoE, `/metrics` | 8000 |
| `edge` | `nginx:1.29` | **CDN** em miniatura: origin + cache (`proxy_cache`, *request coalescing*, logs com CMCD) | 8081 |
| `prometheus` | `prom/prometheus:v3.7.3` | Coleta métricas de todas as peças + [alertas](observability/alerts.yml) | 9090 |
| `grafana` | `grafana/grafana:12.3.0` | Dashboard da pipeline já provisionado | 3000 |
| `nginx-exporter` | `nginx/nginx-prometheus-exporter:1.4.2` | Expõe o `stub_status` do edge em formato Prometheus | — |
| `demo-streamer` | build de [`demo/`](demo/) | Simula o OBS: publica 720p30 via RTMP com a stream key do canal `demo` | — |

### As páginas

- **Ao vivo** (`/`): canais no ar, com miniatura gerada pelo transcoder, protocolo de ingest, codec, resolução e bitrate.
- **Canal** (`/watch/<canal>`):
  - player com três modos: **HLS ABR via CDN**, **LL-HLS** e **WebRTC (WHEP)**;
  - **Stats para nerds**: qualidade, banda estimada, buffer, latência, quadros perdidos, RTT/jitter no WebRTC;
  - aba **Como o vídeo chega até você**: a pipeline com números ao vivo (bitrate de ingest, fps/velocidade do transcoder, cache hit ratio, latência);
  - aba **Requisições**: cada segmento baixado, tamanho, tempo, throughput e `X-Cache` HIT/MISS;
  - aba **Playlist / SDP**: o `.m3u8` ao vivo (ou o SDP do WebRTC) com explicação de cada tag;
  - aba **Assistir pelo terminal**: os mesmos fluxos com `curl`, `ffplay` e `ffprobe`;
  - **chat** via WebSocket com contagem de espectadores e rate limit.
- **Estúdio** (`/studio`): cria canal, mostra a stream key e a configuração para OBS (RTMP e WHIP), ffmpeg (RTMP e SRT) e **transmite direto do navegador** (câmera ou tela, via WHIP).
- **Aprenda** (`/learn`): resumo dos conceitos e experimentos.

---

## 3. Transmitindo

Crie um canal em http://localhost:8000/studio. Você recebe uma **stream key** (ex.: `live_3f9a…`).
Ela é o segredo que prova que você é dono do canal — nunca aparece nas URLs públicas.

### OBS via RTMP (o clássico)
*Configurações → Transmissão → Serviço: Personalizado*

| Campo | Valor |
|---|---|
| Servidor | `rtmp://localhost:1935/live` |
| Chave de transmissão | `<canal>?key=<stream key>` |

*Configurações → Saída (modo avançado)*: x264 ou NVENC/AMF/QSV, **CBR**, 3000–6000 Kbps,
**intervalo de quadro-chave = 2 s**, perfil `main`/`high`, predefinição `veryfast` (x264).

### OBS 30+ via WHIP (WebRTC)
*Serviço: WHIP* · Servidor `http://localhost:8889/live/<canal>/whip` · Bearer token = stream key.

### ffmpeg via SRT
```bash
ffmpeg -re -f lavfi -i testsrc2=size=1280x720:rate=30 -f lavfi -i sine \
  -c:v libx264 -preset veryfast -tune zerolatency -b:v 3000k -g 60 -keyint_min 60 \
  -c:a aac -b:a 128k -f mpegts \
  "srt://localhost:8890?streamid=publish:live/<canal>:<canal>:<stream key>&pkt_size=1316"
```

### Navegador
No Estúdio, clique em **Câmera** ou **Tela**. O navegador codifica (H.264 quando disponível) e
envia via WHIP. O painel ao lado mostra o que o encoder do navegador está fazendo: bitrate,
keyframes, `qualityLimitationReason` (CPU ou banda), NACKs e PLIs recebidos.

### O que acontece quando você aperta "Iniciar transmissão"

```mermaid
sequenceDiagram
    participant OBS
    participant MTX as MediaMTX
    participant HUB as hub
    participant FF as FFmpeg (no hub)
    participant EDGE as edge (NGINX)
    OBS->>MTX: RTMP connect + publish "demo?key=…"
    MTX->>HUB: POST /api/mediamtx/auth {action: publish, path: live/demo, query: key=…}
    HUB-->>MTX: 200 (key válida) ou 401
    OBS->>MTX: vídeo H.264 + áudio AAC (FLV sobre TCP)
    loop a cada 2s
        HUB->>MTX: GET /v3/paths/list (quem está ao vivo?)
    end
    HUB->>FF: inicia transcoder do canal
    FF->>MTX: RTSP read live/demo
    FF->>FF: decode → scale → encode 480p/360p/160p (+ source copiada)
    FF-->>EDGE: escreve master.m3u8, */index.m3u8, *.m4s (volume compartilhado)
    Note over EDGE: o player pede arquivos ao edge;<br/>o edge busca no origin só no MISS
```

O comando FFmpeg completo é montado em [`hub/transcoder.go`](hub/transcoder.go) (`BuildFFmpegArgs`), comentado bloco a bloco.

---

## 4. Assistindo (e comparando protocolos)

| Modo no player | Caminho | Latência típica na internet | Medido neste lab (localhost) | Escala |
|---|---|---|---|---|
| **HLS ABR (via CDN)** | MediaMTX → FFmpeg → arquivos → edge → hls.js | 6–15 s | ~6 s | enorme (é só HTTP + cache) |
| **LL-HLS** | MediaMTX (partes de 200 ms) → hls.js | 2–4 s | < 1–2 s | grande (HTTP, exige CDN compatível) |
| **WebRTC (WHEP)** | MediaMTX → RTP/SRTP sobre UDP | 0,2–1 s | < 0,5 s | cara (conexão com estado por espectador) |

Em localhost não há rede de verdade, então LL-HLS e WebRTC parecem quase iguais. O relógio
gravado no vídeo de demo e o relógio UTC da página permitem medir a latência **glass-to-glass**
a olho: tire um print com os dois na tela e subtraia.
A "latência" dos *Stats para nerds* vem do `#EXT-X-PROGRAM-DATE-TIME`, que o packager grava ao **escrever** o segmento:
ela não inclui o tempo de encode e ingest. A diferença entre os dois números é exatamente o pedaço da pipeline que o
player não enxerga — um bom exemplo de por que medir de ponta a ponta é difícil.

Fora do navegador:

```bash
curl -s  http://localhost:8081/hls/demo/master.m3u8                 # lista de qualidades
curl -s  http://localhost:8081/hls/demo/480p/index.m3u8             # janela de segmentos
curl -sI http://localhost:8081/hls/demo/source/init_0.mp4 | grep -i x-cache   # MISS, depois HIT
ffplay   http://localhost:8081/hls/demo/master.m3u8                 # HLS
ffplay   http://localhost:8888/live/demo/index.m3u8                 # LL-HLS
ffplay   "srt://localhost:8890?streamid=read:live/demo"             # SRT
ffprobe -hide_banner rtmp://localhost:1935/live/demo                # o sinal original
```

---

## 5. Laboratório: experimentos guiados

Cada experimento mostra um conceito com algo que você **vê** acontecer.

1. **Latência × protocolo.** Abra `/watch/demo`, alterne HLS → LL-HLS → WebRTC olhando o relógio do vídeo e o da página.
   Depois veja no Grafana o painel *Latência ao vivo (p50) por modo*. *Por que o HLS atrasa?* O hls.js fica
   `liveSyncDurationCount = 3` segmentos atrás da borda: 3 × 2 s = 6 s, por segurança contra travamentos.
2. **ABR em ação.** DevTools → Network → *Throttling* → "Slow 4G". Em *Stats para nerds*, a qualidade cai para
   360p/160p. Volte para "No throttling" e veja subir. No Grafana: *Bitrate assistido* e *trocas de qualidade*.
3. **Cache de CDN.** Aba *Requisições*: primeiro acesso a cada segmento é `MISS`. Abra o mesmo canal em outra aba:
   agora quase tudo é `HIT`. É por isso que 1 milhão de espectadores não derruba o origin.
4. **Leia o protocolo.** Aba *Playlist / SDP*. Em HLS, veja `#EXT-X-MEDIA-SEQUENCE` subir e `#EXT-X-PROGRAM-DATE-TIME`.
   Em LL-HLS, `#EXT-X-PART` e `#EXT-X-PRELOAD-HINT`. Em WebRTC, os `a=candidate` (ICE) e `a=fingerprint` (DTLS).
5. **Derrube o transcoder.** Suba a escada para algo pesado e veja a CPU perder:
   ```bash
   TRANSCODE_LADDER="1080p:1080:6000:160,720p:720:3500:128,480p:480:1400:128,360p:360:700:96,160p:160:250:64" \
     docker compose --profile demo up -d hub
   ```
   *Velocidade do transcoder* < 1.0x, o alerta `TranscoderAtrasado` dispara (Prometheus → Alerts) e o
   *Rebuffering ratio* sobe. É o problema nº 1 de quem roda transcodificação própria.
6. **Rede ruim no ingest: RTMP × SRT.** Em Linux, simule perda no loopback (lembre de remover!):
   ```bash
   sudo tc qdisc add dev lo root netem loss 3% delay 40ms
   # publique um canal por RTMP e outro por SRT e compare os players / painel "SRT: RTT e perda"
   sudo tc qdisc del dev lo root
   ```
   RTMP (TCP) sofre com *head-of-line blocking*; SRT retransmite só o que perdeu, dentro de uma janela de latência.
7. **Keyframes importam.** No OBS, coloque intervalo de quadro-chave em 10 s. Os segmentos da qualidade `source`
   ficam com 10 s (não dá para cortar sem keyframe) e o LL-HLS/ABR pioram. Volte para 2 s.
8. **CMCD.** `docker compose logs -f edge` mostra, em cada requisição, o campo `cmcd` enviado pelo player
   (`bl` = buffer, `br` = bitrate, `mtp` = throughput medido, `sid` = sessão…). O CDN passa a saber o que o player sente.
9. **Segurança da stream key.** Tente publicar com a chave errada e veja o `deny` no painel *Auth de stream key*
   e em `docker compose logs hub | grep auth`.
10. **Chat sob carga.** Mande mensagens rápido: o rate limit (1 msg/s, rajada de 3) responde, e
    `streamlab_chat_messages_rejected_total{reason="rate_limit"}` sobe.

---

## 6. Observabilidade de streaming

### QoS × QoE

- **QoS (Quality of Service)** — saúde do sistema: bitrate do ingest, CPU/velocidade do transcoder, cache hit ratio,
  erros 5xx no edge, perda de pacotes no SRT/WebRTC.
- **QoE (Quality of Experience)** — o que a pessoa sente: demorou para começar? travou? ficou borrado? está atrasado
  em relação ao chat?

O servidor pode estar 100% verde e o espectador travando (Wi-Fi ruim, CPU fraca, CDN distante). **Só o player sabe
a QoE.** Por isso o player do StreamLab envia *beacons* para `POST /api/qoe` e o hub os transforma em métricas.

### As métricas que importam (e onde elas estão aqui)

| Sinal | Pergunta | Métrica |
|---|---|---|
| **Video Start Time** | quanto tempo do clique ao primeiro quadro? | `streamlab_player_startup_seconds` (histograma) |
| **Rebuffering ratio** | que % do tempo assistido ficou travado? | `rate(streamlab_player_rebuffer_seconds_total) / rate(streamlab_player_watch_seconds_total)` |
| **Latência ao vivo** | quão atrás do "agora" está o espectador? | `streamlab_player_latency_seconds` por modo |
| **Bitrate entregue** | qual qualidade a pessoa está vendo? | `streamlab_player_bitrate_kbps`, `..._quality_switches_total` |
| **Erros de reprodução** | quantas sessões falham? | `streamlab_player_errors_total{kind}` |
| **Saúde do ingest** | o sinal está chegando estável? | `rate(paths_bytes_received[30s])`, `srt_conns_ms_rtt`, `srt_conns_packets_received_loss_rate` |
| **Saúde do transcoder** | dá conta em tempo real? | `streamlab_transcoder_speed_ratio`, `..._fps`, `..._restarts_total` |
| **Entrega** | o edge aguenta? | `nginx_http_requests_total`, `X-Cache` nos logs |
| **Audiência** | quantos estão assistindo? | `streamlab_viewers`, `streamlab_player_sessions{mode}` |

PromQL úteis:

```promql
# p95 do tempo de início por protocolo
histogram_quantile(0.95, sum by (le, mode) (rate(streamlab_player_startup_seconds_bucket[5m])))

# rebuffering ratio por canal
sum by (channel) (rate(streamlab_player_rebuffer_seconds_total[5m]))
  / sum by (channel) (rate(streamlab_player_watch_seconds_total[5m]))

# bitrate de ingest de cada canal, em Mbps
rate(paths_bytes_received{name=~"live/.*"}[30s]) * 8 / 1e6
```

### Dashboard

O dashboard **StreamLab — pipeline de live streaming** é lido de cima para baixo na mesma ordem em que o vídeo
percorre o sistema: **1 Ingest → 2 Transcoding → 3 Entrega → 4 Audiência e QoE**. Ele é gerado por
[`observability/grafana/gen_dashboard.py`](observability/grafana/gen_dashboard.py) (edite o script, não o JSON).

### Alertas ([`observability/alerts.yml`](observability/alerts.yml))

| Alerta | Por que importa |
|---|---|
| `TranscoderAtrasado` | velocidade < 0.95x: segmentos atrasam e todo mundo trava |
| `TranscoderReiniciando` | FFmpeg caindo em loop (sinal corrompido, codec não suportado) |
| `IngestSemDados` | canal "ao vivo" mas sem quadros sendo processados |
| `RebufferingAlto` | > 2% do tempo travado: a métrica de QoE mais correlacionada com abandono |
| `LatenciaAltaHLS` | latência mediana > 15 s: o chat fica dessincronizado do vídeo |

### Boas práticas que este lab ensina

- **Cardinalidade**: labels vindas do cliente (canal, modo, tipo de erro) são **validadas** antes de virar label
  ([`hub/qoe.go`](hub/qoe.go)). Nunca use `session_id` ou IP como label.
- **Histogramas, não médias**: latência e startup têm cauda longa; olhe p50 e p95.
- **Correlação**: CMCD (`sid`) liga o log do CDN à sessão do player; `PROGRAM-DATE-TIME` liga o segmento ao relógio real.
- **Logs estruturados**: hub e edge logam JSON (`docker compose logs hub edge`), prontos para Loki/Elasticsearch.
- **Próximo passo natural**: OpenTelemetry no hub (traces do auth/transcoder) e RUM no player, enviando para um
  Collector — os mesmos sinais, com contexto de trace.

---

## 7. Fundamentos: como o vídeo ao vivo funciona

### Do sensor ao arquivo

Um vídeo 1080p60 cru tem 1920 × 1080 × 1,5 bytes (YUV 4:2:0) × 60 ≈ **1,5 Gbit/s**. Um encoder H.264 leva isso para
**~6 Mbit/s** — 250× menor — explorando redundância:

- **I-frame (keyframe)**: imagem completa, decodificável sozinha. Grande.
- **P-frame**: só a diferença em relação a quadros anteriores. **B-frame**: usa quadros anteriores *e* futuros.
- **GOP (Group of Pictures)**: a distância entre keyframes. Em live, **2 s** é o padrão: um player só pode começar,
  e um segmento só pode ser cortado, num keyframe.
- **Bitrate**: CBR (constante) é o preferido para ingest ao vivo — previsível para a rede. VBR/CRF é melhor para VOD.
- **Profile/level** (`main`, `high`, `4.1`): o "subconjunto" do codec que o decoder precisa suportar.

### Codec × container × protocolo

Três coisas que todo mundo confunde:

| Camada | O que é | Exemplos |
|---|---|---|
| **Codec** | como comprimir a imagem/som | H.264/AVC, H.265/HEVC, AV1, VP9, VVC · AAC, Opus |
| **Container** | como embalar trilhas + timestamps num arquivo/fluxo | FLV, MPEG-TS, MP4/fMP4 (CMAF), WebM, Matroska |
| **Protocolo** | como transportar pela rede | RTMP, SRT, RTSP, WebRTC, HLS, DASH, MoQ |

Ex.: o OBS manda **H.264 + AAC** em **FLV** sobre **RTMP**. O StreamLab entrega o mesmo H.264 em **fMP4 (CMAF)** sobre
**HLS**, e em **RTP** sobre **WebRTC** — sem re-encode na qualidade `source`.

### A escada ABR (bitrate ladder)

O transcoder produz várias versões do mesmo vídeo, com **keyframes alinhados** (o StreamLab força um a cada 2 s em
todas), para o player poder trocar de qualidade na fronteira de qualquer segmento:

| Rendition | Resolução | Vídeo | Áudio |
|---|---|---|---|
| source | a do streamer (copiada, sem re-encode) | a do streamer | 160k |
| 480p | 854×480 | 1400k | 128k |
| 360p | 640×360 | 700k | 96k |
| 160p | 284×160 | 250k | 64k |

O player (hls.js) mede o throughput de cada download e estima a banda (média móvel exponencial); outros algoritmos
olham o **buffer** (BOLA) ou combinam os dois. Em 2027 a tendência é **per-title / content-aware encoding**
(escada diferente por conteúdo: um jogo rápido precisa de mais bits que um podcast) e escadas geradas por modelos.

### Orçamento de latência (HLS de 2 s, valores típicos)

| Etapa | Tempo |
|---|---|
| captura + encode no OBS | 0,1–0,5 s |
| ingest (rede do streamer) | 0,1–0,5 s |
| transcode | 0,2–1 s |
| esperar o segmento fechar | 2 s |
| CDN + download | 0,1–0,5 s |
| buffer do player (3 segmentos) | 6 s |
| **total** | **~9–10 s** |

As técnicas de baixa latência atacam as duas maiores linhas: **segmentos menores/partes** (LL-HLS, LL-DASH, CMAF chunked)
e **buffer menor** (que só funciona se a rede for estável — trade-off direto com rebuffering).

### CDN

Como HLS/DASH são arquivos HTTP, qualquer CDN serve. Conceitos que o [`edge/nginx.conf`](edge/nginx.conf) mostra:
**origin × edge**, `Cache-Control` diferente para playlist (1 s) e segmento (imutável), **request coalescing**
(`proxy_cache_lock`: mil pedidos simultâneos do mesmo segmento viram um ao origin), chave de cache sem query string
(o CMCD muda a cada requisição), cache de 404 curto (evita "tempestade" pedindo segmentos futuros).
Em produção ainda entram **origin shield**, multi-CDN e **content steering** (o HLS/DASH dizem ao player qual CDN usar).

---

## 8. Protocolos em detalhe

### Ingest (streamer → plataforma)

- **RTMP** (Adobe, ~2002). TCP, porta 1935, container FLV. Simples e universal, por isso ainda domina o ingest. Limites:
  TCP sofre com perda de pacotes (head-of-line blocking), o FLV original só levava H.264/AAC, sem criptografia nativa (RTMPS = TLS).
- **Enhanced RTMP (E-RTMP)**. Extensão do Veovera Software Organization (com Adobe, YouTube, Twitch, OBS, FFmpeg) que
  adiciona **HEVC, AV1, VP9, Opus**, **multitrack** (várias qualidades/idiomas em uma conexão) e reconexão. É a base do
  *Enhanced Broadcasting* da Twitch e do ingest HEVC/AV1 do YouTube.
- **SRT** (Haivision, open source, 2017). UDP + retransmissão seletiva (ARQ) + criptografia AES, com latência
  configurável (`latency` ≈ 2,5–4× o RTT). Ótimo para redes ruins (4G, internet pública). Padrão em broadcast/contribuição.
- **WHIP** (WebRTC-HTTP Ingestion Protocol, **RFC 9725**). Um `POST` HTTP com a oferta SDP; a resposta é a answer.
  Latência sub-segundo e permite transmitir **do navegador** sem plugin. Suportado pelo OBS desde a versão 30.
- **RTSP**. O protocolo das câmeras IP. Aqui é usado internamente pelo transcoder.
- **RIST**. Alternativa ao SRT vinda do broadcast (VSF), interoperável entre fabricantes.

### Entrega (plataforma → espectador)

- **HLS** (Apple, RFC 8216). Playlists `.m3u8` + segmentos (`.ts` ou `.m4s`). Funciona em qualquer CDN e em
  todo dispositivo. Latência alta por padrão.
- **LL-HLS** (Apple, 2019/2020). Segmentos divididos em **partes** (200 ms), playlists com **blocking reload**
  (`_HLS_msn`/`_HLS_part`: o servidor segura a resposta até a parte existir), **preload hints** e **delta updates**. 2–4 s.
- **MPEG-DASH** (ISO). Equivalente aberto do HLS, manifesto `.mpd`. **LL-DASH** usa CMAF *chunked transfer*.
- **CMAF** (Common Media Application Format). Um único formato de segmento fMP4 que serve HLS e DASH — empacota uma vez,
  entrega para os dois. O StreamLab gera HLS com segmentos CMAF.
- **WebRTC**. RTP/SRTP sobre UDP, com ICE (atravessar NAT: STUN/TURN), DTLS (chaves), controle de congestionamento
  (GCC/transport-cc), NACK e PLI. Latência < 1 s; escalar exige SFUs/cascatas (cada espectador é uma sessão com estado).
  **WHEP** é o equivalente do WHIP para assistir.
- **MoQ (Media over QUIC)**. Grupo de trabalho do IETF desde 2022. Publish/subscribe sobre QUIC/WebTransport com
  **relays** cacheáveis como um CDN — promete latência de WebRTC com escala de HLS. O MediaMTX 1.21 já tem suporte
  experimental (`moq: yes` em [`mediamtx/mediamtx.yml`](mediamtx/mediamtx.yml)).

---

## 9. Streaming em 2027: o panorama

> Esta seção resume o estado da arte no fim de 2026 e as tendências que devem marcar 2027. Datas e adoção de padrões
> mudam rápido; confira as fontes de cada projeto antes de decidir arquitetura.

### Protocolos: o fim da "era só RTMP + HLS"

- **Ingest se diversifica.** RTMP continua universal, mas em 2027 o padrão de fato para novos produtos é: **E-RTMP**
  (compatibilidade + HEVC/AV1 + multitrack), **SRT** para redes ruins e produção remota, e **WHIP** para tudo que é
  web/interativo. Com o WHIP padronizado (RFC 9725) e nativo no OBS, transmitir do navegador deixou de ser gambiarra.
- **Multitrack / transcodificação no cliente.** Em vez de a plataforma transcodificar tudo, o encoder do streamer
  (GPU) envia várias qualidades prontas (Twitch *Enhanced Broadcasting* com OBS + NVENC/AMF/QSV). Menos custo de
  servidor, melhor qualidade por bit, mas a plataforma perde controle e precisa de **observabilidade do lado do streamer**.
- **Latência como produto.** O mercado se divide em faixas: *broadcast* (HLS/DASH, 10–30 s, barato), *interativo*
  (LL-HLS/LL-DASH, 2–5 s, padrão de plataformas UGC) e *tempo real* (WebRTC/MoQ, < 1 s: leilões, apostas, co-stream,
  watch parties). **MoQ** é a aposta para unir a última faixa com a escala da primeira — espere os primeiros
  deployments em produção, CDNs oferecendo relays MoQ e players experimentais, mas não a substituição do HLS.
- **HLS/DASH evoluem por extensões**: *content steering* (multi-CDN dirigido pelo servidor), *interstitials*/SSAI
  (anúncios inseridos no servidor), *CMCD v2* e *CMSD* (servidor → player) para fechar o ciclo player ↔ CDN.

### Codecs

- **H.264** segue como piso de compatibilidade. **HEVC** volta ao live via E-RTMP e decodificação em hardware universal.
- **AV1** vira o codec "premium" do live: encode em hardware nas GPUs de consumo atuais (NVIDIA, AMD, Intel) e decode
  em praticamente todo dispositivo novo. Para a mesma qualidade, ~30–50% menos bits que H.264 — direto na conta de egress.
- **VVC/H.266** e **LCEVC** (camada de melhoria sobre um codec base) avançam em broadcast; **AV2** está no horizonte.
- Áudio: **Opus** no WebRTC/MoQ, AAC no HLS; áudio espacial e várias trilhas de idioma via multitrack.

### IA no pipeline

- **Legendas e tradução ao vivo** (modelos de fala em tempo real) e **dublagem sintética** multi-idioma.
- **Encoding guiado por percepção**: escadas por título/cena, métricas como **VMAF** em tempo real, super-resolução no cliente.
- **Clipes e highlights automáticos**, sumarização de lives longas, busca dentro do vídeo.
- **Moderação** de chat e de vídeo com LLMs/modelos de visão em tempo real — e agentes ajudando streamers.
- **Observabilidade com IA**: detecção de anomalias em QoE e correlação automática entre ingest, CDN e player.

### Plataforma e operação

- **Egress é o custo dominante**: multi-CDN, codecs mais eficientes e **P2P assistido** (WebRTC entre espectadores)
  são as alavancas.
- **Navegador como estúdio**: WebCodecs + WebTransport + WebGPU permitem encoder, compositor e player customizados
  sem plugin.
- **Observabilidade padronizada**: OpenTelemetry no backend, RUM nos players, eventos de QoE no formato **CTA-2066**,
  CMCD ligando player e CDN.
- **Segurança**: stream keys rotacionáveis e tokens assinados (JWT) por sessão, URLs assinadas no CDN, DRM com CMAF
  `cbcs` (Widevine/FairPlay/PlayReady) para conteúdo premium e *watermarking* forense contra pirataria de eventos ao vivo.

---

## 10. Ferramentas do ecossistema

| Categoria | Ferramentas |
|---|---|
| **Encoders / produção** | OBS Studio, FFmpeg, GStreamer, vMix, Wirecast, Streamlabs, encoders de hardware (NVENC, AMF, Quick Sync, Apple VideoToolbox) |
| **Servidores de mídia** | **MediaMTX** (usado aqui), SRS, OvenMediaEngine, nginx-rtmp (versão `classic/`), Wowza, Red5, Ant Media |
| **WebRTC / SFU** | LiveKit, Janus, mediasoup, Pion (Go), Jitsi, Galène, Cloudflare Calls/Realtime |
| **Transcode / packaging** | FFmpeg, GStreamer, Shaka Packager, Bento4, GPAC/MP4Box, AWS Elemental, encoders AV1 (SVT-AV1, rav1e) |
| **MoQ** | moq-rs / moq.dev, Cloudflare MoQ relay, MediaMTX (experimental), implementações do IETF MOQT |
| **Players** | hls.js (usado aqui), Shaka Player, dash.js, Video.js, Media Chrome, ExoPlayer/Media3 (Android), AVPlayer (Apple), mpv/VLC/ffplay |
| **Plataformas gerenciadas** | AWS IVS, Mux, Cloudflare Stream, Dolby OptiView (Millicast/THEO), Agora, api.video |
| **CDN** | Akamai, Cloudflare, Fastly, CloudFront, Google Media CDN, Varnish/NGINX próprios |
| **Observabilidade** | Prometheus + Grafana (usado aqui), OpenTelemetry, Loki, Mux Data, NPAW/Youbora, Conviva, Bitmovin Analytics |
| **Qualidade / testes** | VMAF, `ffprobe`, Apple `mediastreamvalidator`, DASH-IF Conformance, `tc netem`, Chrome `webrtc-internals` |

---

## 11. Do laboratório para a produção

O StreamLab corta caminho de propósito. O que uma plataforma real precisa a mais:

| Área | No StreamLab | Em produção |
|---|---|---|
| Contas | qualquer um cria canal | login (OIDC), canal ligado à conta, rotação de stream key |
| Auth do ingest | key no query/password/Bearer | idem + rate limit de tentativas, allowlist, JWT com expiração |
| Transcoder | 1 FFmpeg por canal no hub | fila + workers com GPU, autoscaling, isolamento por canal, transcode no cliente (multitrack) |
| Origin/CDN | 1 NGINX | packager redundante, origin shield, multi-CDN, URLs assinadas |
| WebRTC | 1 MediaMTX, `127.0.0.1` | SFUs em cascata, TURN, IPs públicos, HTTPS obrigatório |
| Chat | memória de 1 processo | pub/sub (Redis, NATS, Kafka), moderação, emotes, persistência |
| Gravação / VOD | não tem | `record` no MediaMTX ou CMAF → objeto (S3) → VOD + clipes |
| Estado | JSON em volume | banco (Postgres), cache |
| Observabilidade | Prometheus + Grafana | + logs centralizados, traces (OTel), RUM, SLOs e on-call |
| Segurança | HTTP, CORS `*` | TLS em tudo, CORS restrito, DRM/watermark se precisar |

---

## 12. Estrutura do repositório e configuração

```
.
├── docker-compose.yml          # a plataforma inteira
├── mediamtx/mediamtx.yml       # ingest + servidor de mídia (comentado)
├── hub/                        # plano de controle em Go
│   ├── main.go                 #   rotas e configuração
│   ├── api.go                  #   auth da stream key, /api/live, /api/channels, /api/config
│   ├── transcoder.go           #   reconciliation loop + comando FFmpeg da escada ABR
│   ├── chat.go                 #   chat WebSocket com rate limit
│   ├── qoe.go                  #   beacons dos players → métricas
│   ├── metrics.go              #   todas as métricas streamlab_*
│   ├── store.go                #   canais e stream keys (JSON)
│   └── web/                    #   UI (HTML/CSS/JS puro + hls.js)
├── edge/nginx.conf             # origin + edge com cache (mini CDN)
├── observability/              # Prometheus, alertas, Grafana (datasource + dashboard)
├── demo/                       # streamer de teste (ffmpeg)
└── classic/                    # a versão original com nginx-rtmp
```

Variáveis de ambiente (use um `.env` ao lado do compose):

| Variável | Padrão | Para quê |
|---|---|---|
| `DEMO_KEY` | `demo-key-troque-me` | stream key do canal `demo` |
| `TRANSCODE_LADDER` | `480p:480:1400:128,360p:360:700:96,160p:160:250:64` | escada ABR (`nome:altura:kbpsVídeo:kbpsÁudio`); a `source` sempre existe |
| `WEBRTC_HOST` | `127.0.0.1` | IP anunciado no WebRTC (use o IP da máquina para assistir de outro dispositivo) |
| `EDGE_PORT` / `GRAFANA_PORT` / `PROMETHEUS_PORT` | `8081` / `3000` / `9090` | portas no host |

API do hub:

| Rota | Descrição |
|---|---|
| `GET /api/live` | canais ao vivo com ingest, codec, resolução, bitrate, espectadores, progresso do transcoder |
| `GET /api/channels/{nome}` | um canal (sem a stream key) |
| `POST /api/channels` `{"name","title"}` | cria canal e devolve a stream key (única vez) |
| `POST /api/mediamtx/auth` | chamado pelo MediaMTX a cada publish/read |
| `POST /api/qoe` | beacons dos players (array JSON) |
| `GET /ws/chat/{nome}?nick=` | chat (WebSocket) |
| `GET /metrics` | métricas Prometheus |

Testes do hub: `cd hub && go test ./...` (também rodam no `docker build`).

---

## 13. Problemas comuns

- **`port is already allocated`**: mude as portas com `EDGE_PORT`, `GRAFANA_PORT`, `PROMETHEUS_PORT`.
- **WebRTC não conecta de outro dispositivo**: `WEBRTC_HOST=<ip-da-sua-máquina> docker compose up -d mediamtx` e libere 8189/udp e 8189/tcp no firewall. Câmera/tela no navegador só funcionam em `localhost` ou HTTPS.
- **Canal aparece mas o HLS ABR dá 404**: o transcoder leva ~4–6 s para gerar os primeiros segmentos. Veja `docker compose logs -f hub`.
- **Transcoder lento / player travando**: reduza `TRANSCODE_LADDER`; veja *Velocidade do transcoder* no Grafana.
- **OBS conecta e cai**: confira a chave no formato `<canal>?key=<stream key>` e o log `docker compose logs hub | grep auth`.
- **Áudio não toca**: navegadores bloqueiam autoplay com som; clique em **Ativar som**.

---

## 14. Glossário

**ABR** — Adaptive Bitrate: o player troca de qualidade conforme a rede. ·
**CMAF** — formato de segmento fMP4 comum a HLS e DASH. ·
**CMCD** — Common Media Client Data (CTA-5004): o player envia seu estado ao CDN. ·
**CBR/VBR** — bitrate constante/variável. ·
**Edge/Origin** — servidor de cache perto do usuário / fonte da verdade. ·
**GOP** — Group of Pictures, distância entre keyframes. ·
**Glass-to-glass** — latência total da câmera até a tela. ·
**ICE/STUN/TURN** — mecanismos do WebRTC para atravessar NAT. ·
**Ingest** — a entrada do sinal do streamer na plataforma. ·
**Keyframe (I-frame)** — quadro completo, ponto de entrada do vídeo. ·
**Ladder** — escada de qualidades geradas pelo transcoder. ·
**PLI/NACK** — pedidos RTCP de keyframe / de retransmissão. ·
**QoE/QoS** — qualidade percebida / qualidade do serviço. ·
**Rebuffering** — travamento por falta de dados no buffer. ·
**Rendition** — uma das qualidades geradas. ·
**SDP** — descrição de sessão negociada no WebRTC. ·
**SFU** — servidor que repassa streams WebRTC sem decodificar. ·
**SSAI** — inserção de anúncios no servidor. ·
**Stream key** — segredo que autoriza a publicação em um canal. ·
**VMAF** — métrica de qualidade perceptual da Netflix. ·
**WHIP/WHEP** — publicar/assistir WebRTC com um único POST HTTP.

---

Feito para aprender. Abra issues e PRs com experimentos novos!
