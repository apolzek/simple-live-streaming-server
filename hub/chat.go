package main

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

// Chat é um pub/sub em memória: uma "sala" por canal. Em produção isso
// viraria Redis/NATS para escalar em várias réplicas (veja o README).
type Chat struct {
	mu    sync.Mutex
	rooms map[string]*room
}

type room struct {
	clients map[*client]struct{}
	history []chatMsg
}

type client struct {
	send chan []byte
	nick string
}

type chatMsg struct {
	Type  string `json:"type"`
	Nick  string `json:"nick,omitempty"`
	Color string `json:"color,omitempty"`
	Text  string `json:"text,omitempty"`
	TS    int64  `json:"ts"`
	Count int    `json:"count,omitempty"`
}

var palette = []string{"#ff7a59", "#2ec4b6", "#e9c46a", "#8ecae6", "#f28482", "#90be6d", "#c77dff", "#f4a261"}

func colorFor(nick string) string {
	h := fnv.New32a()
	h.Write([]byte(nick))
	return palette[h.Sum32()%uint32(len(palette))]
}

func NewChat() *Chat { return &Chat{rooms: map[string]*room{}} }

func (c *Chat) Viewers(ch string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.rooms[ch]; ok {
		return len(r.clients)
	}
	return 0
}

func (c *Chat) broadcastLocked(r *room, m chatMsg) {
	b, _ := json.Marshal(m)
	for cl := range r.clients {
		select {
		case cl.send <- b:
		default: // cliente lento: descarta em vez de travar a sala inteira
		}
	}
}

func (c *Chat) join(ch string, cl *client) []chatMsg {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.rooms[ch]
	if !ok {
		r = &room{clients: map[*client]struct{}{}}
		c.rooms[ch] = r
	}
	r.clients[cl] = struct{}{}
	chatConnections.WithLabelValues(ch).Set(float64(len(r.clients)))
	c.broadcastLocked(r, chatMsg{Type: "viewers", Count: len(r.clients), TS: time.Now().UnixMilli()})
	return append([]chatMsg(nil), r.history...)
}

func (c *Chat) leave(ch string, cl *client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.rooms[ch]
	delete(r.clients, cl)
	chatConnections.WithLabelValues(ch).Set(float64(len(r.clients)))
	c.broadcastLocked(r, chatMsg{Type: "viewers", Count: len(r.clients), TS: time.Now().UnixMilli()})
}

func (c *Chat) post(ch string, m chatMsg) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.rooms[ch]
	r.history = append(r.history, m)
	if len(r.history) > 50 {
		r.history = r.history[len(r.history)-50:]
	}
	c.broadcastLocked(r, m)
}

func cleanNick(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
		if b.Len() >= 20 {
			break
		}
	}
	if b.Len() == 0 {
		return "anon"
	}
	return b.String()
}

func (c *Chat) Handle(w http.ResponseWriter, r *http.Request) {
	ch := r.PathValue("name")
	if !validName.MatchString(ch) {
		http.Error(w, "canal inválido", 400)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 10)

	cl := &client{send: make(chan []byte, 64), nick: cleanNick(r.URL.Query().Get("nick"))}
	history := c.join(ch, cl)
	defer c.leave(ch, cl)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// escritor
	go func() {
		defer cancel()
		for _, m := range history {
			b, _ := json.Marshal(m)
			if conn.Write(ctx, websocket.MessageText, b) != nil {
				return
			}
		}
		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case b := <-cl.send:
				wctx, wc := context.WithTimeout(ctx, 5*time.Second)
				err := conn.Write(wctx, websocket.MessageText, b)
				wc()
				if err != nil {
					return
				}
			case <-ping.C:
				if conn.Ping(ctx) != nil {
					return
				}
			}
		}
	}()

	// leitor, com rate limit simples: 1 mensagem por segundo (rajada de 3)
	tokens, last := 3.0, time.Now()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var in struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(data, &in) != nil {
			continue
		}
		text := strings.TrimSpace(in.Text)
		now := time.Now()
		tokens = min(3, tokens+now.Sub(last).Seconds())
		last = now
		switch {
		case text == "":
			continue
		case utf8.RuneCountInString(text) > 300:
			chatDropped.WithLabelValues(ch, "too_long").Inc()
			continue
		case tokens < 1:
			chatDropped.WithLabelValues(ch, "rate_limit").Inc()
			b, _ := json.Marshal(chatMsg{Type: "system", Text: "calma! limite de 1 msg/s", TS: now.UnixMilli()})
			select {
			case cl.send <- b:
			default:
			}
			continue
		}
		tokens--
		chatMessages.WithLabelValues(ch).Inc()
		c.post(ch, chatMsg{Type: "chat", Nick: cl.nick, Color: colorFor(cl.nick), Text: text, TS: now.UnixMilli()})
	}
}
