package ssh

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// nopConn is the least an ssh.Conn can be. The pool's fake clients are only
// ever asked whether they are connected and then closed; anything else would
// mean real I/O, which these tests deliberately never do.
type nopConn struct{}

func (nopConn) User() string          { return "test" }
func (nopConn) SessionID() []byte     { return nil }
func (nopConn) ClientVersion() []byte { return nil }
func (nopConn) ServerVersion() []byte { return nil }
func (nopConn) RemoteAddr() net.Addr  { return nil }
func (nopConn) LocalAddr() net.Addr   { return nil }
func (nopConn) Close() error          { return nil }
func (nopConn) Wait() error           { return nil }

func (nopConn) SendRequest(string, bool, []byte) (bool, []byte, error) {
	return false, nil, nil
}

func (nopConn) OpenChannel(string, []byte) (ssh.Channel, <-chan *ssh.Request, error) {
	return nil, nil, nil
}

// fakeClient is a Client that reports connected without any real connection.
func fakeClient(host string, port int) *Client {
	return &Client{host: host, port: port, conn: &ssh.Client{Conn: nopConn{}}, lastUsed: time.Now()}
}

// testPool returns a pool whose dial is replaced by fn, with the background
// cleanup loop stopped so it can't race the test.
func testPool(t *testing.T, fn func(ctx context.Context, host string, port int, user string) (*Client, error)) *Pool {
	t.Helper()

	p := NewPool(nil)
	t.Cleanup(func() { _ = p.Close() })
	p.dial = fn
	return p
}

// TestPoolSlowDialDoesNotBlockOtherHosts is the regression test for the pool
// dialing while holding its write lock: one unreachable host used to stall
// every other host's Get for the whole SSH timeout.
func TestPoolSlowDialDoesNotBlockOtherHosts(t *testing.T) {
	release := make(chan struct{})
	dialing := make(chan struct{})

	p := testPool(t, func(ctx context.Context, host string, port int, user string) (*Client, error) {
		if host == "slow" {
			close(dialing)
			<-release
		}
		return fakeClient(host, port), nil
	})

	slowDone := make(chan struct{})
	// Unblock and reap the slow dial even if the assertion below fails, so a
	// failure is a 5s error and not a hung test.
	defer func() {
		close(release)
		<-slowDone
	}()

	go func() {
		defer close(slowDone)
		if _, err := p.Get(context.Background(), "slow", 22); err != nil {
			t.Errorf("slow Get: %v", err)
		}
	}()

	// Only ask for the other host once the slow dial is genuinely in flight.
	<-dialing

	fastDone := make(chan error, 1)
	go func() {
		_, err := p.Get(context.Background(), "fast", 22)
		fastDone <- err
	}()

	select {
	case err := <-fastDone:
		if err != nil {
			t.Fatalf("fast Get: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("Get for a healthy host blocked behind a slow dial to another host")
	}
}

// TestPoolOneDialPerHost checks the flip side: moving the dial out of the lock
// must not let concurrent callers open duplicate connections.
func TestPoolOneDialPerHost(t *testing.T) {
	var dials atomic.Int32
	gate := make(chan struct{})

	p := testPool(t, func(ctx context.Context, host string, port int, user string) (*Client, error) {
		dials.Add(1)
		<-gate // hold every caller inside the dial window
		return fakeClient(host, port), nil
	})

	const callers = 20
	var wg sync.WaitGroup
	clients := make([]*Client, callers)

	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client, err := p.Get(context.Background(), "web1", 22)
			if err != nil {
				t.Errorf("Get: %v", err)
				return
			}
			clients[i] = client
		}()
	}

	// Let the callers pile up on the entry, then let the single dial finish.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()

	if got := dials.Load(); got != 1 {
		t.Errorf("dialed %d times, want 1", got)
	}
	for i, client := range clients {
		if client == nil {
			t.Fatalf("caller %d got no client", i)
		}
		if client != clients[0] {
			t.Errorf("caller %d got a different client; connection was duplicated", i)
		}
	}
	if stats := p.Stats(); stats.TotalConnections != 1 {
		t.Errorf("pool holds %d connections, want 1", stats.TotalConnections)
	}
}

