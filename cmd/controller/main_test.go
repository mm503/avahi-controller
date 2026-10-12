package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/mm503/avahi-controller/internal/hostsfile"
	"os"
	"path/filepath"
	"strings"
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

func TestRun_ValidatesFlagsBeforeConnecting(t *testing.T) {
	for _, args := range [][]string{{"--resync-period=0"}, {"--resync-period=-1s"}, {"--unknown"}, {"--kubeconfig=/does/not/exist"}} {
		if err := runArgs(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if err := runArgs([]string{"--help"}); err != nil {
		t.Fatal(err)
	}
}

func TestRun_CleanupOnExitRemovesBlock(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("cleanup=%v/failed=%v", cleanup, failed), func(t *testing.T) {
				mgr := &hostsfile.Manager{FilePath: filepath.Join(t.TempDir(), "hosts")}
				static := "127.0.0.1 localhost\n"
				if err := os.WriteFile(mgr.FilePath, []byte(static), 0644); err != nil {
					t.Fatal(err)
				}
				failure := errors.New("controller failed")
				run := func(context.Context) error {
					if err := mgr.WriteBlock([]hostsfile.HostEntry{{IP: "10.0.0.1", Hostname: "nas.local"}}); err != nil {
						t.Fatal(err)
					}
					if failed {
						return failure
					}
					return nil
				}
				err := finishController(context.Background(), run, mgr, nil, cleanup)
				if failed != errors.Is(err, failure) {
					t.Fatalf("run error: %v", err)
				}
				entries, err := mgr.ReadBlock()
				if err != nil {
					t.Fatal(err)
				}
				if cleanup && len(entries) != 0 || !cleanup && len(entries) != 1 {
					t.Fatalf("cleanup=%v entries=%v", cleanup, entries)
				}
				data, err := os.ReadFile(mgr.FilePath)
				if err != nil || !strings.HasPrefix(string(data), static) {
					t.Fatalf("static content lost: %q %v", data, err)
				}
			})
		}
	}
}

func TestBuildConfig_LoadsKubeconfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	data := `apiVersion: v1
kind: Config
clusters:
- name: local
  cluster:
    server: https://127.0.0.1:6443
contexts:
- name: local
  context:
    cluster: local
    user: local
current-context: local
users:
- name: local
  user:
    token: test
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := buildConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "https://127.0.0.1:6443" || config.BearerToken != "test" {
		t.Fatalf("config: %v", config.Host)
	}
}
