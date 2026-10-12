package main

import (
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
