import json, sys
# Gera dashboards/streamlab.json:  python3 observability/grafana/gen_dashboard.py observability/grafana/dashboards/streamlab.json
panels=[]; y=0; pid=1
DS={"type":"prometheus","uid":"prometheus"}
def row(title):
    global y,pid
    panels.append({"type":"row","title":title,"id":pid,"collapsed":False,"gridPos":{"x":0,"y":y,"w":24,"h":1},"panels":[]}); pid+=1; y+=1
def panel(kind,title,targets,x,w,h=7,unit=None,desc="",extra=None):
    global pid
    p={"type":kind,"title":title,"id":pid,"datasource":DS,"description":desc,
       "gridPos":{"x":x,"y":y,"w":w,"h":h},
       "targets":[{"refId":chr(65+i),"datasource":DS,"expr":e,"legendFormat":l} for i,(e,l) in enumerate(targets)],
       "fieldConfig":{"defaults":{"unit":unit or "short"},"overrides":[]},
       "options":{}}
    if kind=="timeseries":
        p["fieldConfig"]["defaults"]["custom"]={"lineWidth":2,"fillOpacity":10,"showPoints":"never"}
        p["options"]={"legend":{"displayMode":"list","placement":"bottom"},"tooltip":{"mode":"multi"}}
    if kind=="stat":
        p["options"]={"reduceOptions":{"calcs":["lastNotNull"]},"colorMode":"value","graphMode":"area","textMode":"value_and_name"}
    if extra: 
        for k,v in extra.items(): p["fieldConfig"]["defaults"][k]=v
    pid+=1; panels.append(p)
def nexty(h=7):
    global y; y+=h

row("1 · Ingest — o sinal chegando do streamer (MediaMTX)")
panel("stat","Canais ao vivo",[("count(streamlab_channel_live == 1) or vector(0)","ao vivo")],0,4,
      desc="Paths live/<canal> prontos no MediaMTX.")
panel("stat","Publicadores por protocolo",[("sum(rtmp_conns{state=\"publish\"}) or vector(0)","RTMP"),("sum(srt_conns{state=\"publish\"}) or vector(0)","SRT"),("sum(webrtc_sessions{state=\"publish\"}) or vector(0)","WHIP")],4,8,
      desc="Conexões de ingest abertas por protocolo.")
panel("timeseries","Bitrate de ingest por canal",[("rate(paths_bytes_received{name=~\"live/.*\"}[30s]) * 8","{{name}}")],12,12,unit="bps",
      desc="Quanto o encoder (OBS) está enviando. Se oscila muito, a conexão de upload do streamer está ruim.")
nexty()
panel("timeseries","SRT: RTT e perda de pacotes",[("srt_conns_ms_rtt","RTT ms {{path}}"),("srt_conns_packets_received_loss_rate","perda % {{path}}")],0,12,
      desc="Só aparece com ingest SRT. SRT retransmite pacotes perdidos (ARQ) dentro da janela de latência configurada.")
panel("timeseries","Auth de stream key (MediaMTX → hub)",[("sum by (action,result) (rate(streamlab_auth_requests_total[1m]))","{{action}} {{result}}")],12,12,unit="reqps",
      desc="Cada publish/leitura pergunta ao hub. 'publish deny' alto = alguém tentando chaves erradas.")
nexty()

row("2 · Transcoding — a escada ABR (FFmpeg no hub)")
panel("timeseries","Velocidade do transcoder (1.0 = tempo real)",[("streamlab_transcoder_speed_ratio","{{channel}}")],0,8,unit="x",
      desc="Abaixo de 1.0 o transcoder está ficando para trás: a CPU não dá conta. É o alerta mais importante do pipeline.",
      extra={"thresholds":{"mode":"absolute","steps":[{"color":"red","value":None},{"color":"green","value":0.97}]},"custom":{"lineWidth":2,"fillOpacity":10,"showPoints":"never","thresholdsStyle":{"mode":"line"}}})
panel("timeseries","FPS processados",[("streamlab_transcoder_fps","{{channel}}")],8,8,
      desc="Deveria acompanhar o FPS da fonte (30 no demo).")
panel("stat","Renditions / Restarts (1h)",[("sum(streamlab_transcoder_renditions)","renditions"),("round(sum(increase(streamlab_transcoder_restarts_total[1h]))) or vector(0)","restarts")],16,8)
nexty()

row("3 · Entrega — edge/CDN, LL-HLS e WebRTC")
panel("timeseries","Requisições no edge (NGINX)",[("rate(nginx_http_requests_total[30s])","req/s")],0,8,unit="reqps",
      desc="Cada espectador HLS pede 1 playlist + 1 segmento a cada ~2s. Abra o DevTools e veja o header X-Cache (HIT/MISS).")
