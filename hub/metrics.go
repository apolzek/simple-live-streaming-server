package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Todas as métricas do hub começam com "streamlab_". As métricas do servidor de
// mídia (MediaMTX) e do edge (NGINX) são coletadas por seus próprios endpoints.
var (
	// ---- API / HTTP
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "streamlab_http_requests_total", Help: "Requisições HTTP atendidas pelo hub.",
	}, []string{"route", "code"})
	httpLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "streamlab_http_request_duration_seconds", Help: "Latência das requisições HTTP do hub.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route"})

	// ---- Ingest / autenticação
	authRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "streamlab_auth_requests_total", Help: "Decisões de autenticação pedidas pelo MediaMTX.",
	}, []string{"action", "protocol", "result"})

	keyRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "streamlab_stream_key_requests_total", Help: "Ações protegidas pela senha do Estúdio (recuperar/regenerar chave, excluir canal, senha errada).",
	}, []string{"result"})

	// ---- Transcoder
	transcoderUp = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "streamlab_transcoder_up", Help: "1 se o processo FFmpeg do canal está rodando.",
	}, []string{"channel"})
	transcoderFPS = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "streamlab_transcoder_fps", Help: "Quadros por segundo processados pelo FFmpeg.",
	}, []string{"channel"})
	transcoderSpeed = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "streamlab_transcoder_speed_ratio", Help: "Velocidade do FFmpeg (1.0 = tempo real; <1 = atrasando).",
	}, []string{"channel"})
	transcoderDropped = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "streamlab_transcoder_dropped_frames", Help: "Quadros descartados pelo FFmpeg desde o início do processo.",
	}, []string{"channel"})
	transcoderRestarts = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "streamlab_transcoder_restarts_total", Help: "Quantas vezes o FFmpeg do canal precisou ser reiniciado.",
	}, []string{"channel"})
	renditionsGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "streamlab_transcoder_renditions", Help: "Número de qualidades (renditions) geradas para o canal.",
	}, []string{"channel"})

	// ---- Canais ao vivo
	liveGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "streamlab_channel_live", Help: "1 se o canal está ao vivo.",
	}, []string{"channel", "ingest"})

	// ---- Chat / audiência
	chatConnections = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "streamlab_viewers", Help: "Espectadores conectados (conexões WebSocket abertas na página do canal).",
	}, []string{"channel"})
	chatMessages = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "streamlab_chat_messages_total", Help: "Mensagens de chat aceitas.",
	}, []string{"channel"})
	chatDropped = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "streamlab_chat_messages_rejected_total", Help: "Mensagens rejeitadas (rate limit, tamanho...).",
	}, []string{"channel", "reason"})

	// ---- QoE (Quality of Experience) reportado pelos players
	playerSessions = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "streamlab_player_sessions", Help: "Sessões de player ativas (heartbeat nos últimos 20s).",
	}, []string{"channel", "mode"})
	playerStartup = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "streamlab_player_startup_seconds", Help: "Tempo entre o play e o primeiro quadro (video start time).",
		Buckets: []float64{0.25, 0.5, 0.75, 1, 1.5, 2, 3, 5, 8, 13},
	}, []string{"mode"})
	playerLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "streamlab_player_latency_seconds", Help: "Latência ao vivo medida pelo player (distância até o 'agora').",
		Buckets: []float64{0.2, 0.5, 1, 2, 3, 4, 6, 8, 10, 15, 20, 30},
	}, []string{"mode"})
	playerBuffer = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "streamlab_player_buffer_seconds", Help: "Segundos de mídia no buffer do player.",
		Buckets: []float64{0, 0.5, 1, 2, 4, 6, 8, 12, 20},
	}, []string{"mode"})
	playerBitrate = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "streamlab_player_bitrate_kbps", Help: "Bitrate da qualidade sendo assistida.",
		Buckets: []float64{300, 600, 900, 1200, 1800, 2500, 3500, 5000, 7000, 10000},
	}, []string{"mode"})
	playerRebuffers = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "streamlab_player_rebuffer_events_total", Help: "Travamentos (stalls) com o vídeo já rodando.",
	}, []string{"channel", "mode"})
	playerRebufferTime = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "streamlab_player_rebuffer_seconds_total", Help: "Tempo total travado.",
	}, []string{"channel", "mode"})
	playerWatchTime = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "streamlab_player_watch_seconds_total", Help: "Tempo assistido (somado pelos heartbeats). Use com rebuffer_seconds para o rebuffering ratio.",
	}, []string{"channel", "mode"})
	playerSwitches = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "streamlab_player_quality_switches_total", Help: "Trocas de qualidade feitas pelo ABR.",
	}, []string{"channel", "direction"})
	playerErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "streamlab_player_errors_total", Help: "Erros reportados pelo player.",
	}, []string{"mode", "kind"})
	playerDropped = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "streamlab_player_dropped_frames_total", Help: "Quadros descartados pelo navegador (decodificação lenta).",
	}, []string{"mode"})
)

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }

// Unwrap permite que o websocket faça Hijack através do wrapper.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// routeLabel mantém a cardinalidade baixa: /watch/foo vira /watch/{name}.
func routeLabel(p string) string {
	switch {
	case strings.HasPrefix(p, "/api/channels/") && strings.HasSuffix(p, "/key"):
		return "/api/channels/{name}/key"
	case strings.HasPrefix(p, "/api/channels/"):
		return "/api/channels/{name}"
	case strings.HasPrefix(p, "/watch/"):
		return "/watch/{name}"
	case strings.HasPrefix(p, "/ws/chat/"):
		return "/ws/chat/{name}"
	case strings.HasPrefix(p, "/static/"):
		return "/static"
	}
	switch p {
	case "/", "/studio", "/learn", "/metrics", "/healthz", "/api/config", "/api/live", "/api/channels", "/api/qoe", "/api/mediamtx/auth":
		return p
	}
	return "other"
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		next.ServeHTTP(rec, r)
		route := routeLabel(r.URL.Path)
		httpRequests.WithLabelValues(route, strconv.Itoa(rec.code)).Inc()
		if route != "/ws/chat/{name}" {
			httpLatency.WithLabelValues(route).Observe(time.Since(start).Seconds())
		}
	})
}
