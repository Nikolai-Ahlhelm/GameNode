package console

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"
)

const MaxInputBytes = 16 << 10
const MaxLineBytes = 64 << 10

type Event struct {
	Type      string    `json:"type"`
	Stream    string    `json:"stream,omitempty"`
	Data      string    `json:"data,omitempty"`
	State     string    `json:"state,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

type Session struct {
	ID, ServerID, InstanceID string
	mu                       sync.Mutex
	events                   []Event
	next                     int
	full                     bool
	subscribers              map[chan Event]struct{}
	input                    io.WriteCloser
	state                    string
	detached                 bool
}

type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	current  map[string]string
	detached map[string]bool
}

func NewManager() *Manager {
	return &Manager{sessions: map[string]*Session{}, current: map[string]string{}, detached: map[string]bool{}}
}

func (m *Manager) CreateSession(server, instance string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := newSession(newSessionID(), server, instance)
	m.sessions[s.ID] = s
	m.current[server] = s.ID
	delete(m.detached, server)
	return s
}

func (m *Manager) CurrentSession(server string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.current[server]
	if !ok {
		return nil, false
	}
	s, ok := m.sessions[id]
	return s, ok
}

func (m *Manager) ClearCurrentSession(server, expected string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current[server] == expected {
		delete(m.current, server)
	}
}

func (m *Manager) RemoveSession(server, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current[server] == id {
		delete(m.current, server)
	}
	delete(m.sessions, id)
}

func (m *Manager) MarkDetached(server string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.current, server)
	m.detached[server] = true
}

// IsDetached reports whether a server has been rediscovered without
// attachable console I/O. It never creates a synthetic session.
func (m *Manager) IsDetached(server string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.detached[server]
}

// Start preserves the original server-ID based API while creating a normal,
// uniquely identified session under the current manager model.
func (m *Manager) Start(server string, input io.WriteCloser) *Session {
	s := m.CreateSession(server, "")
	s.AttachInput(input)
	return s
}

// Detach preserves the original server-ID based API. The detached session is
// retained in sessions for compatibility, while current is deliberately empty.
func (m *Manager) Detach(server string) {
	s, ok := m.CurrentSession(server)
	if !ok {
		s = m.CreateSession(server, "")
	}
	s.markDetached()
	m.MarkDetached(server)
}

// Get accepts either a session ID (the new API) or a server ID (the legacy API).
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.sessions[id]; ok {
		return s, true
	}
	if current, ok := m.current[id]; ok {
		s, ok := m.sessions[current]
		return s, ok
	}
	if !m.detached[id] {
		return nil, false
	}
	for _, s := range m.sessions {
		if s.ServerID == id && s.isDetached() {
			return s, true
		}
	}
	return nil, false
}

func newSession(id, server, instance string) *Session {
	return &Session{ID: id, ServerID: server, InstanceID: instance, events: make([]Event, 1000), subscribers: map[chan Event]struct{}{}, state: "running"}
}

func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Session) AttachInput(input io.WriteCloser) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.input == nil && !s.detached {
		s.input = input
	} else if input != nil {
		_ = input.Close()
	}
}

func (s *Session) markDetached() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.input != nil {
		_ = s.input.Close()
		s.input = nil
	}
	s.state = "detached"
	s.detached = true
}

func (s *Session) isDetached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detached
}

// Output returns an io.Writer that publishes each written chunk to stream.
func (s *Session) Output(stream string) io.Writer { return outputSink{session: s, stream: stream} }

type outputSink struct {
	session *Session
	stream  string
}

func (w outputSink) Write(data []byte) (int, error) {
	w.session.Publish(w.stream, string(data))
	return len(data), nil
}

func (s *Session) Publish(stream, data string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.detached {
		return
	}
	e := Event{Type: "output", Stream: stream, Data: data, Timestamp: time.Now().UTC()}
	s.events[s.next] = e
	s.next = (s.next + 1) % len(s.events)
	if s.next == 0 {
		s.full = true
	}
	for c := range s.subscribers {
		select {
		case c <- e:
		default:
			close(c)
			delete(s.subscribers, c)
		}
	}
}

func (s *Session) Subscribe() (<-chan Event, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := make(chan Event, 128)
	if s.detached {
		c <- Event{Type: "state", State: "detached", Timestamp: time.Now().UTC()}
		close(c)
		return c, func() {}
	}
	start := 0
	if s.full {
		start = s.next
	}
	count := s.next
	if s.full {
		count = len(s.events)
	}
	for i := 0; i < count; i++ {
		c <- s.events[(start+i)%len(s.events)]
	}
	c <- Event{Type: "state", State: s.state, Timestamp: time.Now().UTC()}
	s.subscribers[c] = struct{}{}
	return c, func() {
		s.mu.Lock()
		if _, ok := s.subscribers[c]; ok {
			delete(s.subscribers, c)
			close(c)
		}
		s.mu.Unlock()
	}
}

func (s *Session) Input(data string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.detached || s.input == nil {
		return errors.New("console input unavailable")
	}
	if len(data) == 0 || len(data) > MaxInputBytes {
		return errors.New("invalid console input")
	}
	_, err := io.WriteString(s.input, data)
	return err
}

func (s *Session) Close(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.input != nil {
		_ = s.input.Close()
		s.input = nil
	}
	s.state = state
	for c := range s.subscribers {
		select {
		case c <- Event{Type: "state", State: state, Timestamp: time.Now().UTC()}:
		default:
		}
		close(c)
		delete(s.subscribers, c)
	}
}

func (s *Session) Read(stream string, r io.Reader) {
	reader := bufio.NewReaderSize(r, MaxLineBytes)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			s.Publish(stream, line)
		}
		if err != nil {
			return
		}
	}
}
