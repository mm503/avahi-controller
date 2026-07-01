package reconciler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	corev1listers "k8s.io/client-go/listers/core/v1"

	"github.com/mm503/avahi-controller/internal/hostsfile"
)

// --- fakes ---

type fakeServiceLister struct {
	svcs []*corev1.Service
	err  error
}

func (f *fakeServiceLister) List(_ labels.Selector) ([]*corev1.Service, error) {
	return f.svcs, f.err
}

func (f *fakeServiceLister) Services(namespace string) corev1listers.ServiceNamespaceLister {
	panic("not used in tests")
}

type fakeReloader struct {
	called bool
	err    error
}

func (f *fakeReloader) Reload(_ context.Context) error {
	f.called = true
	return f.err
}

// --- builder helpers ---

func makeSvc(ns, name, hostname, ip string) *corev1.Service {
	ann := map[string]string{}
	if hostname != "" {
		ann[annotationHostname] = hostname
	}

	ingress := []corev1.LoadBalancerIngress{}
	if ip != "" {
		ingress = append(ingress, corev1.LoadBalancerIngress{IP: ip})
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: ann,
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
		},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{Ingress: ingress},
		},
	}
}

func newReconciler(t *testing.T, svcs []*corev1.Service, reloader *fakeReloader) (*Reconciler, *hostsfile.Manager) {
	t.Helper()
	dir := t.TempDir()
	mgr := &hostsfile.Manager{FilePath: filepath.Join(dir, "hosts")}
	var r avahiReloader
	if reloader != nil {
		r = reloader
	}
	rec := &Reconciler{
		Lister:   &fakeServiceLister{svcs: svcs},
		HostsMgr: mgr,
		Reloader: r,
		Recorder: nil,
	}
	return rec, mgr
}

// avahiReloader is a local alias so we can pass nil cleanly.
type avahiReloader interface {
	Reload(ctx context.Context) error
}

// --- buildDesiredEntries tests ---

func TestBuildDesiredEntries_SkipsNonLoadBalancer(t *testing.T) {
	svc := makeSvc("default", "svc", "app.local", "10.0.0.1")
	svc.Spec.Type = corev1.ServiceTypeClusterIP

	r, _ := newReconciler(t, []*corev1.Service{svc}, nil)
	entries, requeue, err := r.buildDesiredEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for ClusterIP service, got %v", entries)
	}
	if requeue {
		t.Error("should not requeue for non-LB service")
	}
}

func TestBuildDesiredEntries_SkipsNoAnnotation(t *testing.T) {
	svc := makeSvc("default", "svc", "", "10.0.0.1")

	r, _ := newReconciler(t, []*corev1.Service{svc}, nil)
	entries, requeue, err := r.buildDesiredEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %v", entries)
	}
	if requeue {
		t.Error("should not requeue for unannotated service")
	}
}

func TestBuildDesiredEntries_MissingIP_SignalsRequeue(t *testing.T) {
	svc := makeSvc("default", "svc", "app.local", "")

	r, _ := newReconciler(t, []*corev1.Service{svc}, nil)
	entries, requeue, err := r.buildDesiredEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries (no IP), got %v", entries)
	}
	if !requeue {
		t.Error("should signal requeue when service has no IP")
	}
}

func TestBuildDesiredEntries_PendingIPCountsAccumulateAndReset(t *testing.T) {
	svc := makeSvc("default", "svc", "app.local", "")
	r, _ := newReconciler(t, []*corev1.Service{svc}, nil)

	for i := 1; i <= 3; i++ {
		if _, _, err := r.buildDesiredEntries(); err != nil {
			t.Fatal(err)
		}
		if got := r.pendingIPCounts["default/svc"]; got != i {
			t.Fatalf("after pass %d: pending count = %d, want %d", i, got, i)
		}
	}

	// Once the IP is assigned the counter must be cleared.
	r.Lister = &fakeServiceLister{svcs: []*corev1.Service{
		makeSvc("default", "svc", "app.local", "10.0.0.1"),
	}}
	if _, _, err := r.buildDesiredEntries(); err != nil {
		t.Fatal(err)
	}
	if len(r.pendingIPCounts) != 0 {
		t.Errorf("pending counts should be cleared after IP assignment, got %v", r.pendingIPCounts)
	}
}

