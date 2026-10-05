package server

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nixfleet/nixfleet/internal/inventory"
)

// stubHost registers a host whose base no base handler knows. BuildHost
// rejects it before touching the (nil, in tests) evaluator, so applyOne runs
// its real locking and fails fast at the build stage without any nix or SSH.
func stubHost(ts *TestServer, name string) *inventory.Host {
	host := &inventory.Host{Name: name, Addr: "10.0.0.9", SSHPort: 22, Base: "unsupported-in-tests"}
	ts.inventory.Hosts[name] = host
	return host
}

func TestApplyOneWaitsForTheHostLock(t *testing.T) {
	ts := newTestServer(t)
	host := stubHost(ts, "web9")

	// Stand in for an apply already in flight on web9.
	lock := ts.applyLock(host.Name)
	lock.Lock()

	done := make(chan error, 1)
	go func() {
		_, err := ts.applyOne(context.Background(), host)
		done <- err
	}()

	select {
	case err := <-done:
		lock.Unlock()
		t.Fatalf("applyOne ran while another apply held the host lock (err=%v)", err)
	case <-time.After(200 * time.Millisecond):
		// Correctly queued.
	}

	lock.Unlock()

	select {
	case err := <-done:
		if err == nil || !strings.HasPrefix(err.Error(), "build failed:") {
			t.Fatalf("err = %v, want a build failure once the lock was released", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("applyOne never proceeded after the host lock was released")
	}
}

func TestApplyOneDoesNotBlockOtherHosts(t *testing.T) {
	ts := newTestServer(t)
	busy := stubHost(ts, "web9")
	other := stubHost(ts, "web10")

	ts.applyLock(busy.Name).Lock()
	defer ts.applyLock(busy.Name).Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = ts.applyOne(context.Background(), other)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("apply on one host blocked behind an apply on a different host")
	}
}

// TestApplyLockRegistryUnderConcurrency exercises the lock registry itself:
// callers racing to register the same host must all end up on one mutex, so
// the critical section is never entered twice at once. Run under -race, this
// also covers concurrent reads and writes of the registry.
func TestApplyLockRegistryUnderConcurrency(t *testing.T) {
	ts := newTestServer(t)
	hosts := []*inventory.Host{stubHost(ts, "web9"), stubHost(ts, "web10")}

	var (
		mu       sync.Mutex
		inFlight = map[string]int{}
		maxSeen  = map[string]int{}
	)

	observe := func(name string, delta int) {
		mu.Lock()
		defer mu.Unlock()
		inFlight[name] += delta
		if inFlight[name] > maxSeen[name] {
			maxSeen[name] = inFlight[name]
		}
	}

	var wg sync.WaitGroup
	for i := range 40 {
		host := hosts[i%len(hosts)]
		wg.Add(1)
		go func() {
			defer wg.Done()

			// The same lock/observe/unlock shape applyOne uses.
			lock := ts.applyLock(host.Name)
			lock.Lock()
			observe(host.Name, 1)
			time.Sleep(time.Millisecond) // widen the window a real apply would hold
			observe(host.Name, -1)
			lock.Unlock()
		}()
	}
	wg.Wait()

	for _, host := range hosts {
		if maxSeen[host.Name] != 1 {
			t.Errorf("%s had %d concurrent applies, want at most 1", host.Name, maxSeen[host.Name])
		}
	}
}

func TestApplyLockIsPerHostAndStable(t *testing.T) {
	ts := newTestServer(t)

	if a, b := ts.applyLock("web1"), ts.applyLock("web1"); a != b {
		t.Error("applyLock returned two different mutexes for the same host")
	}
	if a, b := ts.applyLock("web1"), ts.applyLock("db1"); a == b {
		t.Error("applyLock shared one mutex across hosts; applies would serialize fleet-wide")
	}
}

// TestRunApplyJobWaitsForTheHostLock covers the API entry point: a second
// POST /api/hosts/{name}/apply must queue behind the first.
func TestRunApplyJobWaitsForTheHostLock(t *testing.T) {
	ts := newTestServer(t)
	host := stubHost(ts, "web9")

	lock := ts.applyLock(host.Name)
	lock.Lock()

	job := ts.createJob("apply", host.Name)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ts.runApplyJob(context.Background(), job, host)
	}()

	time.Sleep(200 * time.Millisecond)

	ts.jobsMu.RLock()
	status := job.Status
	ts.jobsMu.RUnlock()
	if status == "failed" || status == "completed" {
		lock.Unlock()
		t.Fatalf("job reached %q while another apply held the host lock", status)
	}

	lock.Unlock()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("apply job never finished after the host lock was released")
	}

	ts.jobsMu.RLock()
	defer ts.jobsMu.RUnlock()
	if job.Status != "failed" {
		t.Errorf("job status = %q, want %q", job.Status, "failed")
	}
	if !strings.Contains(job.Error, "build failed:") {
		t.Errorf("job error = %q, want a build failure", job.Error)
	}
}
