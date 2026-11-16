package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// QoE recebe "beacons" dos players. Isso é observabilidade do LADO DO CLIENTE:
// o servidor pode estar 100% saudável e o usuário estar vendo o vídeo travar
// (Wi-Fi ruim, CPU fraca, CDN distante). Só o player sabe disso.
//
// Eventos aceitos:
//
//	startup   {startup_ms}                        primeiro quadro na tela
//	heartbeat {latency_s, buffer_s, bitrate_kbps, dropped_frames, interval_s}  a cada 5s
//	rebuffer  {rebuffer_ms}                       terminou um travamento
//	switch    {direction: up|down}                ABR trocou de qualidade
//	error     {kind}                              erro fatal/de rede
type QoE struct {
	mu       sync.Mutex
	sessions map[string]sessionInfo
}

type sessionInfo struct {
	channel, mode string
	seen          time.Time
	dropped       float64
}

type beacon struct {
	Session       string  `json:"session"`
	Channel       string  `json:"channel"`
	Mode          string  `json:"mode"` // hls | llhls | webrtc
	Event         string  `json:"event"`
	StartupMs     float64 `json:"startup_ms"`
	RebufferMs    float64 `json:"rebuffer_ms"`
	LatencyS      float64 `json:"latency_s"`
	BufferS       float64 `json:"buffer_s"`
	BitrateKbps   float64 `json:"bitrate_kbps"`
	DroppedFrames float64 `json:"dropped_frames"`
	IntervalS     float64 `json:"interval_s"`
	Direction     string  `json:"direction"`
	Kind          string  `json:"kind"`
}

func NewQoE() *QoE { return &QoE{sessions: map[string]sessionInfo{}} }

var validModes = map[string]bool{"hls": true, "llhls": true, "webrtc": true}

func (q *QoE) Handle(w http.ResponseWriter, r *http.Request) {
	// sendBeacon manda text/plain; aceitamos qualquer content-type
	var batch []beacon
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	if err := dec.Decode(&batch); err != nil {
		http.Error(w, "esperado um array JSON de eventos", 400)
		return
	}
	for _, b := range batch {
		q.record(b)
	}
	w.WriteHeader(http.StatusNoContent)
}

func clamp(v, lo, hi float64) float64 { return max(lo, min(hi, v)) }

func (q *QoE) record(b beacon) {
	// Labels vindas do cliente precisam ser validadas: senão qualquer um cria
	// milhões de séries no Prometheus (explosão de cardinalidade).
	if !validModes[b.Mode] || !validName.MatchString(b.Channel) || len(b.Session) == 0 || len(b.Session) > 64 {
		return
	}
	q.mu.Lock()
	s := q.sessions[b.Session]
	s.channel, s.mode, s.seen = b.Channel, b.Mode, time.Now()
	q.sessions[b.Session] = s
	q.mu.Unlock()

	switch b.Event {
	case "startup":
		playerStartup.WithLabelValues(b.Mode).Observe(clamp(b.StartupMs, 0, 60000) / 1000)
	case "heartbeat":
		if b.LatencyS > 0 {
			playerLatency.WithLabelValues(b.Mode).Observe(clamp(b.LatencyS, 0, 120))
		}
		playerBuffer.WithLabelValues(b.Mode).Observe(clamp(b.BufferS, 0, 120))
		if b.BitrateKbps > 0 {
			playerBitrate.WithLabelValues(b.Mode).Observe(clamp(b.BitrateKbps, 0, 100000))
		}
		playerWatchTime.WithLabelValues(b.Channel, b.Mode).Add(clamp(b.IntervalS, 0, 30))
		// dropped_frames vem acumulado do navegador: contamos só o delta
		q.mu.Lock()
		s := q.sessions[b.Session]
		if d := b.DroppedFrames - s.dropped; d > 0 && d < 1e6 {
			playerDropped.WithLabelValues(b.Mode).Add(d)
		}
		s.dropped = b.DroppedFrames
		q.sessions[b.Session] = s
		q.mu.Unlock()
	case "rebuffer":
		playerRebuffers.WithLabelValues(b.Channel, b.Mode).Inc()
		playerRebufferTime.WithLabelValues(b.Channel, b.Mode).Add(clamp(b.RebufferMs, 0, 600000) / 1000)
	case "switch":
		if b.Direction == "up" || b.Direction == "down" {
			playerSwitches.WithLabelValues(b.Channel, b.Direction).Inc()
		}
	case "error":
		kind := b.Kind
		switch kind {
		case "network", "media", "fatal", "ice", "other":
		default:
			kind = "other"
		}
		playerErrors.WithLabelValues(b.Mode, kind).Inc()
	}
}

// Run expira sessões sem heartbeat e atualiza o gauge de sessões ativas.
func (q *QoE) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		q.mu.Lock()
		count := map[[2]string]int{}
		for id, s := range q.sessions {
			if time.Since(s.seen) > 20*time.Second {
				delete(q.sessions, id)
				continue
			}
			count[[2]string{s.channel, s.mode}]++
		}
		q.mu.Unlock()
		playerSessions.Reset()
		for k, n := range count {
			playerSessions.WithLabelValues(k[0], k[1]).Set(float64(n))
		}
	}
}