func TestBuildDesiredEntries_WarnsAtPendingThreshold(t *testing.T) {
	svc := makeSvc("default", "svc", "app.local", "")
	r, _ := newReconciler(t, []*corev1.Service{svc}, nil)
	r.pendingIPCounts = map[string]int{"default/svc": pendingIPEventThreshold - 1}

	buf := captureLog(t)
	if _, _, err := r.buildDesiredEntries(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "still has no LoadBalancer IP") {
		t.Errorf("expected warning at threshold, got log output:\n%s", buf.String())
	}
}

func TestBuildDesiredEntries_SingleService(t *testing.T) {
	svc := makeSvc("default", "svc", "app.local", "10.0.0.1")

	r, _ := newReconciler(t, []*corev1.Service{svc}, nil)
	entries, requeue, err := r.buildDesiredEntries()
	if err != nil {
		t.Fatal(err)
	}
	if requeue {
		t.Error("should not requeue when IP is assigned")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].IP != "10.0.0.1" || entries[0].Hostname != "app.local" {
		t.Errorf("unexpected entry: %+v", entries[0])
	}
}

func TestBuildDesiredEntries_HostnameConflict(t *testing.T) {
	svc1 := makeSvc("default", "svc1", "app.local", "10.0.0.1")
	svc2 := makeSvc("default", "svc2", "app.local", "10.0.0.2")

	r, _ := newReconciler(t, []*corev1.Service{svc1, svc2}, nil)
	entries, _, err := r.buildDesiredEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry (first wins), got %d", len(entries))
	}
	if entries[0].IP != "10.0.0.1" {
		t.Errorf("first service should win conflict, got IP %s", entries[0].IP)
	}
}

func TestBuildDesiredEntries_HostnameConflictIsCaseInsensitive(t *testing.T) {
	svc1 := makeSvc("default", "svc1", "APP.Local", "10.0.0.1")
	svc2 := makeSvc("default", "svc2", "app.local", "10.0.0.2")

	r, _ := newReconciler(t, []*corev1.Service{svc1, svc2}, nil)
	entries, _, err := r.buildDesiredEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (case-insensitive conflict, first wins), got %d", len(entries))
	}
	if entries[0].IP != "10.0.0.1" || entries[0].Hostname != "APP.Local" {
		t.Errorf("unexpected winner: %+v", entries[0])
	}
}

func TestBuildDesiredEntries_ConflictWinnerIsDeterministic(t *testing.T) {
	svc1 := makeSvc("default", "svc1", "app.local", "10.0.0.1")
	svc2 := makeSvc("default", "svc2", "app.local", "10.0.0.2")

	// Same services, opposite lister order — winner must not change.
	for _, svcs := range [][]*corev1.Service{
		{svc1, svc2},
		{svc2, svc1},
	} {
		r, _ := newReconciler(t, svcs, nil)
		entries, _, err := r.buildDesiredEntries()
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		if entries[0].IP != "10.0.0.1" {
			t.Errorf("conflict winner should be svc1 (lowest namespace/name) regardless of lister order, got IP %s", entries[0].IP)
		}
	}
}

func TestBuildDesiredEntries_RejectsInvalidHostname(t *testing.T) {
	invalid := []string{
		"has space.local",
		"inject.local\n10.0.0.9 evil.local",
		"fake.local\n### END k8s-avahi-controller ###",
		"-leading-hyphen.local",
		"trailing-dot.local.",
		"under_score.local",
		strings.Repeat("a", 254),
	}

	for _, hostname := range invalid {
		svc := makeSvc("default", "svc", hostname, "10.0.0.1")
		r, _ := newReconciler(t, []*corev1.Service{svc}, nil)
		entries, requeue, err := r.buildDesiredEntries()
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("hostname %q should be rejected, got entries %v", hostname, entries)
		}
		if requeue {
			t.Errorf("hostname %q should not trigger requeue", hostname)
		}
	}
}

