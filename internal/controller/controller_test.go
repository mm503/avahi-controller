package controller

import (
	"context"
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
