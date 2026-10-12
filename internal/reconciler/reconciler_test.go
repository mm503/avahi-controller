package reconciler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/record"

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

func newReconciler(t *testing.T, svcs []*corev1.Service) (*Reconciler, *hostsfile.Manager) {
	t.Helper()
	dir := t.TempDir()
	mgr := &hostsfile.Manager{FilePath: filepath.Join(dir, "hosts")}
	rec := &Reconciler{
		Lister:   &fakeServiceLister{svcs: svcs},
		HostsMgr: mgr,
		Recorder: nil,
	}
	return rec, mgr
}

// forbidHostsWrites makes any later attempt to open the hosts file for writing
// fail. avahi-daemon reloads whenever a file opened for writing is closed
// (inotify IN_CLOSE_WRITE), even if nothing changed, so unchanged state must
// not open the file for writing at all.
func forbidHostsWrites(t *testing.T, path string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
}

// --- buildDesiredEntries tests ---

func TestBuildDesiredEntries_SkipsNonLoadBalancer(t *testing.T) {
	svc := makeSvc("default", "svc", "app.local", "10.0.0.1")
	svc.Spec.Type = corev1.ServiceTypeClusterIP

	r, _ := newReconciler(t, []*corev1.Service{svc})
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

	r, _ := newReconciler(t, []*corev1.Service{svc})
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

	r, _ := newReconciler(t, []*corev1.Service{svc})
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
	r, _ := newReconciler(t, []*corev1.Service{svc})

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
	r, _ := newReconciler(t, []*corev1.Service{svc})
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

	r, _ := newReconciler(t, []*corev1.Service{svc})
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

	r, _ := newReconciler(t, []*corev1.Service{svc1, svc2})
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

	r, _ := newReconciler(t, []*corev1.Service{svc1, svc2})
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
		r, _ := newReconciler(t, svcs)
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
		"192.168.1.1", // IP-shaped value: matches the hostname pattern but is surely a mistake
	}

	for _, hostname := range invalid {
		svc := makeSvc("default", "svc", hostname, "10.0.0.1")
		r, _ := newReconciler(t, []*corev1.Service{svc})
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
		r, _ := newReconciler(t, []*corev1.Service{svc})
		entries, _, err := r.buildDesiredEntries()
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Errorf("hostname %q should be accepted, got entries %v", hostname, entries)
		}
	}
}

func TestBuildDesiredEntries_WarnsOnceForNonLocalHostname(t *testing.T) {
	svc := makeSvc("default", "svc", "app.example.com", "10.0.0.1")
	r, _ := newReconciler(t, []*corev1.Service{svc})

	buf := captureLog(t)
	entries, _, err := r.buildDesiredEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("non-.local hostname should still be accepted, got %v", entries)
	}
	if !strings.Contains(buf.String(), "does not end in .local") {
		t.Errorf("expected non-.local warning on first pass, got:\n%s", buf.String())
	}

	// Second pass must not repeat the warning.
	buf.Reset()
	if _, _, err := r.buildDesiredEntries(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "does not end in .local") {
		t.Errorf("warning should fire only once, got:\n%s", buf.String())
	}
}

// --- Reconcile integration ---

func TestReconcile_WritesFileOnFirstRun(t *testing.T) {
	svc := makeSvc("default", "svc", "app.local", "10.0.0.1")
	r, mgr := newReconciler(t, []*corev1.Service{svc})

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entries, err := mgr.ReadBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].IP != "10.0.0.1" {
		t.Errorf("unexpected entries in file: %v", entries)
	}
}

func TestReconcile_SkipsWriteWhenUnchanged(t *testing.T) {
	svc := makeSvc("default", "svc", "app.local", "10.0.0.1")
	r, mgr := newReconciler(t, []*corev1.Service{svc})

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	forbidHostsWrites(t, mgr.FilePath)

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("unchanged state opened the hosts file for writing: %v", err)
	}
}

