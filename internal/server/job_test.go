package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"homelab-deploy/internal/report"
)

func TestJobCancel_CancelsContextAndReturnsTrueWhileRunning(t *testing.T) {
	j := newJob("id1", "apply", []string{"a"}, true)
	select {
	case <-j.ctx.Done():
		t.Fatal("context should not be done before Cancel is called")
	default:
	}

	if !j.Cancel() {
		t.Fatal("Cancel should return true for a still-running job")
	}
	select {
	case <-j.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("expected the job's context to be canceled")
	}
	if !j.Snapshot().Canceled {
		t.Error("expected Snapshot().Canceled to be true after Cancel")
	}
}

func TestJobCancel_IsIdempotentWhileRunning(t *testing.T) {
	j := newJob("id2", "plan", nil, true)
	if !j.Cancel() {
		t.Fatal("first Cancel should return true")
	}
	if !j.Cancel() {
		t.Error("a second Cancel while still running should also return true — safe to call more than once")
	}
}

func TestJobCancel_ReturnsFalseOnceAlreadyFinished(t *testing.T) {
	j := newJob("id3", "plan", nil, true)
	j.finish([]report.Entry{{Dir: "a", Status: report.StatusOK}}, nil)

	if j.Cancel() {
		t.Error("Cancel on an already-finished job should return false — nothing to cancel")
	}
	if j.Snapshot().Canceled {
		t.Error("a job that finished naturally, then had Cancel called on it, should not retroactively be marked canceled")
	}
}

func TestJobFinish_ReleasesContextEvenWithoutExplicitCancel(t *testing.T) {
	j := newJob("id4", "plan", nil, true)
	j.finish(nil, nil)
	select {
	case <-j.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("finish should release the job's context (call cancelFunc) even when Cancel was never called")
	}
}

func TestJobFinish_PublishesCanceledFlagInJobDoneEvent(t *testing.T) {
	j := newJob("id5", "apply", []string{"a"}, true)
	_, live := j.broadcaster.Subscribe()
	defer j.broadcaster.Unsubscribe(live)

	j.Cancel()
	j.finish(nil, context.Canceled)

	select {
	case frame := <-live:
		if !strings.Contains(string(frame), `"canceled":true`) {
			t.Errorf("expected job_done frame to report canceled:true, got %s", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for job_done frame")
	}
}