func TestBuildDesiredEntries_AcceptsValidHostnames(t *testing.T) {
	valid := []string{"app.local", "APP.Local", "a.b-c.local", "single"}

	for _, hostname := range valid {
		svc := makeSvc("default", "svc", hostname, "10.0.0.1")
		r, _ := newReconciler(t, []*corev1.Service{svc}, nil)
		entries, _, err := r.buildDesiredEntries()
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Errorf("hostname %q should be accepted, got entries %v", hostname, entries)
		}
	}
}

// --- Reconcile integration ---

func TestReconcile_WritesFileOnFirstRun(t *testing.T) {
	svc := makeSvc("default", "svc", "app.local", "10.0.0.1")
	reloader := &fakeReloader{}
	r, mgr := newReconciler(t, []*corev1.Service{svc}, reloader)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reloader.called {
		t.Error("expected avahi reload to be called")
	}
	entries, err := mgr.ReadBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].IP != "10.0.0.1" {
		t.Errorf("unexpected entries in file: %v", entries)
	}
}

func TestReconcile_SkipsReloadWhenUnchanged(t *testing.T) {
	svc := makeSvc("default", "svc", "app.local", "10.0.0.1")
	reloader := &fakeReloader{}
	r, _ := newReconciler(t, []*corev1.Service{svc}, reloader)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	reloader.called = false

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reloader.called {
		t.Error("reload should be skipped when state is unchanged")
	}
}

func TestReconcile_RetriesReloadAfterFailure(t *testing.T) {
	svc := makeSvc("default", "svc", "app.local", "10.0.0.1")
	reloader := &fakeReloader{err: errors.New("avahi-daemon not running")}
	r, _ := newReconciler(t, []*corev1.Service{svc}, reloader)

	if err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("expected error from failed reload")
	}

	// The file was written, so the hash now matches — but the reload must
	// still be retried on the next pass.
	reloader.called = false
	reloader.err = nil

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("retry should succeed: %v", err)
	}
	if !reloader.called {
		t.Error("expected reload to be retried after earlier failure")
	}

	// Once the reload succeeds, further unchanged passes must not reload.
	reloader.called = false
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reloader.called {
		t.Error("reload should not repeat after a successful retry")
	}
}

func TestReconcile_NoReloaderNilSafe(t *testing.T) {
	svc := makeSvc("default", "svc", "app.local", "10.0.0.1")
	r, _ := newReconciler(t, []*corev1.Service{svc}, nil) // nil reloader

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("nil reloader should not cause error: %v", err)
	}
}

func TestReconcile_RequeueOnMissingIP(t *testing.T) {
	svc := makeSvc("default", "svc", "app.local", "")
	r, _ := newReconciler(t, []*corev1.Service{svc}, nil)

	err := r.Reconcile(context.Background())
	if !errors.Is(err, ErrMissingIP) {
		t.Errorf("expected ErrMissingIP, got %v", err)
	}
}

// --- qualifies tests ---

