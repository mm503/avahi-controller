package avahi

import (
	"errors"
	"testing"
)

type mockReloader struct {
	called bool
	err    error
}

func (m *mockReloader) Reload() error {
	m.called = true
	return m.err
}

// TestSystemdReloader_InterfaceCompliance ensures SystemdReloader satisfies Reloader.
func TestSystemdReloader_InterfaceCompliance(t *testing.T) {
	var _ Reloader = &SystemdReloader{}
}

// TestMockReloader verifies the mock itself works, used in reconciler tests.
func TestMockReloader_Success(t *testing.T) {
	m := &mockReloader{}
	if err := m.Reload(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if !m.called {
		t.Error("expected called=true")
	}
}

func TestMockReloader_Error(t *testing.T) {
	m := &mockReloader{err: errors.New("boom")}
	if err := m.Reload(); err == nil {
		t.Fatal("expected error")
	}
}

// --- NewDefaultReloader tests ---

func TestNewDefaultReloader_UsesProvidedName(t *testing.T) {
	r := NewDefaultReloader("custom.service")
	sr, ok := r.(*SystemdReloader)
	if !ok {
		t.Fatalf("expected *SystemdReloader, got %T", r)
	}
	if sr.ServiceName != "custom.service" {
		t.Errorf("got %q, want custom.service", sr.ServiceName)
	}
}

func TestNewDefaultReloader_FallsBackToDefault(t *testing.T) {
	r := NewDefaultReloader("")
	sr, ok := r.(*SystemdReloader)
	if !ok {
		t.Fatalf("expected *SystemdReloader, got %T", r)
	}
	if sr.ServiceName != DefaultServiceName {
		t.Errorf("got %q, want %q", sr.ServiceName, DefaultServiceName)
	}
}
