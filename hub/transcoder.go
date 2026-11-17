package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Rendition é um degrau da "escada" ABR (bitrate ladder).
type Rendition struct {
	Name      string // ex: 480p
	Height    int    // ex: 480
	VideoKbps int    // bitrate alvo do vídeo
	AudioKbps int
}

func (r Rendition) String() string { return fmt.Sprintf("%s@%dk", r.Name, r.VideoKbps) }

// DefaultLadder: além delas sempre existe a "source" (o sinal original, sem
// re-encode, como a qualidade "Source/Original" da Twitch).
const DefaultLadder = "480p:480:1400:128,360p:360:700:96,160p:160:250:64"

func ParseLadder(s string) ([]Rendition, error) {
	var out []Rendition
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		f := strings.Split(part, ":")
		if len(f) != 4 {
			return nil, fmt.Errorf("degrau %q: esperado nome:altura:videoKbps:audioKbps", part)
		}
		h, e1 := strconv.Atoi(f[1])
		v, e2 := strconv.Atoi(f[2])
		a, e3 := strconv.Atoi(f[3])
		if e1 != nil || e2 != nil || e3 != nil || h <= 0 || v <= 0 || a <= 0 {
			return nil, fmt.Errorf("degrau %q: números inválidos", part)
		}
		out = append(out, Rendition{Name: f[0], Height: h, VideoKbps: v, AudioKbps: a})
	}
	return out, nil
}

// PathInfo é o pedaço da resposta de /v3/paths/list do MediaMTX que usamos.
type PathInfo struct {
	Name      string    `json:"name"`
	Ready     bool      `json:"ready"`
	ReadyTime time.Time `json:"readyTime"`
	Source    *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"source"`
	Tracks  []string `json:"tracks"`
	Tracks2 []struct {
		Codec      string `json:"codec"`
		CodecProps struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"codecProps"`
	} `json:"tracks2"`
	Readers []struct {
		Type string `json:"type"`
	} `json:"readers"`
	BytesReceived int64 `json:"bytesReceived"`
	BytesSent     int64 `json:"bytesSent"`
}

func (p PathInfo) Channel() (string, bool) {
	name, ok := strings.CutPrefix(p.Name, "live/")
	if !ok || !validName.MatchString(name) {
		return "", false
	}
	return name, true
}

func (p PathInfo) VideoCodec() string {
	for _, t := range p.Tracks {
		switch t {
		case "H264", "H265", "AV1", "VP8", "VP9":
			return t
		}
	}
	return ""
}

func (p PathInfo) HasAudio() bool {
	for _, t := range p.Tracks {
		switch t {
		case "MPEG-4 Audio", "Opus", "MPEG-1/2 Audio", "AC-3", "G711", "LPCM":
			return true
		}
	}
	return false
}

func (p PathInfo) Resolution() (int, int) {
	for _, t := range p.Tracks2 {
		if t.CodecProps.Width > 0 {
			return t.CodecProps.Width, t.CodecProps.Height
		}
	}
	return 0, 0
}

// IngestProtocol traduz o tipo de conexão do MediaMTX para algo legível.
func (p PathInfo) IngestProtocol() string {
	if p.Source == nil {
		return "desconhecido"
	}
	switch p.Source.Type {
	case "rtmpConn", "rtmpSource":
		return "RTMP"
	case "srtConn", "srtSource":
		return "SRT"
	case "webRTCSession", "webRTCSource":
		return "WebRTC (WHIP)"
	case "rtspSession", "rtspsSession", "rtspSource":
		return "RTSP"
	case "moqSession":
		return "MoQ"
	}
	return p.Source.Type
}

