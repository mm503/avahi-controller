// Package controller wires the Kubernetes Service informer to the reconciler
// via a rate-limited work queue.
package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/mm503/avahi-controller/internal/reconciler"
)

// sentinelKey is the single work-queue key used for all events.
// All add/update/delete events collapse to one reconcile pass.
const sentinelKey = "reconcile"

// Controller drives reconciliation from Kubernetes Service events.
type Controller struct {
	informer    cache.SharedIndexInformer
	queue       workqueue.RateLimitingInterface
	reconciler  *reconciler.Reconciler
	initialDone atomic.Bool
}

// New creates a Controller. Call Run() to start it.
func New(
	informer cache.SharedIndexInformer,
	rec *reconciler.Reconciler,
) *Controller {
	c := &Controller{
		informer: informer,
		queue: workqueue.NewRateLimitingQueueWithConfig(
			workqueue.NewItemExponentialFailureRateLimiter(100*time.Millisecond, 30*time.Second),
			workqueue.RateLimitingQueueConfig{Name: "avahi-controller"},
		),
		reconciler: rec,
	}

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onAdd,
		UpdateFunc: c.onUpdate,
		DeleteFunc: c.onDelete,
	})

	return c
}

func (c *Controller) onAdd(obj any) {
	if svc, ok := obj.(*corev1.Service); ok {
		slog.Debug("service added", "service", svc.Namespace+"/"+svc.Name)
	}
	c.queue.Add(sentinelKey)
}

func (c *Controller) onUpdate(_, newObj any) {
	if svc, ok := newObj.(*corev1.Service); ok {
		slog.Debug("service updated", "service", svc.Namespace+"/"+svc.Name)
	}
	c.queue.Add(sentinelKey)
}

func (c *Controller) onDelete(obj any) {
	svc, ok := obj.(*corev1.Service)
	if !ok {
		// Handle tombstone (object deleted while controller was down).
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			slog.Error("could not get object from tombstone", "type", fmt.Sprintf("%T", obj))
			return
		}
		svc, ok = tombstone.Obj.(*corev1.Service)
		if !ok {
			slog.Error("tombstone contained non-Service object", "type", fmt.Sprintf("%T", tombstone.Obj))
			return
		}
	}
	slog.Info("service deleted", "service", svc.Namespace+"/"+svc.Name)
	c.queue.Add(sentinelKey)
}

// Run starts the informer, waits for cache sync, triggers an initial reconcile,
// then processes work items until ctx is cancelled.
func (c *Controller) Run(ctx context.Context) error {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	slog.Info("waiting for cache sync")
	if !cache.WaitForCacheSync(ctx.Done(), c.informer.HasSynced) {
		return fmt.Errorf("timed out waiting for cache sync")
	}
	slog.Info("cache synced")

	// Trigger startup reconcile immediately.
	c.queue.Add(sentinelKey)

	// Run the worker loop until context is cancelled.
	go wait.UntilWithContext(ctx, c.runWorker, time.Second)

	<-ctx.Done()
	slog.Info("controller shutting down")
	return nil
}

func (c *Controller) runWorker(ctx context.Context) {
	for c.processNextItem(ctx) {
	}
}

func (c *Controller) processNextItem(ctx context.Context) bool {
	item, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(item)

	err := c.reconciler.Reconcile(ctx)
	if err == nil {
		if c.initialDone.CompareAndSwap(false, true) {
			slog.Info("controller ready")
		}
		c.queue.Forget(item)
		return true
	}

	// ErrMissingIP is expected while MetalLB is assigning an IP.
	if errors.Is(err, reconciler.ErrMissingIP) {
		slog.Debug("requeuing, waiting for IP assignment")
	} else {
		slog.Error("reconcile failed, requeuing with backoff", "error", err)
	}
	c.queue.AddRateLimited(item)
	return true
}
