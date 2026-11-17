# Documentação técnica do StreamLab

O [README principal](../README.md) mostra como subir e usar o projeto. Esta pasta explica **como as coisas
funcionam por dentro**: cada componente, cada protocolo, o formato dos arquivos e como observar tudo.

## Por onde começar

```mermaid
flowchart TD
  start["Comece aqui"] --> fund["Fundamentos de vídeo<br/>pixels, codecs, GOP, bitrate"]
  fund --> arq["Arquitetura do StreamLab<br/>componentes e fluxos"]
  arq --> ingest["Protocolos de ingest<br/>RTMP · SRT · WebRTC/WHIP · RTSP"]
  arq --> entrega["Protocolos de entrega<br/>HLS · LL-HLS · CMAF · DASH · MoQ"]
  ingest & entrega --> obs["Observabilidade<br/>QoS, QoE, métricas e alertas"]
```

## Índice

| Documento | Conteúdo |
|---|---|
| [Fundamentos de vídeo](fundamentos-video.md) | YUV, bitrate bruto, I/P/B frames, GOP, PTS/DTS, codecs, rate control, escada ABR, VMAF |
| [Arquitetura](arquitetura.md) | componentes, autenticação da stream key, reconciliation loop do transcoder, CDN, chat, QoE, portas |
| [Observabilidade](observabilidade.md) | QoS × QoE, catálogo de métricas, alertas, logs, cardinalidade |
| **Ingest** | |
| [RTMP e Enhanced RTMP](protocolos/rtmp.md) | handshake, chunk stream, mensagens AMF, tags FLV, E-RTMP |
| [SRT](protocolos/srt.md) | handshake, ARQ, TSBPD e latência, criptografia, streamid |
| [WebRTC, WHIP e WHEP](protocolos/webrtc-whip-whep.md) | SDP, ICE, DTLS-SRTP, RTP/RTCP, controle de congestionamento, WHIP/WHEP |
| [RTSP](protocolos/rtsp.md) | métodos, transporte RTP sobre TCP/UDP |
| **Entrega** | |
| [HLS](protocolos/hls.md) | playlists, tags, janela ao vivo, ABR, CDN, CMCD |
| [LL-HLS](protocolos/ll-hls.md) | partes, blocking reload, preload hints, delta updates |
| [CMAF e fMP4](protocolos/cmaf-fmp4.md) | caixas MP4, init e media segments, codecs string |
| [MPEG-DASH](protocolos/dash.md) | MPD, SegmentTemplate, LL-DASH |
| [MoQ (Media over QUIC)](protocolos/moq.md) | QUIC, tracks/groups/objects, relays, estado da padronização |

## Mapa dos protocolos no StreamLab

```mermaid
flowchart LR
  subgraph ingest["Ingest"]
    rtmp["RTMP :1935"]
    srt["SRT :8890/udp"]
    whip["WHIP :8889"]
  end
  mtx["MediaMTX"]
  rtsp["RTSP :8554<br/>(interno)"]
  ff["FFmpeg no hub"]
  subgraph entrega["Entrega"]
    hls["HLS + CMAF<br/>edge :8081"]
    ll["LL-HLS :8888"]
    whep["WHEP :8889"]
    moq["MoQ :8893<br/>(experimental, desligado)"]
  end
  rtmp & srt & whip --> mtx
  mtx --> rtsp --> ff --> hls
  mtx --> ll & whep
  mtx -.-> moq
```

Todos os desenhos desta documentação são [Mermaid](https://mermaid.js.org/) e são renderizados pelo GitHub.