// BuildFFmpegArgs monta o comando do transcoder. É a parte mais didática do
// projeto: leia o comentário de cada bloco.
func BuildFFmpegArgs(input, outDir string, ladder []Rendition, videoCodec string, hasAudio bool, sourceKbps int) []string {
	if sourceKbps <= 0 {
		sourceKbps = 4500
	}
	copySource := videoCodec == "H264" // H.264 pode ir direto pro HLS sem re-encode
	nSplit := len(ladder) + 1          // + thumbnail
	if !copySource {
		nSplit++ // a "source" também precisa ser codificada
	}

	// 1) ENTRADA: lê o sinal original do MediaMTX via RTSP/TCP (sem perdas na rede interna).
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostats", "-progress", "pipe:1",
		"-rtsp_transport", "tcp", "-i", input,
	}

	// 2) FILTROS: duplica o vídeo decodificado e redimensiona para cada degrau.
	var fc strings.Builder
	fc.WriteString(fmt.Sprintf("[0:v]split=%d", nSplit))
	if !copySource {
		fc.WriteString("[vsrc]")
	}
	for i := range ladder {
		fc.WriteString(fmt.Sprintf("[v%d]", i))
	}
	fc.WriteString("[vthumb]")
	for i, r := range ladder {
		fc.WriteString(fmt.Sprintf(";[v%d]scale=-2:%d[o%d]", i, r.Height, i))
	}
	fc.WriteString(";[vthumb]fps=1/5,scale=-2:180[thumb]")
	args = append(args, "-filter_complex", fc.String())

	// 3) MAPEAMENTO: cada rendition = 1 vídeo + (opcionalmente) 1 áudio.
	addAudio := func() {
		if hasAudio {
			args = append(args, "-map", "0:a:0")
		}
	}
	if copySource {
		args = append(args, "-map", "0:v:0")
	} else {
		args = append(args, "-map", "[vsrc]")
	}
	addAudio()
	for i := range ladder {
		args = append(args, "-map", fmt.Sprintf("[o%d]", i))
		addAudio()
	}

	// 4) CODECS. Keyframe a cada 2s, alinhado em todas as qualidades, para o
	//    player poder trocar de qualidade na fronteira de qualquer segmento.
	if copySource {
		// sem re-encode o FFmpeg não sabe o bitrate: damos uma dica para o
		// BANDWIDTH da master playlist (o player usa isso no ABR)
		args = append(args, "-c:v:0", "copy", "-b:v:0", fmt.Sprintf("%dk", sourceKbps))
	} else {
		args = append(args, "-c:v:0", "libx264", "-b:v:0", "4500k", "-maxrate:v:0", "4800k", "-bufsize:v:0", "6000k")
	}
	for i, r := range ladder {
		idx := i + 1
		args = append(args,
			fmt.Sprintf("-c:v:%d", idx), "libx264",
			fmt.Sprintf("-b:v:%d", idx), fmt.Sprintf("%dk", r.VideoKbps),
			fmt.Sprintf("-maxrate:v:%d", idx), fmt.Sprintf("%dk", r.VideoKbps*107/100),
			fmt.Sprintf("-bufsize:v:%d", idx), fmt.Sprintf("%dk", r.VideoKbps*3/2),
		)
	}
	// opções do x264 só para as saídas re-codificadas (no "copy" elas dão erro)
	first := 1
	if !copySource {
		first = 0
	}
	for idx := first; idx <= len(ladder); idx++ {
		args = append(args,
			fmt.Sprintf("-preset:v:%d", idx), "veryfast",
			fmt.Sprintf("-tune:v:%d", idx), "zerolatency",
			fmt.Sprintf("-profile:v:%d", idx), "main",
		)
	}
	args = append(args, "-force_key_frames", "expr:gte(t,n_forced*2)", "-sc_threshold", "0")
	if hasAudio {
		args = append(args, "-c:a", "aac", "-ac", "2", "-ar", "48000", "-b:a:0", "160k")
		for i, r := range ladder {
			args = append(args, fmt.Sprintf("-b:a:%d", i+1), fmt.Sprintf("%dk", r.AudioKbps))
		}
	}

	// 5) EMPACOTAMENTO: HLS com segmentos fMP4 (CMAF) de 2s, playlist de 8
	//    segmentos, PROGRAM-DATE-TIME (permite ao player medir a latência)
	//    e uma master playlist listando todas as qualidades.
	var vsm []string
	name := func(i int) string {
		if i == 0 {
			return "source"
		}
		return ladder[i-1].Name
	}
	for i := 0; i <= len(ladder); i++ {
		if hasAudio {
			vsm = append(vsm, fmt.Sprintf("v:%d,a:%d,name:%s", i, i, name(i)))
		} else {
			vsm = append(vsm, fmt.Sprintf("v:%d,name:%s", i, name(i)))
		}
	}
	args = append(args,
		"-f", "hls", "-hls_time", "2", "-hls_list_size", "8",
		"-hls_flags", "delete_segments+independent_segments+program_date_time",
		"-hls_segment_type", "fmp4", "-hls_fmp4_init_filename", "init.mp4",
		"-master_pl_name", "master.m3u8",
		"-var_stream_map", strings.Join(vsm, " "),
		"-hls_segment_filename", filepath.Join(outDir, "%v", "seg_%05d.m4s"),
		filepath.Join(outDir, "%v", "index.m3u8"),
	)

	// 6) THUMBNAIL: um JPG atualizado a cada 5s para a página inicial.
	args = append(args, "-map", "[thumb]", "-c:v", "mjpeg", "-q:v", "6", "-update", "1", "-f", "image2", filepath.Join(outDir, "thumb.jpg"))
	return args
}

// Progress é o que o FFmpeg reporta via -progress.
type Progress struct {
	FPS     float64 `json:"fps"`
	Speed   float64 `json:"speed"`
	Dropped float64 `json:"dropped_frames"`
}

func (t *Transcoders) Progress(ch string) Progress {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.prog[ch]
}

