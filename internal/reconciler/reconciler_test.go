package reconciler

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	corev1listers "k8s.io/client-go/listers/core/v1"

	"github.com/mm503/avahi-dns/internal/hostsfile"
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

func (f *fakeReloader) Reload() error {
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
type avahiReloader interface{ Reload() error }

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