func TestReconcile_RequeueOnMissingIP(t *testing.T) {
	svc := makeSvc("default", "svc", "app.local", "")
	r, _ := newReconciler(t, []*corev1.Service{svc})

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

func TestReconcile_LogsStartupInventoryOnce(t *testing.T) {
	svc := makeSvc("default", "svc", "app.local", "10.0.0.1")
	r, mgr := newReconciler(t, []*corev1.Service{svc})

	// Pre-populate the hosts file, simulating state left by a previous pod.
	if err := mgr.WriteBlock([]hostsfile.HostEntry{
		{IP: "10.0.0.1", Hostname: "app.local"},
		{IP: "10.0.0.2", Hostname: "other.local"},
	}); err != nil {
		t.Fatal(err)
	}

	buf := captureLog(t)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, w := range []string{"existing DNS record", "app.local", "10.0.0.1", "other.local", "10.0.0.2"} {
		if !strings.Contains(out, w) {
			t.Errorf("expected %q in startup log output:\n%s", w, out)
		}
	}

	// Second pass must not repeat the inventory.
	buf.Reset()
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "existing DNS record") {
		t.Errorf("startup inventory should log only once, got:\n%s", buf.String())
	}
}

func TestReconcile_LogsEmptyStartupInventory(t *testing.T) {
	r, _ := newReconciler(t, nil)

	buf := captureLog(t)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "startup: existing DNS records in hosts file") {
		t.Errorf("expected startup summary even with empty hosts file, got:\n%s", buf.String())
	}
}

func TestReconcile_ClearsFileWhenNoServices(t *testing.T) {
	svc := makeSvc("default", "svc", "app.local", "10.0.0.1")
	r, mgr := newReconciler(t, []*corev1.Service{svc})

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	r.Lister = &fakeServiceLister{svcs: []*corev1.Service{}}

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := mgr.ReadBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty block, got %v", entries)
	}
}

