# Arquitetura do StreamLab

Este documento descreve cada componente, como eles conversam e o que acontece, passo a passo, desde o
momento em que o streamer aperta "Iniciar transmissão" até o quadro aparecer na tela de quem assiste.

## Visão geral

```mermaid
flowchart LR
  subgraph streamer["Streamer"]
    obs["OBS / ffmpeg"]
    nav["Navegador (Estúdio)"]
  end

  subgraph plataforma["Plataforma (docker compose)"]
    mtx["mediamtx<br/>ingest + servidor de mídia"]
    hub["hub (Go)<br/>auth · transcoder · UI · chat · QoE"]
    edge["edge (NGINX)<br/>origin :8082 + cache :8081"]
    vol[("volume hls<br/>.m3u8 / .m4s / thumb.jpg")]
  end

  subgraph obs_stack["Observabilidade"]
    prom["Prometheus"]
    graf["Grafana"]
    exp["nginx-exporter"]
  end

  viewer["Espectador<br/>hls.js / WebRTC"]

  obs -->|"RTMP :1935 / SRT :8890"| mtx
  nav -->|"WHIP :8889"| mtx
  mtx -->|"HTTP auth"| hub
  hub -->|"GET /v3/paths/list"| mtx
  mtx -->|"RTSP :8554"| hub
  hub -->|"escreve"| vol
  vol -->|"lê"| edge
  edge -->|"HLS ABR"| viewer
  mtx -->|"LL-HLS :8888"| viewer
  mtx -->|"WHEP :8889"| viewer
  hub <-->|"HTTP :8000 + WebSocket"| viewer
  prom -->|"scrape :9998"| mtx
  prom -->|"scrape :8000/metrics"| hub
  prom -->|"scrape :9113"| exp
  exp -->|"stub_status :8085"| edge
  graf --> prom
```

## Componentes

| Serviço | Responsabilidade | Entradas | Saídas |
|---|---|---|---|
| `mediamtx` | Recebe o sinal (RTMP, SRT, WHIP, RTSP) e o redistribui convertendo protocolo sem re-encode | encoder do streamer | RTSP para o transcoder, LL-HLS e WebRTC para os espectadores, API :9997, métricas :9998 |
| `hub` | Plano de controle: decide quem pode publicar, liga um FFmpeg por canal, serve a UI, o chat e recebe QoE | pedidos de auth do MediaMTX, navegadores | arquivos HLS no volume `hls`, `/metrics` |
| `edge` | Mini CDN: origin (arquivos) e edge (cache) no mesmo NGINX | volume `hls` (somente leitura) | HLS para os players, logs JSON com CMCD |
| `prometheus` | Coleta e guarda séries temporais, avalia alertas | MediaMTX, hub, nginx-exporter | consultas PromQL |
| `grafana` | Dashboard provisionado | Prometheus | painéis |
| `nginx-exporter` | Converte o `stub_status` do NGINX em métricas | edge :8085 | :9113 |
| `demo-streamer` | Simula o OBS (perfil `demo`) | — | RTMP para o MediaMTX |

## Fluxo 1: publicar (ingest + autenticação)

O MediaMTX não conhece stream keys. Ele usa a autenticação HTTP externa: a cada ação (publicar, ler, usar a API)
faz um `POST` para o hub com quem está pedindo e o quê. Resposta 2xx libera; qualquer outra coisa bloqueia.

```mermaid
sequenceDiagram
  autonumber
  participant OBS
  participant MTX as MediaMTX
  participant HUB as hub
  OBS->>MTX: RTMP connect app=live
  OBS->>MTX: publish stream "apolzek?key=live_abc"
  MTX->>HUB: POST /api/mediamtx/auth
  Note right of MTX: corpo JSON com action=publish,<br/>path=live/apolzek, protocol=rtmp,<br/>query=key=live_abc, ip, user, password, token
  HUB->>HUB: canal existe? key confere (tempo constante)?
  alt key válida
    HUB-->>MTX: 200 OK
    MTX-->>OBS: NetStream.Publish.Start
    OBS->>MTX: vídeo H.264 + áudio AAC
  else key inválida
    HUB-->>MTX: 401
    MTX-->>OBS: fecha a conexão
  end
```

Onde a key pode vir, por protocolo (implementado em `hub/api.go`, função `decide`):

| Protocolo | Formato | Campo que o hub lê |
|---|---|---|
| RTMP | `rtmp://host:1935/live` + chave `<canal>?key=<key>` | `query` |
| SRT | `streamid=publish:live/<canal>:<canal>:<key>` | `password` |
| WHIP | `POST /live/<canal>/whip` com `Authorization: Bearer <key>` | `token` |

Regras para as outras ações: `read`/`playback` são liberadas (assistir é público); `api`/`metrics`/`pprof` só a
partir de IPs privados (a rede interna do compose). Toda decisão vira a métrica
`streamlab_auth_requests_total{action,protocol,result}`.

## Fluxo 1b: recuperar, regenerar e excluir canal

