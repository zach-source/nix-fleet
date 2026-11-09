package server

import (
	"context"
	"testing"
	"time"
)

func TestCanceledApplyDoesNotWaitForHostMutex(t *testing.T) {
	s := newTestServer(t)
	host := stubHost(s, "web9")
	lock := s.applyLock(host.Name)
	lock.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { _, err := s.applyOne(ctx, host); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("canceled apply succeeded")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("canceled apply still waits for host mutex")
	}
	lock.Unlock()
}

func TestCloseCancelsAndJoinsAcceptedJobs(t *testing.T) {
	s := newTestServer(t)
	finished := make(chan struct{})
	_, err := s.startJob("fixture", "", func(ctx context.Context, j *Job) {
		<-ctx.Done()
		s.updateJob(j, "failed", nil, ctx.Err().Error())
		close(finished)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("Close returned before accepted job stopped")
	}
	if _, err := s.startJob("late", "", func(context.Context, *Job) { t.Error("late job ran") }); err == nil {
		t.Fatal("accepted job after Close")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