// TestPoolDifferentUsersGetDifferentConnections guards the cache key: the same
// host under two users is two connections.
func TestPoolDifferentUsersGetDifferentConnections(t *testing.T) {
	var dials atomic.Int32

	p := testPool(t, func(ctx context.Context, host string, port int, user string) (*Client, error) {
		dials.Add(1)
		return fakeClient(host, port), nil
	})

	for _, user := range []string{"", "deploy", "nixbot"} {
		if _, err := p.GetWithUser(context.Background(), "web1", 22, user); err != nil {
			t.Fatalf("GetWithUser(%q): %v", user, err)
		}
	}

	if got := dials.Load(); got != 3 {
		t.Errorf("dialed %d times, want 3 (one per user)", got)
	}
}

// TestPoolFailedDialIsNotCached makes sure a failure is reported and the slot
// freed, so a host that comes back is retried instead of failing forever.
func TestPoolFailedDialIsNotCached(t *testing.T) {
	var dials atomic.Int32
	wantErr := errors.New("connection refused")

	p := testPool(t, func(ctx context.Context, host string, port int, user string) (*Client, error) {
		if dials.Add(1) == 1 {
			return nil, wantErr
		}
		return fakeClient(host, port), nil
	})

	if _, err := p.Get(context.Background(), "web1", 22); !errors.Is(err, wantErr) {
		t.Fatalf("first Get error = %v, want %v", err, wantErr)
	}
	if stats := p.Stats(); stats.TotalConnections != 0 {
		t.Errorf("failed dial left %d entries in the pool", stats.TotalConnections)
	}
	if _, err := p.Get(context.Background(), "web1", 22); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if got := dials.Load(); got != 2 {
		t.Errorf("dialed %d times, want 2", got)
	}
}

// TestPoolWaiterRespectsContext: a caller waiting on someone else's in-flight
// dial must still honour its own deadline.
func TestPoolWaiterRespectsContext(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	p := testPool(t, func(ctx context.Context, host string, port int, user string) (*Client, error) {
		<-release
		return fakeClient(host, port), nil
	})

	go p.Get(context.Background(), "web1", 22) //nolint:errcheck // owns the dial
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if _, err := p.Get(ctx, "web1", 22); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("waiter error = %v, want %v", err, context.DeadlineExceeded)
	}
}

// TestPoolReplacesDeadConnection: a pooled client that has dropped is dialed
// again rather than handed back.
func TestPoolReplacesDeadConnection(t *testing.T) {
	var dials atomic.Int32

	p := testPool(t, func(ctx context.Context, host string, port int, user string) (*Client, error) {
		dials.Add(1)
		return fakeClient(host, port), nil
	})

	first, err := p.Get(context.Background(), "web1", 22)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	first.conn = nil // simulate the connection dropping

	second, err := p.Get(context.Background(), "web1", 22)
	if err != nil {
		t.Fatalf("Get after drop: %v", err)
	}
	if second == first {
		t.Error("pool returned the dead client")
	}
	if got := dials.Load(); got != 2 {
		t.Errorf("dialed %d times, want 2", got)
	}
}

// TestPoolConcurrentMixedTraffic is a race-detector workout: many hosts, many
// callers, interleaved with the maintenance paths that walk the same map.
func TestPoolConcurrentMixedTraffic(t *testing.T) {
	p := testPool(t, func(ctx context.Context, host string, port int, user string) (*Client, error) {
		return fakeClient(host, port), nil
	})

	hosts := []string{"web1", "web2", "db1", "db2"}
	var wg sync.WaitGroup

	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			host := hosts[i%len(hosts)]
			if _, err := p.Get(context.Background(), host, 22); err != nil {
				t.Errorf("Get(%s): %v", host, err)
			}
			p.Stats()
			if i%10 == 0 {
				p.Remove(host, 22)
			}
			p.cleanupIdle()
		}()
	}

	wg.Wait()
}