O lab não tem login. As ações de dono do canal são protegidas pela senha do Estúdio (`STUDIO_PASSWORD`),
comparada em tempo constante, com 500 ms de espera a cada erro para frear tentativas de adivinhação.

```mermaid
sequenceDiagram
  participant U as Estúdio (navegador)
  participant H as hub
  participant M as MediaMTX
  U->>H: POST /api/channels/apolzek/key password
  H-->>U: 200 name, key (recuperar)
  U->>H: POST /api/channels/apolzek/key password, rotate=true
  H->>H: gera live_ nova e salva
  H-->>U: 200 nova key
  Note over H,M: a key antiga é recusada nas próximas conexões,<br/>quem já transmite continua até desconectar
  U->>H: DELETE /api/channels/apolzek password
  H->>H: apaga do store (reconexões passam a ser recusadas)
  H->>M: POST /v3/rtmpconns/kick/id (se estiver ao vivo)
  M-->>H: 200 e encerra a conexão do encoder
  H-->>U: 200 deleted
```

O canal `demo` não pode ser excluído porque é recriado a cada início do hub (desligue com `DEMO_CHANNEL=`).
As ações viram a métrica `streamlab_stream_key_requests_total{result="recovered|rotated|deleted|denied"}`.

## Fluxo 2: transcodificação (reconciliation loop)

O hub não recebe "eventos" do MediaMTX. Ele usa o padrão de **reconciliação** (o mesmo dos controllers do
Kubernetes): a cada 2 s compara o estado desejado (canais prontos no MediaMTX) com o estado atual (FFmpegs
rodando) e corrige a diferença. É simples e se recupera sozinho de qualquer falha.

```mermaid
flowchart TD
  tick["a cada 2 s"] --> list["GET /v3/paths/list no MediaMTX"]
  list --> filter["filtra paths live/&lt;canal&gt; com ready=true e trilha de vídeo"]
  filter --> diff{"compara com os jobs em execução"}
  diff -->|"canal novo"| start["cria job: goroutine supervise()"]
  diff -->|"canal sumiu ou readyTime mudou"| stop["cancela o job (SIGINT no FFmpeg)"]
  diff -->|"nada mudou"| tick
  start --> run["monta argumentos: BuildFFmpegArgs()"]
  run --> ff["executa ffmpeg, lê -progress pelo stdout"]
  ff -->|"saiu com o canal ainda ao vivo"| backoff["espera 1s, 2s, 4s... até 15s"]
  backoff --> run
  stop --> clean["após 15 s apaga /hls/&lt;canal&gt;"]
```

Detalhes importantes:

- **readyTime**: se o streamer reconecta, o MediaMTX gera um novo `readyTime`; o hub reinicia o FFmpeg porque
  codec, resolução ou trilhas podem ter mudado.
- **Fonte copiada ou re-codificada**: se o vídeo de entrada é H.264 e não veio de WebRTC, a qualidade `source`
  é copiada (`-c:v:0 copy`). Navegadores (WHIP) mandam keyframes só quando pedidos, então nesse caso a
  `source` também é re-codificada para manter segmentos de 2 s.
- **Velocidade**: o `speed=` do FFmpeg é uma média desde o início. O hub calcula a velocidade numa janela de
  ~5 s com `out_time_us` (segundos de mídia produzidos ÷ segundos de relógio) e publica em
  `streamlab_transcoder_speed_ratio`. Abaixo de 1.0 o transcoder está ficando para trás.

### O comando FFmpeg

```mermaid
flowchart LR
  in["entrada RTSP/TCP<br/>live/&lt;canal&gt;"] --> dec["decodifica vídeo"]
  dec --> split{"filtro split"}
  split --> s480["scale -2:480"] --> e480["libx264 1400k"]
  split --> s360["scale -2:360"] --> e360["libx264 700k"]
  split --> s160["scale -2:160"] --> e160["libx264 250k"]
  split --> th["fps=1/5, scale -2:180"] --> jpg["thumb.jpg"]
  in -->|"sem decodificar"| copy["source: copy"]
  in --> aud["áudio: AAC 48 kHz estéreo<br/>por rendition"]
  copy & e480 & e360 & e160 & aud --> hls["muxer HLS<br/>fMP4, 2 s, 8 segmentos na janela<br/>PROGRAM-DATE-TIME"]
  hls --> files["master.m3u8<br/>&lt;q&gt;/index.m3u8<br/>&lt;q&gt;/init_N.mp4<br/>&lt;q&gt;/seg_NNNNN.m4s"]
```

Todas as qualidades re-codificadas usam `-force_key_frames expr:gte(t,n_forced*2)` e `-sc_threshold 0`: um
keyframe exatamente a cada 2 s, alinhado entre as qualidades. É isso que permite ao player trocar de qualidade
na fronteira de qualquer segmento sem engasgar.

## Fluxo 3: entrega

Há três rotas, que o player escolhe pelo botão de protocolo:

