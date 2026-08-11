package console

import (
	"io"
	"strings"
	"testing"
	"time"
)

type input struct {
	strings.Builder
	closed int
}

func (i *input) Close() error { i.closed++; return nil }

func TestStartGetDetachCompatibility(t *testing.T) {
	m := NewManager()
	s := m.Start("server-a", &input{})
	if got, ok := m.Get("server-a"); !ok || got != s {
		t.Fatal("Get did not resolve the current session by server ID")
	}
	if got, ok := m.Get(s.ID); !ok || got != s {
		t.Fatal("Get did not resolve the session by session ID")
	}
	m.Detach("server-a")
	detached, ok := m.Get("server-a")
	if !ok || detached != s {
		t.Fatal("Get did not retain the detached session")
	}
	events, _ := detached.Subscribe()
	if event := <-events; event.State != "detached" {
		t.Fatalf("state = %q, want detached", event.State)
	}
}

func TestCreateSessionCreatesUniqueIDs(t *testing.T) {
	m := NewManager()
	a := m.CreateSession("server-a", "instance-a")
	b := m.CreateSession("server-a", "instance-b")
	if a.ID == "" || b.ID == "" || a.ID == b.ID {
		t.Fatalf("session IDs must be unique: %q, %q", a.ID, b.ID)
	}
	if a.ServerID != "server-a" || a.InstanceID != "instance-a" {
		t.Fatalf("unexpected session identity: %#v", a)
	}
}

func TestCurrentSession(t *testing.T) {
	m := NewManager()
	s := m.CreateSession("server-a", "instance-a")
	got, ok := m.CurrentSession("server-a")
	if !ok || got != s {
		t.Fatal("current session missing")
	}
}

func TestClearCurrentSessionIsStaleSafe(t *testing.T) {
	m := NewManager()
	old := m.CreateSession("server-a", "old")
	current := m.CreateSession("server-a", "new")
	m.ClearCurrentSession("server-a", old.ID)
	got, ok := m.CurrentSession("server-a")
	if !ok || got != current {
		t.Fatal("stale clear removed newer session")
	}
}

func TestRemoveSessionIsStaleSafe(t *testing.T) {
	m := NewManager()
	old := m.CreateSession("server-a", "old")
	current := m.CreateSession("server-a", "new")
	m.RemoveSession("server-a", old.ID)
	got, ok := m.CurrentSession("server-a")
	if !ok || got != current {
		t.Fatal("stale removal removed newer current session")
	}
	if _, ok := m.Get(old.ID); ok {
		t.Fatal("removed session remains accessible")
	}
}

func TestMarkDetached(t *testing.T) {
	m := NewManager()
	s := m.CreateSession("server-a", "instance-a")
	m.MarkDetached("server-a")
	if _, ok := m.CurrentSession("server-a"); ok {
		t.Fatal("detached server still has a current session")
	}
	if got, ok := m.Get(s.ID); !ok || got != s {
		t.Fatal("MarkDetached must not remove existing sessions")
	}
	if !m.IsDetached("server-a") {
		t.Fatal("detached state missing")
	}
}

func TestNewSessionClearsDetached(t *testing.T) {
	m := NewManager()
	m.Detach("server-a")
	fresh := m.CreateSession("server-a", "instance-a")
	if m.IsDetached("server-a") {
		t.Fatal("new session did not clear detached state")
	}
	got, ok := m.Get("server-a")
	if !ok || got != fresh {
		t.Fatal("new session did not replace detached server state")
	}
	events, done := fresh.Subscribe()
	defer done()
	if event := <-events; event.State != "running" {
		t.Fatalf("state = %q, want running", event.State)
	}
}

func TestHistoryAndDetached(t *testing.T) {
	m := NewManager()
	s := m.Start("a", &input{})
	s.Publish("stdout", "one")
	events, done := s.Subscribe()
	defer done()
	if (<-events).Data != "one" {
		t.Fatal("history missing")
	}
	m.Detach("b")
	d, _ := m.Get("b")
	c, _ := d.Subscribe()
	if (<-c).State != "detached" {
		t.Fatal("detached missing")
	}
}

func TestInputLimit(t *testing.T) {
	s := NewManager().Start("a", &input{})
	if err := s.Input(strings.Repeat("x", MaxInputBytes+1)); err == nil {
		t.Fatal("oversized input accepted")
	}
	if err := s.Input("ok\n"); err != nil {
		t.Fatal(err)
	}
}

func TestDisconnectAndSlowSubscriberDoNotBlockPublish(t *testing.T) {
	s := NewManager().Start("server", &input{})
	slow, removeSlow := s.Subscribe()
	_, removeFast := s.Subscribe()
	removeFast()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 300; i++ {
			s.Publish("stdout", "line")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slow subscriber blocked publish")
	}
	for range slow {
	}
	removeSlow()
}

var _ io.WriteCloser = (*input)(nil)