panel("timeseries","Saída do MediaMTX por canal",[("rate(paths_bytes_sent{name=~\"live/.*\"}[30s]) * 8","{{name}}")],8,8,unit="bps",
      desc="Inclui o transcoder (RTSP), LL-HLS e WebRTC. HLS ABR sai pelo edge, não por aqui.")
panel("timeseries","Leitores por tipo",[("sum by (readerType) (paths_readers{name=~\"live/.*\",readerType!=\"\"})","{{readerType}}"),("sum(webrtc_sessions{state=\"read\"}) or vector(0)","sessões WebRTC")],16,8,
      desc="Quem está lendo direto do servidor de mídia.")
nexty()

row("4 · Audiência e QoE — o que o espectador sente (beacons do player)")
panel("stat","Espectadores",[("sum(streamlab_viewers) or vector(0)","total")],0,4,desc="Conexões WebSocket abertas nas páginas dos canais.")
panel("timeseries","Sessões de player por modo",[("sum by (mode) (streamlab_player_sessions)","{{mode}}")],4,10,
      desc="hls = ABR via edge · llhls = Low-Latency HLS · webrtc = WHEP")
panel("timeseries","Latência ao vivo (p50) por modo",[("histogram_quantile(0.5, sum by (le,mode) (rate(streamlab_player_latency_seconds_bucket[1m])))","{{mode}}")],14,10,unit="s",
      desc="Distância entre o que o player mostra e o 'agora'. HLS ~6-10s, LL-HLS ~2-3s, WebRTC < 1s.")
nexty()
panel("timeseries","Tempo de início (p50 / p95)",[("histogram_quantile(0.5, sum by (le,mode) (rate(streamlab_player_startup_seconds_bucket[5m])))","p50 {{mode}}"),("histogram_quantile(0.95, sum by (le,mode) (rate(streamlab_player_startup_seconds_bucket[5m])))","p95 {{mode}}")],0,8,unit="s",
      desc="Video Start Time: do clique ao primeiro quadro. Cada segundo a mais perde audiência.")
panel("timeseries","Rebuffering ratio",[("sum by (channel,mode) (rate(streamlab_player_rebuffer_seconds_total[2m])) / sum by (channel,mode) (rate(streamlab_player_watch_seconds_total[2m]))","{{channel}} {{mode}}")],8,8,unit="percentunit",
      desc="Fração do tempo assistido em que o vídeo ficou travado. A métrica de QoE mais importante. Meta: < 1%.")
panel("timeseries","Bitrate assistido (p50) e trocas de qualidade",[("histogram_quantile(0.5, sum by (le,mode) (rate(streamlab_player_bitrate_kbps_bucket[2m])))","p50 kbps {{mode}}"),("sum by (direction) (rate(streamlab_player_quality_switches_total[2m])) * 60","trocas/min {{direction}}")],16,8,
      desc="O ABR escolhendo a qualidade. Muitas trocas = oscilação (ruim para a experiência).")
nexty(8)
panel("timeseries","Buffer do player (p50)",[("histogram_quantile(0.5, sum by (le,mode) (rate(streamlab_player_buffer_seconds_bucket[2m])))","{{mode}}")],0,8,unit="s")
panel("timeseries","Erros e quadros descartados",[("sum by (mode,kind) (rate(streamlab_player_errors_total[5m])) * 60","erros/min {{mode}} {{kind}}"),("sum by (mode) (rate(streamlab_player_dropped_frames_total[1m]))","dropped fps {{mode}}")],8,8)
panel("timeseries","Chat: mensagens por segundo",[("sum by (channel) (rate(streamlab_chat_messages_total[1m]))","{{channel}}"),("sum by (reason) (rate(streamlab_chat_messages_rejected_total[1m]))","rejeitadas {{reason}}")],16,8,unit="short")
nexty(8)

dash={"uid":"streamlab","title":"StreamLab — pipeline de live streaming","tags":["streaming","streamlab"],
 "timezone":"browser","schemaVersion":41,"version":1,"refresh":"5s","editable":True,
 "time":{"from":"now-15m","to":"now"},"panels":panels,
 "description":"Ingest → Transcode → Entrega → Espectador. Leia de cima para baixo: é a ordem em que o vídeo percorre o sistema."}
json.dump(dash,open(sys.argv[1],"w"),indent=1,ensure_ascii=False)
print(len(panels),"panels")