```mermaid
flowchart LR
  mtx["MediaMTX"] -->|"RTSP"| ff["FFmpeg no hub"] --> vol[("volume hls")] --> origin["NGINX origin :8082"] --> edge["NGINX edge :8081<br/>proxy_cache"] -->|"HLS ABR<br/>~6 s"| p["player"]
  mtx -->|"LL-HLS<br/>~1-3 s"| p
  mtx -->|"WebRTC WHEP<br/>&lt; 1 s"| p
```

### O edge como CDN

```mermaid
sequenceDiagram
  participant A as Espectador A
  participant B as Espectador B
  participant E as edge :8081
  participant O as origin :8082
  A->>E: GET /hls/demo/480p/seg_00042.m4s
  E->>O: MISS: proxy_pass
  Note over E: proxy_cache_lock segura os demais pedidos<br/>do mesmo arquivo até o primeiro voltar
  B->>E: GET /hls/demo/480p/seg_00042.m4s
  O-->>E: 200 + Cache-Control max-age=60
  E-->>A: 200, X-Cache MISS
  E-->>B: 200, X-Cache HIT
```

| Arquivo | `Cache-Control` no origin | Por quê |
|---|---|---|
| `*.m3u8` | `max-age=1` | muda a cada segmento novo |
| `*.m4s`, `*.mp4` | `max-age=60` | imutável depois de escrito |
| `thumb.jpg` | `max-age=4` | atualizado a cada 5 s |

A chave de cache é `$scheme$host$uri`, **sem** a query string: o hls.js anexa `?CMCD=...` diferente em cada
requisição, e incluí-la na chave zeraria o cache.

## Fluxo 4: chat e audiência

```mermaid
sequenceDiagram
  participant V as Navegador
  participant H as hub
  V->>H: GET /ws/chat/demo?nick=ana (upgrade WebSocket)
  H-->>V: histórico (até 50 mensagens)
  H-->>V: type=viewers count=N (para todos da sala)
  V->>H: {"text": "oi"}
  H->>H: rate limit token bucket: 1/s, rajada 3, até 300 caracteres
  H-->>V: type=chat nick=ana color text ts (para todos da sala)
  Note over V,H: ping a cada 25 s mantém a conexão viva
```

O número de espectadores é o número de conexões WebSocket abertas na sala (`streamlab_viewers`). Cliente lento
não trava a sala: se o buffer de envio (64 mensagens) enche, a mensagem é descartada para aquele cliente.

## Fluxo 5: QoE

```mermaid
sequenceDiagram
  participant P as Player
  participant H as hub /api/qoe
  participant PR as Prometheus
  P->>P: video playing pela primeira vez
  P->>H: startup startup_ms
  loop a cada 5 s
    P->>H: heartbeat latency_s buffer_s bitrate_kbps dropped_frames interval_s
  end
  P->>H: rebuffer rebuffer_ms (ao sair de um travamento)
  P->>H: switch direction=up ou down
  P->>H: error kind=network, media, ice...
  H->>H: valida canal, modo, tipo (protege a cardinalidade)
  PR->>H: GET /metrics
```

Os beacons vão em lote (array JSON) a cada 5 s, e com `navigator.sendBeacon` quando a página fecha.

## Portas

| Porta | Protocolo | Serviço | Uso |
|---|---|---|---|
| 1935/tcp | RTMP | mediamtx | ingest (OBS) |
| 8890/udp | SRT | mediamtx | ingest e leitura SRT |
| 8888/tcp | HTTP | mediamtx | LL-HLS |
| 8889/tcp | HTTP | mediamtx | WHIP/WHEP (sinalização) |
| 8189/udp+tcp | ICE/SRTP | mediamtx | mídia WebRTC |
| 8554/tcp | RTSP | mediamtx | interno (transcoder) |
| 9997/tcp | HTTP | mediamtx | API (interna) |
| 9998/tcp | HTTP | mediamtx | métricas (interna) |
| 8000/tcp | HTTP/WS | hub | UI, API, chat, `/metrics` |
| 8081/tcp | HTTP | edge | HLS ABR com cache (`EDGE_PORT`) |
| 8082/tcp | HTTP | edge | origin (interna) |
| 9090/tcp | HTTP | prometheus | `PROMETHEUS_PORT` |
| 3000/tcp | HTTP | grafana | `GRAFANA_PORT` |

## Decisões de projeto

| Decisão | Alternativa | Por quê |
|---|---|---|
| MediaMTX como ingest | nginx-rtmp, SRS | um binário com RTMP, SRT, WebRTC, LL-HLS, RTSP, API e métricas |
| Transcoder dentro do hub | serviço separado / fila | menos peças para estudar; em produção seriam workers com GPU |
| Reconciliação por polling | webhooks `runOnReady` | auto-recuperável e sem estado perdido se o hub reiniciar |
| HLS com fMP4 (CMAF) | MPEG-TS | formato único para HLS e DASH, padrão atual |
| Segmentos de 2 s | 4–6 s | menor latência mantendo poucas requisições |
| Chat em memória | Redis/NATS | uma réplica só; ver [o README](../README.md#11-do-laboratório-para-a-produção) |
| JSON em volume para canais | banco | suficiente para um lab |
