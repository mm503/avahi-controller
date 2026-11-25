package main

import (
	"slices"
	"testing"
	"time"
)

func TestFlags_RejectsNonPositiveResync(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		if err := validateResync(d); err == nil {
			t.Fatalf("accepted %s", d)
		}
	}
	if err := validateResync(time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestParseHostIPs(t *testing.T) {
	cases := map[string][]string{
		"":                          nil,
		"192.168.1.10":              {"192.168.1.10"},
		" 192.168.1.10 , fd00::10 ": {"192.168.1.10", "fd00::10"},
		"192.168.1.10,,not-an-ip":   {"192.168.1.10"},
	}
	for in, want := range cases {
		if got := parseHostIPs(in); !slices.Equal(got, want) {
			t.Errorf("parseHostIPs(%q) = %v, want %v", in, got, want)
		}
	}
}
