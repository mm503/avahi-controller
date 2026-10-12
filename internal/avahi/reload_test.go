package avahi

import "testing"

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
