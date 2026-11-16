// Command hub é o "cérebro" do StreamLab: autentica stream keys, dispara o
// transcoder ABR (FFmpeg), serve a interface web, o chat via WebSocket e
// coleta métricas de QoE enviadas pelos players.
//
// Ele é propositalmente pequeno (só stdlib + 2 libs) para ser lido em uma tarde.
package main

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

//go:embed web
var webFS embed.FS

type Config struct {
	Addr        string // onde o hub escuta (UI + API)
	MediaMTXAPI string // API de controle do MediaMTX
	RTSPBase    string // de onde o transcoder lê o sinal original
	HLSDir      string // onde o transcoder escreve o HLS ABR (servido pelo edge)
	DataDir     string // onde ficam os canais/stream keys
	DemoChannel string
	DemoKey     string
	Ladder      []Rendition
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func loadConfig() Config {
	ladder, err := ParseLadder(env("TRANSCODE_LADDER", DefaultLadder))
	if err != nil {
		slog.Error("TRANSCODE_LADDER inválido", "err", err)
		os.Exit(1)
	}
	return Config{
		Addr:        env("HUB_ADDR", ":8000"),
		MediaMTXAPI: env("MEDIAMTX_API", "http://mediamtx:9997"),
		RTSPBase:    env("MEDIAMTX_RTSP", "rtsp://mediamtx:8554"),
		HLSDir:      env("HLS_DIR", "/hls"),
		DataDir:     env("DATA_DIR", "/data"),
		DemoChannel: env("DEMO_CHANNEL", "demo"),
		DemoKey:     env("DEMO_KEY", "demo-key-troque-me"),
		Ladder:      ladder,
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg := loadConfig()

	store, err := NewStore(cfg.DataDir)
	if err != nil {
		slog.Error("abrindo store", "err", err)
		os.Exit(1)
	}
	if cfg.DemoChannel != "" {
		store.Ensure(cfg.DemoChannel, "Canal de demonstração (padrão de teste do FFmpeg)", cfg.DemoKey)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	tc := NewTranscoders(cfg, store)
	go tc.Run(ctx)

	chat := NewChat()
	qoe := NewQoE()
	go qoe.Run(ctx)

	api := &API{cfg: cfg, store: store, tc: tc, chat: chat, qoe: qoe}

	mux := http.NewServeMux()
	// --- integração com o servidor de mídia
	mux.HandleFunc("POST /api/mediamtx/auth", api.mediamtxAuth)
	// --- API pública usada pela UI
	mux.HandleFunc("GET /api/config", api.config)
	mux.HandleFunc("GET /api/live", api.live)
	mux.HandleFunc("GET /api/channels/{name}", api.channel)
	mux.HandleFunc("POST /api/channels", api.createChannel)
	mux.HandleFunc("POST /api/qoe", qoe.Handle)
	mux.HandleFunc("GET /ws/chat/{name}", chat.Handle)
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	// --- páginas
	sub, _ := fs.Sub(webFS, "web")
	static := http.FileServerFS(sub)
	page := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, sub, name)
		}
	}
	mux.HandleFunc("GET /{$}", page("index.html"))
	mux.HandleFunc("GET /watch/{name}", page("watch.html"))
	mux.HandleFunc("GET /studio", page("studio.html"))
	mux.HandleFunc("GET /learn", page("learn.html"))
	mux.Handle("GET /static/", http.StripPrefix("/static/", static))

	srv := &http.Server{Addr: cfg.Addr, Handler: logRequests(mux), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	slog.Info("hub no ar", "addr", cfg.Addr, "ladder", cfg.Ladder)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("http", "err", err)
		os.Exit(1)
	}
	tc.StopAll()
}