func (t *Transcoders) setProgress(ch string, f func(*Progress)) {
	t.mu.Lock()
	p := t.prog[ch]
	f(&p)
	t.prog[ch] = p
	t.mu.Unlock()
}

type job struct {
	cancel  context.CancelFunc
	done    chan struct{}
	started time.Time
	info    PathInfo
}

// Transcoders observa o MediaMTX e mantém 1 FFmpeg por canal ao vivo.
type Transcoders struct {
	cfg   Config
	store *Store
	http  *http.Client

	mu    sync.RWMutex
	jobs  map[string]*job
	paths map[string]PathInfo // último snapshot dos canais ao vivo
	kbps  map[string]float64  // bitrate de ingest calculado entre dois snapshots
	prog  map[string]Progress // último progresso reportado por cada FFmpeg
	last  time.Time
}

func NewTranscoders(cfg Config, store *Store) *Transcoders {
	return &Transcoders{cfg: cfg, store: store, http: &http.Client{Timeout: 3 * time.Second},
		jobs: map[string]*job{}, paths: map[string]PathInfo{}, kbps: map[string]float64{}, prog: map[string]Progress{}}
}

func (t *Transcoders) Snapshot() map[string]PathInfo {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]PathInfo, len(t.paths))
	for k, v := range t.paths {
		out[k] = v
	}
	return out
}

func (t *Transcoders) IngestKbps(ch string) float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.kbps[ch]
}

func (t *Transcoders) Running(ch string) (time.Time, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	j, ok := t.jobs[ch]
	if !ok {
		return time.Time{}, false
	}
	return j.started, true
}

