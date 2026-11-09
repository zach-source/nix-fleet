package server

import (
	"context"
	"testing"
	"time"
)

func TestJobAdmissionAndRetentionLimits(t *testing.T) {
	s := newTestServer(t)
	s.config.MaxActiveJobs = 1
	s.config.MaxCompletedJobs = 1
	release := make(chan struct{})
	first, err := s.startJob("fixture", "", func(ctx context.Context, j *Job) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		s.updateJob(j, "completed", nil, "")
	})
	if err != nil {
		t.Fatal(err)
	}
	_, rejected := s.startJob("excess", "", func(context.Context, *Job) {})
	close(release)
	s.jobsWG.Wait()
	if rejected == nil {
		t.Fatal("over-capacity job accepted")
	}
	last, err := s.startJob("last", "", func(_ context.Context, j *Job) { s.updateJob(j, "completed", nil, "") })
	if err != nil {
		t.Fatal(err)
	}
	s.jobsWG.Wait()
	if len(s.jobs) != 1 || s.jobs[first.ID] != nil || s.jobs[last.ID] == nil {
		t.Fatalf("retention failed: %v", s.jobs)
	}
}
func TestJobTimeoutIsTerminal(t *testing.T) {
	s := newTestServer(t)
	s.config.JobTimeout = 10 * time.Millisecond
	done := make(chan struct{})
	finished := make(chan struct{})
	job, err := s.startJob("fixture", "", func(ctx context.Context, j *Job) {
		select {
		case <-ctx.Done():
		case <-done:
		}
		close(finished)
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
		s.jobsWG.Wait()
	case <-time.After(time.Second):
	}

	s.jobsMu.RLock()
	status := s.jobs[job.ID].Status
	s.jobsMu.RUnlock()
	close(done)
	s.jobsWG.Wait()
	if status != "failed" {
		t.Fatalf("timeout status: %s", status)
	}
}