func TestQualifies(t *testing.T) {
	r := &Reconciler{}

	tests := []struct {
		name string
		svc  *corev1.Service
		want bool
	}{
		{
			name: "LoadBalancer with annotation",
			svc:  makeSvc("default", "svc", "app.local", ""),
			want: true,
		},
		{
			name: "ClusterIP with annotation",
			svc: func() *corev1.Service {
				s := makeSvc("default", "svc", "app.local", "")
				s.Spec.Type = corev1.ServiceTypeClusterIP
				return s
			}(),
			want: false,
		},
		{
			name: "LoadBalancer without annotation",
			svc:  makeSvc("default", "svc", "", ""),
			want: false,
		},
		{
			name: "LoadBalancer with whitespace-only annotation",
			svc: func() *corev1.Service {
				s := makeSvc("default", "svc", "   ", "")
				return s
			}(),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.qualifies(tc.svc); got != tc.want {
				t.Errorf("qualifies() = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- loadBalancerIP tests ---

func TestLoadBalancerIP(t *testing.T) {
	t.Run("returns IP when present", func(t *testing.T) {
		svc := makeSvc("default", "svc", "", "10.0.0.1")
		if got := loadBalancerIP(svc); got != "10.0.0.1" {
			t.Errorf("got %q, want 10.0.0.1", got)
		}
	})
	t.Run("returns empty when no ingress", func(t *testing.T) {
		svc := makeSvc("default", "svc", "", "")
		if got := loadBalancerIP(svc); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
	t.Run("skips hostname-only ingress entries", func(t *testing.T) {
		svc := makeSvc("default", "svc", "", "")
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{
			{Hostname: "lb.example.com"},
			{IP: "10.0.0.2"},
		}
		if got := loadBalancerIP(svc); got != "10.0.0.2" {
			t.Errorf("got %q, want 10.0.0.2", got)
		}
	})
	t.Run("returns empty when all entries are hostname-only", func(t *testing.T) {
		svc := makeSvc("default", "svc", "", "")
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{
			{Hostname: "lb.example.com"},
		}
		if got := loadBalancerIP(svc); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

// --- logDiff tests ---

// captureLog redirects slog output to a buffer for the duration of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return &buf
}

func TestLogDiff(t *testing.T) {
	tests := []struct {
		name    string
		old     []hostsfile.HostEntry
		desired []hostsfile.HostEntry
		want    []string // substrings expected in log output
		notWant []string // substrings that must not appear
	}{
		{
			name:    "add record",
			old:     nil,
			desired: []hostsfile.HostEntry{{IP: "10.0.0.1", Hostname: "a.local"}},
			want:    []string{"adding DNS record", "a.local", "10.0.0.1"},
			notWant: []string{"removing DNS record", "updating DNS record"},
		},
		{
			name:    "remove record",
			old:     []hostsfile.HostEntry{{IP: "10.0.0.1", Hostname: "a.local"}},
			desired: nil,
			want:    []string{"removing DNS record", "a.local", "10.0.0.1"},
			notWant: []string{"adding DNS record", "updating DNS record"},
		},
		{
			name:    "update IP",
			old:     []hostsfile.HostEntry{{IP: "10.0.0.1", Hostname: "a.local"}},
			desired: []hostsfile.HostEntry{{IP: "10.0.0.2", Hostname: "a.local"}},
			want:    []string{"updating DNS record", "a.local", "10.0.0.2", "10.0.0.1"},
			notWant: []string{"adding DNS record", "removing DNS record"},
		},
		{
			name:    "unchanged record produces no output",
			old:     []hostsfile.HostEntry{{IP: "10.0.0.1", Hostname: "a.local"}},
			desired: []hostsfile.HostEntry{{IP: "10.0.0.1", Hostname: "a.local"}},
			notWant: []string{"adding DNS record", "removing DNS record", "updating DNS record"},
		},
		{
			name: "add and remove simultaneously",
			old:  []hostsfile.HostEntry{{IP: "10.0.0.1", Hostname: "old.local"}},
			desired: []hostsfile.HostEntry{
				{IP: "10.0.0.2", Hostname: "new.local"},
			},
			want:    []string{"adding DNS record", "new.local", "removing DNS record", "old.local"},
			notWant: []string{"updating DNS record"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLog(t)
			logDiff(tc.old, tc.desired)
			out := buf.String()
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("expected %q in log output:\n%s", w, out)
				}
			}
			for _, nw := range tc.notWant {
				if strings.Contains(out, nw) {
					t.Errorf("unexpected %q in log output:\n%s", nw, out)
				}
			}
		})
	}
}

func TestReconcile_ClearsFileWhenNoServices(t *testing.T) {
	svc := makeSvc("default", "svc", "app.local", "10.0.0.1")
	reloader := &fakeReloader{}
	r, mgr := newReconciler(t, []*corev1.Service{svc}, reloader)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	r.Lister = &fakeServiceLister{svcs: []*corev1.Service{}}
	reloader.called = false

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reloader.called {
		t.Error("expected reload when block is cleared")
	}
	entries, err := mgr.ReadBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty block, got %v", entries)
	}
}
