package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type API struct {
	cfg   Config
	store *Store
	tc    *Transcoders
	chat  *Chat
	qoe   *QoE
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// mediamtxAuth implementa a "external HTTP authentication" do MediaMTX:
// a cada conexão (publicar, assistir, API...) o MediaMTX nos pergunta se pode.
// 200 = pode; qualquer outro código = não pode.
//
// Para PUBLICAR em live/<canal> a stream key precisa vir em um destes lugares:
//
//	RTMP (OBS):  rtmp://host:1935/live  +  chave "<canal>?key=<stream key>"
//	SRT:         srt://host:8890?streamid=publish:live/<canal>:<canal>:<stream key>
//	WHIP:        http://host:8889/live/<canal>/whip?key=<stream key>   (ou Bearer token)
func (a *API) mediamtxAuth(w http.ResponseWriter, r *http.Request) {
	var req struct {
		User     string `json:"user"`
		Password string `json:"password"`
		Token    string `json:"token"`
		IP       string `json:"ip"`
		Action   string `json:"action"`
		Path     string `json:"path"`
		Protocol string `json:"protocol"`
		Query    string `json:"query"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	allow, reason := a.decide(req.Action, req.Path, req.Query, req.User, req.Password, req.Token, req.IP)
	result := "allow"
	if !allow {
		result = "deny"
	}
	authRequests.WithLabelValues(req.Action, req.Protocol, result).Inc()
	if req.Action == "publish" || !allow {
		slog.Info("auth", "action", req.Action, "protocol", req.Protocol, "path", req.Path, "ip", req.IP, "result", result, "reason", reason)
	}
	if !allow {
		http.Error(w, reason, http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func isPrivate(ip string) bool {
	p := net.ParseIP(ip)
	return p != nil && (p.IsLoopback() || p.IsPrivate())
}

func (a *API) decide(action, path, query, user, pass, token, ip string) (bool, string) {
	switch action {
	case "read", "playback":
		return true, "assistir é público"
	case "api", "metrics", "pprof":
		// só a rede interna do compose (hub, prometheus) fala com a API/metrics
		if isPrivate(ip) {
			return true, "rede interna"
		}
		return false, "api/metrics só pela rede interna"
	case "publish":
		ch, ok := strings.CutPrefix(path, "live/")
		if !ok || !validName.MatchString(ch) {
			return false, "publique em live/<canal>"
		}
		q, _ := url.ParseQuery(query)
		for _, k := range []string{q.Get("key"), pass, token} {
			if a.store.CheckKey(ch, k) {
				return true, "stream key válida"
			}
		}
		return false, "stream key inválida"
	}
	return false, "ação desconhecida"
}

type liveChannel struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Live        bool           `json:"live"`
	StartedAt   time.Time      `json:"started_at,omitempty"`
	Ingest      string         `json:"ingest,omitempty"`
	Tracks      []string       `json:"tracks,omitempty"`
	Width       int            `json:"width,omitempty"`
	Height      int            `json:"height,omitempty"`
	IngestKbps  float64        `json:"ingest_kbps,omitempty"`
	Viewers     int            `json:"viewers"`
	Transcoding bool           `json:"transcoding"`
	Renditions  []string       `json:"renditions,omitempty"`
	Readers     map[string]int `json:"readers,omitempty"`
	Transcoder  *Progress      `json:"transcoder,omitempty"`
}

func (a *API) describe(ch Channel, p PathInfo, live bool) liveChannel {
	lc := liveChannel{Name: ch.Name, Title: ch.Title, Live: live, Viewers: a.chat.Viewers(ch.Name)}
	if !live {
		return lc
	}
	lc.StartedAt = p.ReadyTime
	lc.Ingest = p.IngestProtocol()
	lc.Tracks = p.Tracks
	lc.Width, lc.Height = p.Resolution()
	lc.IngestKbps = a.tc.IngestKbps(ch.Name)
	_, lc.Transcoding = a.tc.Running(ch.Name)
	lc.Renditions = []string{"source"}
	for _, r := range a.cfg.Ladder {
		lc.Renditions = append(lc.Renditions, r.Name)
	}
	if lc.Transcoding {
		pr := a.tc.Progress(ch.Name)
		lc.Transcoder = &pr
	}
	lc.Readers = map[string]int{}
	for _, rd := range p.Readers {
		lc.Readers[rd.Type]++
	}
	return lc
}

func (a *API) live(w http.ResponseWriter, r *http.Request) {
	out := []liveChannel{}
	for name, p := range a.tc.Snapshot() {
		ch, ok := a.store.Get(name)
		if !ok {
			ch = Channel{Name: name, Title: name}
		}
		out = append(out, a.describe(ch, p, true))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Viewers != out[j].Viewers {
			return out[i].Viewers > out[j].Viewers
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, 200, out)
}

// channels lista todos os canais (ao vivo ou não), ao vivo primeiro.
func (a *API) channels(w http.ResponseWriter, r *http.Request) {
	snap := a.tc.Snapshot()
	out := []liveChannel{}
	for _, ch := range a.store.List() {
		p, live := snap[ch.Name]
		out = append(out, a.describe(ch, p, live))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Live && !out[j].Live })
	writeJSON(w, 200, out)
}

func (a *API) channel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ch, ok := a.store.Get(name)
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "canal não existe"})
		return
	}
	p, live := a.tc.Snapshot()[name]
	writeJSON(w, 200, a.describe(ch, p, live))
}

func (a *API) createChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "json inválido"})
		return
	}
	ch, err := a.store.Create(strings.ToLower(strings.TrimSpace(req.Name)), strings.TrimSpace(req.Title))
	switch {
	case errors.Is(err, ErrInvalidName):
		writeJSON(w, 400, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrExists):
		writeJSON(w, 409, map[string]string{"error": err.Error()})
	case err != nil:
		writeJSON(w, 500, map[string]string{"error": err.Error()})
	default:
		slog.Info("canal criado", "channel", ch.Name)
		writeJSON(w, 201, ch) // única vez que a stream key é devolvida
	}
}

// studioAuth confere a senha do Estúdio; em caso de erro já responde 401.
func (a *API) studioAuth(w http.ResponseWriter, r *http.Request, channel, password string) bool {
	if subtle.ConstantTimeCompare([]byte(password), []byte(a.cfg.StudioPass)) == 1 {
		return true
	}
	keyRequests.WithLabelValues("denied").Inc()
	time.Sleep(500 * time.Millisecond) // freia tentativas de adivinhar a senha
	slog.Warn("senha do estúdio incorreta", "channel", channel, "ip", r.RemoteAddr)
	writeJSON(w, 401, map[string]string{"error": "senha do Estúdio incorreta"})
	return false
}

// deleteChannel apaga o canal. Se estiver ao vivo, derruba a transmissão antes.
func (a *API) deleteChannel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "json inválido"})
		return
	}
	if !a.studioAuth(w, r, name, req.Password) {
		return
	}
	if name == a.cfg.DemoChannel {
		writeJSON(w, 400, map[string]string{"error": "o canal de demonstração é recriado a cada início; desligue-o com DEMO_CHANNEL vazio"})
		return
	}
	if _, ok := a.store.Get(name); !ok {
		writeJSON(w, 404, map[string]string{"error": ErrNotFound.Error()})
		return
	}
	// apaga primeiro: assim uma reconexão do encoder já é recusada no auth
	if err := a.store.Delete(name); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if err := a.tc.Kick(r.Context(), name); err != nil {
		slog.Warn("não consegui derrubar a transmissão", "channel", name, "err", err)
	}
	keyRequests.WithLabelValues("deleted").Inc()
	slog.Info("canal excluído", "channel", name)
	writeJSON(w, 200, map[string]string{"deleted": name})
}

// channelKey recupera (ou regenera, com rotate=true) a stream key de um canal.
// Como o lab não tem login, a prova de "dono" é a senha do Estúdio
// (STUDIO_PASSWORD). Numa plataforma real isso seria a sessão do usuário.
func (a *API) channelKey(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		Password string `json:"password"`
		Rotate   bool   `json:"rotate"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "json inválido"})
		return
	}
	if !a.studioAuth(w, r, name, req.Password) {
		return
	}
	var key string
	var err error
	action := "recovered"
	if req.Rotate {
		action = "rotated"
		key, err = a.store.RotateKey(name)
	} else {
		key, err = a.store.Key(name)
	}
	switch {
	case errors.Is(err, ErrNotFound):
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	case err != nil:
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	keyRequests.WithLabelValues(action).Inc()
	slog.Info("stream key "+action, "channel", name)
	writeJSON(w, 200, map[string]string{"name": name, "key": key})
}

// config devolve as URLs públicas montadas a partir do host que o navegador usou,
// assim funciona em localhost e também na sua rede local (ex: 192.168.0.10).
func (a *API) config(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		host = h
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	writeJSON(w, 200, map[string]any{
		"edge_hls":   "http://" + host + ":" + env("EDGE_PORT", "8081") + "/hls", // HLS ABR via "CDN" (nginx com cache)
		"ll_hls":     "http://" + host + ":8888/live",                            // LL-HLS direto do MediaMTX
		"webrtc":     "http://" + host + ":8889/live",                            // WHEP/WHIP direto do MediaMTX
		"rtmp":       "rtmp://" + host + ":1935/live",
		"srt":        "srt://" + host + ":8890",
		"grafana":    "http://" + host + ":" + env("GRAFANA_PORT", "3000"),
		"prometheus": "http://" + host + ":" + env("PROMETHEUS_PORT", "9090"),
		"ladder":     a.cfg.Ladder,
	})
}
