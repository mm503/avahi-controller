package events

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"testing"
)

func TestWarnf_FormatsWarningEvent(t *testing.T) {
	fake := record.NewFakeRecorder(1)
	recorder := &Recorder{recorder: fake}
	recorder.Warnf(&corev1.Service{}, "InvalidHostname", "invalid name %q", "bad!")
	select {
	case event := <-fake.Events:
		if event != `Warning InvalidHostname invalid name "bad!"` {
			t.Fatalf("event: %s", event)
		}
	default:
		t.Fatal("event was not emitted")
	}
}
