package controller

import (
	"context"
	"fmt"
	"github.com/mm503/avahi-controller/internal/hostsfile"
	"github.com/mm503/avahi-controller/internal/reconciler"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	corelisters "k8s.io/client-go/listers/core/v1"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

func newTestController() *Controller {
	return &Controller{
		queue: workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()),
	}
}

func makeSvc(ns, name string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
}

// --- Run ---

func TestRun_ReturnsNilWhenCancelledDuringCacheSync(t *testing.T) {
	// Informer is never started, so HasSynced stays false and Run blocks in
	// WaitForCacheSync until the context is cancelled.
	informer := cache.NewSharedIndexInformer(&cache.ListWatch{}, &corev1.Service{}, 0, cache.Indexers{})
	c, err := New(informer, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("shutdown during cache sync should be clean, got error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

// --- onAdd / onUpdate ---

func TestOnAdd_EnqueuesItem(t *testing.T) {
	c := newTestController()
	c.onAdd(makeSvc("default", "svc"))
	if c.queue.Len() != 1 {
		t.Errorf("expected 1 item in queue, got %d", c.queue.Len())
	}
}

func TestOnUpdate_EnqueuesItem(t *testing.T) {
	c := newTestController()
	c.onUpdate(nil, makeSvc("default", "svc"))
	if c.queue.Len() != 1 {
		t.Errorf("expected 1 item in queue, got %d", c.queue.Len())
	}
}

// --- onDelete ---

func TestOnDelete_ValidService(t *testing.T) {
	c := newTestController()
	c.onDelete(makeSvc("default", "svc"))
	if c.queue.Len() != 1 {
		t.Errorf("expected 1 item in queue, got %d", c.queue.Len())
	}
}

func TestOnDelete_Tombstone(t *testing.T) {
	c := newTestController()
	tombstone := cache.DeletedFinalStateUnknown{
		Key: "default/svc",
		Obj: makeSvc("default", "svc"),
	}
	c.onDelete(tombstone)
	if c.queue.Len() != 1 {
		t.Errorf("expected 1 item in queue, got %d", c.queue.Len())
	}
}

func TestOnDelete_NonServiceObject_DoesNotEnqueue(t *testing.T) {
	c := newTestController()
	c.onDelete("not-a-service")
	if c.queue.Len() != 0 {
		t.Errorf("expected 0 items in queue for unrecognised object, got %d", c.queue.Len())
	}
}

func TestOnDelete_TombstoneWithNonServiceObject_DoesNotEnqueue(t *testing.T) {
	c := newTestController()
	tombstone := cache.DeletedFinalStateUnknown{Key: "x", Obj: "not-a-service"}
	c.onDelete(tombstone)
	if c.queue.Len() != 0 {
		t.Errorf("expected 0 items in queue for invalid tombstone object, got %d", c.queue.Len())
	}
}

func TestRun_ErrorsWhenCacheNeverSyncs(t *testing.T) {
	informer := cache.NewSharedIndexInformer(&cache.ListWatch{}, &corev1.Service{}, 0, cache.Indexers{})
	c, err := New(informer, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.SyncTimeout = 10 * time.Millisecond
	if err := c.Run(context.Background()); err == nil {
		t.Fatal("expected sync deadline error")
	}
}

func TestHealth_StaleAfterRepeatedFailures(t *testing.T) {
	c := newTestController()
	c.HealthTimeout = time.Minute
	check := func(want int) {
		t.Helper()
		w := httptest.NewRecorder()
		c.Health(w, httptest.NewRequest("GET", "/healthz", nil))
		if w.Code != want {
			t.Fatalf("status %d, want %d", w.Code, want)
		}
	}
	check(http.StatusServiceUnavailable)
	c.markReady()
	check(http.StatusOK)
	c.lastSuccess.Store(time.Now().Add(-2 * time.Minute).UnixNano())
	check(http.StatusServiceUnavailable)
	c.markReady()
	check(http.StatusOK)
}

func workerFixture(t *testing.T) (*Controller, *hostsfile.Manager) {
	t.Helper()
	informer := cache.NewSharedIndexInformer(&cache.ListWatch{}, &corev1.Service{}, 0, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	mgr := &hostsfile.Manager{FilePath: filepath.Join(t.TempDir(), "hosts")}
	rec := &reconciler.Reconciler{Lister: corelisters.NewServiceLister(informer.GetIndexer()), HostsMgr: mgr}
	c, err := New(informer, rec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.queue.ShutDown)
	return c, mgr
}

func TestProcessNextItem_FailureRequeuesWithBackoff(t *testing.T) {
	c, mgr := workerFixture(t)
	if err := os.Mkdir(mgr.FilePath, 0755); err != nil {
		t.Fatal(err)
	}
	c.queue.Add(sentinelKey)
	start := time.Now()
	if !c.processNextItem(context.Background()) {
		t.Fatal("worker stopped")
	}
	if c.queue.NumRequeues(sentinelKey) != 1 {
		t.Fatal("failed item was not rate limited")
	}
	retried := make(chan string, 1)
	go func() {
		item, quit := c.queue.Get()
		if !quit {
			c.queue.Done(item)
			retried <- item
		}
	}()
	select {
	case item := <-retried:
		if item != sentinelKey || time.Since(start) < 80*time.Millisecond {
			t.Fatal("retry did not use production backoff")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failed reconcile was never retried")
	}
}

func TestProcessNextItem_SuccessForgets(t *testing.T) {
	c, _ := workerFixture(t)
	c.queue.AddRateLimited(sentinelKey)
	if !c.processNextItem(context.Background()) {
		t.Fatal("worker stopped")
	}
	if c.queue.NumRequeues(sentinelKey) != 0 {
		t.Fatal("successful item retained backoff")
	}
	if !c.initialDone.Load() || c.lastSuccess.Load() == 0 {
		t.Fatal("success did not mark ready")
	}
}

func TestProcessNextItem_RepeatedFailureEmitsOneWarning(t *testing.T) {
	c, mgr := workerFixture(t)
	if err := os.Mkdir(mgr.FilePath, 0755); err != nil {
		t.Fatal(err)
	}
	warnings := 0
	c.FailureEvent = func(error) { warnings++ }
	for i := 0; i < 4; i++ {
		c.queue.Add(sentinelKey)
		c.processNextItem(context.Background())
	}
	if warnings != 1 {
		t.Fatalf("warnings: %d", warnings)
	}
}

type blockingReloader struct{ started, release chan struct{} }

func (r *blockingReloader) Reload(context.Context) error { close(r.started); <-r.release; return nil }

func TestRun_WaitsForInFlightReconcile(t *testing.T) {
	svc := makeSvc("default", "nas")
	svc.Spec.Type = corev1.ServiceTypeLoadBalancer
	svc.Annotations = map[string]string{"avahi.homelab/hostname": "nas.local"}
	svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "10.0.0.1"}}
	client := fake.NewClientset(svc)
	factory := informers.NewSharedInformerFactory(client, time.Minute)
	informer := factory.Core().V1().Services()
	reload := &blockingReloader{started: make(chan struct{}), release: make(chan struct{})}
	rec := &reconciler.Reconciler{Lister: informer.Lister(), HostsMgr: &hostsfile.Manager{FilePath: filepath.Join(t.TempDir(), "hosts")}, Reloader: reload}
	c, err := New(informer.Informer(), rec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	released := false
	defer func() {
		if !released {
			close(reload.release)
		}
	}()
	factory.Start(ctx.Done())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	select {
	case <-reload.started:
	case <-time.After(5 * time.Second):
		t.Fatal("reconcile never started")
	}
	// Re-dirty the item while the first pass is blocked, then cancel.
	c.queue.Add(sentinelKey)
	cancel()
	select {
	case err := <-done:
		t.Fatalf("Run returned before reconcile finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(reload.release)
	released = true
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestProcessNextItem_MalformedMarkersWaitForChange(t *testing.T) {
	c, mgr := workerFixture(t)
	if err := os.WriteFile(mgr.FilePath, []byte("### BEGIN k8s-avahi-controller ###\n"), 0644); err != nil {
		t.Fatal(err)
	}
	warnings := 0
	c.FailureEvent = func(error) { warnings++ }
	c.queue.Add(sentinelKey)
	c.processNextItem(context.Background())
	if c.queue.NumRequeues(sentinelKey) != 0 || c.queue.Len() != 0 || warnings != 1 {
		t.Fatalf("retries=%d queue=%d warnings=%d", c.queue.NumRequeues(sentinelKey), c.queue.Len(), warnings)
	}
	previous, err := os.Stat(mgr.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.watchFile(ctx, previous); close(done) }()
	if err := os.WriteFile(mgr.FilePath, []byte("# repaired\n"), 0644); err != nil {
		t.Fatal(err)
	}
	itemReady := make(chan string, 1)
	go func() {
		item, quit := c.queue.Get()
		if !quit {
			c.queue.Done(item)
			itemReady <- item
		}
	}()
	select {
	case item := <-itemReady:
		if item != sentinelKey {
			t.Fatal(item)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("file repair did not enqueue reconcile")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watcher did not stop")
	}
}

func TestMalformedMarkers_StayLiveButUnreadyUntilRepaired(t *testing.T) {
	for _, previouslyReady := range []bool{false, true} {
		t.Run(fmt.Sprintf("previouslyReady=%v", previouslyReady), func(t *testing.T) {
			c, mgr := workerFixture(t)
			if previouslyReady {
				c.markReady()
			}
			if err := os.WriteFile(mgr.FilePath, []byte("### BEGIN k8s-avahi-controller ###\n"), 0644); err != nil {
				t.Fatal(err)
			}
			c.queue.Add(sentinelKey)
			c.processNextItem(context.Background())
			check := func(handler http.HandlerFunc, want int) {
				t.Helper()
				w := httptest.NewRecorder()
				handler(w, httptest.NewRequest("GET", "/", nil))
				if w.Code != want {
					t.Fatalf("status %d, want %d", w.Code, want)
				}
			}
			check(c.Health, http.StatusOK)
			check(c.Ready, http.StatusServiceUnavailable)
			c.lastSuccess.Store(time.Now().Add(-time.Hour).UnixNano())
			check(c.Health, http.StatusOK)
			check(c.Ready, http.StatusServiceUnavailable)

			// A different file error must restore normal liveness failure handling.
			if err := os.Remove(mgr.FilePath); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(mgr.FilePath, 0755); err != nil {
				t.Fatal(err)
			}
			c.queue.Add(sentinelKey)
			c.processNextItem(context.Background())
			check(c.Health, http.StatusServiceUnavailable)
			check(c.Ready, http.StatusServiceUnavailable)

			if err := os.Remove(mgr.FilePath); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(mgr.FilePath, []byte("# repaired\n"), 0644); err != nil {
				t.Fatal(err)
			}
			c.queue.Add(sentinelKey)
			c.processNextItem(context.Background())
			check(c.Health, http.StatusOK)
			check(c.Ready, http.StatusOK)
		})
	}
}