func (t *Transcoders) fetch(ctx context.Context) ([]PathInfo, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", t.cfg.MediaMTXAPI+"/v3/paths/list?itemsPerPage=1000", nil)
	resp, err := t.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("mediamtx api: %s: %s", resp.Status, b)
	}
	var body struct {
		Items []PathInfo `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Items, nil
}

// Run é o "reconciliation loop": a cada 2s compara o que está ao vivo no
// MediaMTX com os FFmpegs rodando e liga/desliga o que for preciso.
func (t *Transcoders) Run(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		items, err := t.fetch(ctx)
		if err != nil {
			slog.Warn("não consegui listar paths do MediaMTX", "err", err)
		} else {
			t.reconcile(ctx, items)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (t *Transcoders) reconcile(ctx context.Context, items []PathInfo) {
	live := map[string]PathInfo{}
	for _, p := range items {
		if ch, ok := p.Channel(); ok && p.Ready && p.VideoCodec() != "" {
			live[ch] = p
		}
	}

	t.mu.Lock()
	// canais que caíram
	for ch, old := range t.paths {
		if _, ok := live[ch]; !ok {
			liveGauge.DeleteLabelValues(ch, old.IngestProtocol())
		}
	}
	now := time.Now()
	kbps := map[string]float64{}
	for ch, p := range live {
		if old, ok := t.paths[ch]; ok && !t.last.IsZero() && p.BytesReceived >= old.BytesReceived {
			kbps[ch] = float64(p.BytesReceived-old.BytesReceived) * 8 / now.Sub(t.last).Seconds() / 1000
		}
	}
	t.paths, t.kbps, t.last = live, kbps, now
	for ch, p := range live {
		liveGauge.WithLabelValues(ch, p.IngestProtocol()).Set(1)
	}
	var toStop []*job
	var stopped []string
	for ch, j := range t.jobs {
		p, ok := live[ch]
		// parou de transmitir OU reconectou com outro codec/trilhas → reinicia
		if !ok || !p.ReadyTime.Equal(j.info.ReadyTime) {
			toStop = append(toStop, j)
			stopped = append(stopped, ch)
			delete(t.jobs, ch)
		}
	}
	var toStart []PathInfo
	for ch, p := range live {
		if _, ok := t.jobs[ch]; !ok {
			jctx, cancel := context.WithCancel(ctx)
			j := &job{cancel: cancel, done: make(chan struct{}), started: time.Now(), info: p}
			t.jobs[ch] = j
			toStart = append(toStart, p)
			go t.supervise(jctx, ch, j)
		}
	}
	t.mu.Unlock()

	for i, j := range toStop {
		j.cancel()
		<-j.done
		slog.Info("transcoder parado", "channel", stopped[i])
	}
	for _, p := range toStart {
		slog.Info("canal ao vivo, iniciando transcoder", "path", p.Name, "ingest", p.IngestProtocol(), "tracks", p.Tracks)
	}
}

// supervise roda o FFmpeg e o reinicia (com backoff) se ele morrer enquanto o
// canal continua ao vivo.
func (t *Transcoders) supervise(ctx context.Context, ch string, j *job) {
	defer close(j.done)
	outDir := filepath.Join(t.cfg.HLSDir, ch)
	defer func() {
		for _, g := range []interface{ DeleteLabelValues(...string) bool }{transcoderUp, transcoderFPS, transcoderSpeed, transcoderDropped, renditionsGauge} {
			g.DeleteLabelValues(ch)
		}
		// dá tempo dos players baixarem os últimos segmentos antes de apagar
		t.mu.Lock()
		delete(t.prog, ch)
		t.mu.Unlock()
		go func() { time.Sleep(15 * time.Second); t.cleanup(ch) }()
	}()

	backoff := time.Second
	for attempt := 0; ctx.Err() == nil; attempt++ {
		if attempt > 0 {
			transcoderRestarts.WithLabelValues(ch).Inc()
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
		}
		os.RemoveAll(outDir)
		os.MkdirAll(outDir, 0o755)
		codec := j.info.VideoCodec()
		if j.info.Source != nil && j.info.Source.Type == "webRTCSession" {
			// navegadores mandam keyframes raramente (só quando pedidos): se
			// copiássemos, os segmentos da "source" teriam tamanhos irregulares.
			codec = "reencode:" + codec
		}
		args := BuildFFmpegArgs(t.cfg.RTSPBase+"/live/"+ch, outDir, t.cfg.Ladder, codec, j.info.HasAudio(), int(t.IngestKbps(ch)))
		renditionsGauge.WithLabelValues(ch).Set(float64(len(t.cfg.Ladder) + 1))
		start := time.Now()
		err := t.runFFmpeg(ctx, ch, args)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		slog.Warn("ffmpeg saiu, reiniciando", "channel", ch, "err", err, "backoff", backoff.String())
	}
}

func (t *Transcoders) runFFmpeg(ctx context.Context, ch string, args []string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return err
	}
	transcoderUp.WithLabelValues(ch).Set(1)
	defer transcoderUp.WithLabelValues(ch).Set(0)

	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			slog.Warn("ffmpeg", "channel", ch, "msg", sc.Text())
		}
	}()
	// -progress pipe:1 escreve blocos "chave=valor" a cada ~0,5s
	type sample struct {
		wall time.Time
		out  float64
	}
	var win []sample
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "fps":
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				transcoderFPS.WithLabelValues(ch).Set(f)
				t.setProgress(ch, func(p *Progress) { p.FPS = f })
			}
		case "out_time_us":
			// O "speed=" do FFmpeg é uma média desde o início (inclui o tempo
			// de conexão). Calculamos a velocidade numa janela de ~5s:
			// segundos de mídia produzidos ÷ segundos de relógio. (A janela
			// também absorve as rajadas em que o pipe entrega várias linhas juntas.)
			us, err := strconv.ParseFloat(v, 64)
			if err != nil || us <= 0 {
				continue
			}
			now := time.Now()
			win = append(win, sample{now, us})
			for len(win) > 2 && now.Sub(win[1].wall) >= 5*time.Second {
				win = win[1:]
			}
			if dt := now.Sub(win[0].wall).Seconds(); dt >= 3 {
				sp := math.Round((us-win[0].out)/1e6/dt*1000) / 1000
				transcoderSpeed.WithLabelValues(ch).Set(sp)
				t.setProgress(ch, func(p *Progress) { p.Speed = sp })
			}
		case "drop_frames":
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				transcoderDropped.WithLabelValues(ch).Set(f)
				t.setProgress(ch, func(p *Progress) { p.Dropped = f })
			}
		}
	}
	return cmd.Wait()
}

func (t *Transcoders) cleanup(ch string) {
	t.mu.RLock()
	_, running := t.jobs[ch]
	t.mu.RUnlock()
	if !running {
		os.RemoveAll(filepath.Join(t.cfg.HLSDir, ch))
	}
}

// Kick derruba quem está publicando no canal, usando a API do MediaMTX.
func (t *Transcoders) Kick(ctx context.Context, ch string) error {
	p, ok := t.Snapshot()[ch]
	if !ok || p.Source == nil || p.Source.ID == "" {
		return nil // não está ao vivo
	}
	kind := map[string]string{
		"rtmpConn":      "rtmpconns",
		"srtConn":       "srtconns",
		"webRTCSession": "webrtcsessions",
		"rtspSession":   "rtspsessions",
		"rtspsSession":  "rtspssessions",
	}[p.Source.Type]
	if kind == "" {
		return fmt.Errorf("não sei derrubar fonte do tipo %s", p.Source.Type)
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/v3/%s/kick/%s", t.cfg.MediaMTXAPI, kind, p.Source.ID), nil)
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("mediamtx kick: %s", resp.Status)
	}
	return nil
}

func (t *Transcoders) StopAll() {
	t.mu.Lock()
	jobs := t.jobs
	t.jobs = map[string]*job{}
	t.mu.Unlock()
	for _, j := range jobs {
		j.cancel()
		<-j.done
	}
}