func TestReconcile_CancelledDoesNotWrite(t *testing.T) {
	r, mgr := newReconciler(t, []*corev1.Service{makeSvc("default", "nas", "nas.local", "10.0.0.1")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Reconcile(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	entries, err := mgr.ReadBlock()
	if err != nil || len(entries) != 0 {
		t.Fatalf("wrote entries: %v, %v", entries, err)
	}
}

func TestReconcile_StaticOnlyFileNoServicesNotOpenedForWrite(t *testing.T) {
	r, mgr := newReconciler(t, nil)
	original := []byte("127.0.0.1 localhost\n")
	if err := os.WriteFile(mgr.FilePath, original, 0644); err != nil {
		t.Fatal(err)
	}
	forbidHostsWrites(t, mgr.FilePath)
	for i := 0; i < 3; i++ {
		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("unchanged static file opened for writing: %v", err)
		}
	}
	data, err := os.ReadFile(mgr.FilePath)
	if err != nil || string(data) != string(original) {
		t.Fatalf("static file changed: %q %v", data, err)
	}
}

func TestReconcile_PendingServiceDoesNotBlockReadyEntries(t *testing.T) {
	r, mgr := newReconciler(t, []*corev1.Service{
		makeSvc("default", "pending", "pending.local", ""),
		makeSvc("default", "ready", "ready.local", "10.0.0.1"),
	})
	if err := r.Reconcile(context.Background()); !errors.Is(err, ErrMissingIP) {
		t.Fatalf("got %v", err)
	}
	entries, err := mgr.ReadBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Hostname != "ready.local" {
		t.Fatalf("ready entries missing: %v", entries)
	}
}

type fakeWarnings struct{ recorder *record.FakeRecorder }

func (f fakeWarnings) Warnf(obj *corev1.Service, reason, format string, args ...any) {
	f.recorder.Eventf(obj, corev1.EventTypeWarning, reason, format, args...)
}

func TestBuildDesiredEntries_EmitsWarnings(t *testing.T) {
	cases := []struct {
		name, reason string
		services     []*corev1.Service
		passes       int
	}{
		{"invalid", "InvalidHostname", []*corev1.Service{makeSvc("default", "bad", "bad!", "10.0.0.1")}, 1},
		{"conflict", "HostnameConflict", []*corev1.Service{makeSvc("default", "a", "nas.local", "10.0.0.1"), makeSvc("default", "b", "NAS.local", "10.0.0.2")}, 1},
		{"nonlocal once", "NonLocalHostname", []*corev1.Service{makeSvc("default", "nas", "nas.example", "10.0.0.1")}, 3},
		{"pending threshold once", "PendingLoadBalancerIP", []*corev1.Service{makeSvc("default", "nas", "nas.local", "")}, pendingIPEventThreshold + 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newReconciler(t, tc.services)
			fake := record.NewFakeRecorder(100)
			r.Recorder = fakeWarnings{fake}
			for i := 0; i < tc.passes; i++ {
				if _, _, err := r.buildDesiredEntries(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case event := <-fake.Events:
				if !strings.HasPrefix(event, "Warning "+tc.reason+" ") {
					t.Fatalf("event: %s", event)
				}
			default:
				t.Fatal("warning never emitted")
			}
			select {
			case event := <-fake.Events:
				t.Fatalf("duplicate warning: %s", event)
			default:
			}
		})
	}
}

func TestReconcile_MalformedBlockLeavesFileUntouched(t *testing.T) {
	r, mgr := newReconciler(t, []*corev1.Service{makeSvc("default", "nas", "nas.local", "10.0.0.1")})
	original := "### BEGIN k8s-avahi-controller ###\nmissing end\n"
	if err := os.WriteFile(mgr.FilePath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(context.Background()); !errors.Is(err, hostsfile.ErrMalformedBlock) {
		t.Fatalf("got %v", err)
	}
	actual, err := os.ReadFile(mgr.FilePath)
	if err != nil || string(actual) != original {
		t.Fatalf("file modified: %q %v", actual, err)
	}
}

func TestReconcile_ReturnsWriteError(t *testing.T) {
	r, mgr := newReconciler(t, []*corev1.Service{makeSvc("default", "nas", "nas.local", "10.0.0.1")})
	mgr.FilePath = filepath.Join(mgr.FilePath, "missing", "hosts")
	if err := r.Reconcile(context.Background()); err == nil || !strings.Contains(err.Error(), "write hosts block") {
		t.Fatalf("got %v", err)
	}
}

func TestValidHostname_MaxLength253(t *testing.T) {
	prefix := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "."
	if !validHostname(prefix + strings.Repeat("d", 61)) {
		t.Fatal("253-character hostname rejected")
	}
	if validHostname(prefix + strings.Repeat("d", 62)) {
		t.Fatal("254-character hostname accepted")
	}
}

// hostnameOfLength returns a valid hostname of exactly n bytes (n <= 255).
func hostnameOfLength(n int) string {
	return (strings.Repeat(strings.Repeat("a", 63)+".", 3) + strings.Repeat("b", 63))[:n]
}

func TestBuildDesiredEntries_SkipsLinesAvahiCannotRead(t *testing.T) {
	fits := makeSvc("default", "fits", hostnameOfLength(hostsfile.MaxLineLength-len("10.0.0.1 ")), "10.0.0.1")
	tooLong := makeSvc("default", "long", hostnameOfLength(hostsfile.MaxLineLength-len("10.0.0.2 ")+1), "10.0.0.2")
	r, _ := newReconciler(t, []*corev1.Service{fits, tooLong})
	fake := record.NewFakeRecorder(10)
	r.Recorder = fakeWarnings{fake}

	entries, _, err := r.buildDesiredEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].IP != "10.0.0.1" || len(entries[0].Line()) != hostsfile.MaxLineLength {
		t.Fatalf("want only the %d-byte line, got %v", hostsfile.MaxLineLength, entries)
	}
	close(fake.Events)
	tooLongEvents := 0
	for event := range fake.Events {
		if strings.HasPrefix(event, "Warning HostsLineTooLong ") {
			tooLongEvents++
		}
	}
	if tooLongEvents != 1 {
		t.Fatalf("HostsLineTooLong events: %d", tooLongEvents)
	}
}
