// Package events wraps the client-go EventRecorder for use by the avahi controller.
package events

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
)

// Recorder emits Kubernetes Events on behalf of the avahi controller.
type Recorder struct {
	recorder   record.EventRecorder
	broadcaster record.EventBroadcaster
}

// New creates a Recorder and starts broadcasting events to the Kubernetes API.
// Call Stop() when the controller exits.
func New(client kubernetes.Interface, nodeName string) *Recorder {
	broadcaster := record.NewBroadcaster()
	broadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{
		Interface: client.CoreV1().Events(""),
	})

	recorder := broadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{
		Component: "avahi-controller",
		Host:      nodeName,
	})

	return &Recorder{
		recorder:    recorder,
		broadcaster: broadcaster,
	}
}

// Stop shuts down the event broadcaster.
func (r *Recorder) Stop() {
	r.broadcaster.Shutdown()
}

// Warn emits a Warning event on the given Service.
func (r *Recorder) Warn(svc *corev1.Service, reason, message string) {
	r.recorder.Event(svc, corev1.EventTypeWarning, reason, message)
}

// Warnf emits a formatted Warning event on the given Service.
func (r *Recorder) Warnf(svc *corev1.Service, reason, format string, args ...any) {
	r.recorder.Event(svc, corev1.EventTypeWarning, reason, fmt.Sprintf(format, args...))
}
