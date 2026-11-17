package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// Channel é um canal (como twitch.tv/<nome>). A Key é a "stream key":
// o segredo que o streamer coloca no OBS. Ela NUNCA aparece nas URLs públicas.
type Channel struct {
	Name      string    `json:"name"`
	Title     string    `json:"title"`
	Key       string    `json:"key,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

var validName = regexp.MustCompile(`^[a-z0-9_]{3,25}$`)

type Store struct {
	mu       sync.RWMutex
	path     string
	channels map[string]*Channel
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "channels.json"), channels: map[string]*Channel{}}
	b, err := os.ReadFile(s.path)
	if err == nil {
		if err := json.Unmarshal(b, &s.channels); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	b, _ := json.MarshalIndent(s.channels, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func newKey() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "live_" + hex.EncodeToString(b)
}

// Ensure cria (ou atualiza a key de) um canal fixo, usado pelo canal de demo.
func (s *Store) Ensure(name, title, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.channels[name]
	if !ok {
		c = &Channel{Name: name, Title: title, CreatedAt: time.Now()}
		s.channels[name] = c
	}
	c.Key = key
	s.saveLocked()
}

var (
	ErrInvalidName = errors.New("nome inválido: use 3-25 caracteres [a-z0-9_]")
	ErrExists      = errors.New("canal já existe")
)

func (s *Store) Create(name, title string) (*Channel, error) {
	if !validName.MatchString(name) {
		return nil, ErrInvalidName
	}
	if title == "" {
		title = "Live de " + name
	}
	if len(title) > 120 {
		title = title[:120]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.channels[name]; ok {
		return nil, ErrExists
	}
	c := &Channel{Name: name, Title: title, Key: newKey(), CreatedAt: time.Now()}
	s.channels[name] = c
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	cp := *c
	return &cp, nil
}

// List devolve todos os canais, sem as stream keys, ordenados por nome.
func (s *Store) List() []Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Channel, 0, len(s.channels))
	for _, c := range s.channels {
		cp := *c
		cp.Key = ""
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get devolve uma cópia sem a stream key (seguro para expor).
func (s *Store) Get(name string) (Channel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.channels[name]
	if !ok {
		return Channel{}, false
	}
	cp := *c
	cp.Key = ""
	return cp, true
}

var ErrNotFound = errors.New("canal não existe")

// Key devolve a stream key atual do canal (uso: recuperação no Estúdio).
func (s *Store) Key(name string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.channels[name]
	if !ok {
		return "", ErrNotFound
	}
	return c.Key, nil
}

// RotateKey gera uma nova stream key; a antiga deixa de funcionar para novas
// conexões (quem já está transmitindo continua até desconectar).
func (s *Store) RotateKey(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.channels[name]
	if !ok {
		return "", ErrNotFound
	}
	c.Key = newKey()
	if err := s.saveLocked(); err != nil {
		return "", err
	}
	return c.Key, nil
}

// Delete remove o canal e sua stream key.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.channels[name]; !ok {
		return ErrNotFound
	}
	delete(s.channels, name)
	return s.saveLocked()
}

func (s *Store) CheckKey(name, key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.channels[name]
	// comparação em tempo constante: não vaza quantos caracteres batem
	return ok && key != "" && subtle.ConstantTimeCompare([]byte(c.Key), []byte(key)) == 1
}
