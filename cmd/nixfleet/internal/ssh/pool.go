package ssh

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Pool manages a pool of SSH connections
type Pool struct {
	entries     map[string]*poolEntry
	mu          sync.Mutex
	config      *ClientConfig
	maxIdle     time.Duration
	cleanupStop chan struct{}

	// dial is the connection constructor, swapped out in tests.
	dial func(ctx context.Context, host string, port int, user string) (*Client, error)
}

// poolEntry is a pool slot that may still be connecting. It is published to the
// map before the dial starts so concurrent callers for the same host find it
// and wait, rather than opening a second connection.
type poolEntry struct {
	ready  chan struct{} // closed once client/err are set; read them only after
	client *Client
	err    error
}

// PoolConfig holds configuration for the connection pool
type PoolConfig struct {
	ClientConfig *ClientConfig
	MaxIdleTime  time.Duration
}

// NewPool creates a new SSH connection pool
func NewPool(cfg *PoolConfig) *Pool {
	if cfg == nil {
		cfg = &PoolConfig{}
	}
	if cfg.ClientConfig == nil {
		cfg.ClientConfig = DefaultConfig()
	}
	if cfg.MaxIdleTime == 0 {
		cfg.MaxIdleTime = 5 * time.Minute
	}

	p := &Pool{
		entries:     make(map[string]*poolEntry),
		config:      cfg.ClientConfig,
		maxIdle:     cfg.MaxIdleTime,
		cleanupStop: make(chan struct{}),
	}
	p.dial = p.dialHost

	// Start background cleanup
	go p.cleanupLoop()

	return p
}

// Get returns an SSH client for the given host, creating one if necessary
func (p *Pool) Get(ctx context.Context, host string, port int) (*Client, error) {
	return p.GetWithUser(ctx, host, port, "")
}

// GetWithUser returns an SSH client for the given host with a specific user.
//
// Dialing happens outside the pool lock. Holding it across the dial meant one
// unreachable host stalled every other host's Get for the full 30s SSH
// timeout, which serialized a parallel fleet run behind its slowest member.
func (p *Pool) GetWithUser(ctx context.Context, host string, port int, user string) (*Client, error) {
	key := fmt.Sprintf("%s@%s:%d", user, host, port)
	if user == "" {
		key = fmt.Sprintf("%s:%d", host, port)
	}

	for {
		p.mu.Lock()
		entry, found := p.entries[key]
		if !found {
			entry = &poolEntry{ready: make(chan struct{})}
			p.entries[key] = entry
		}
		p.mu.Unlock()

		if !found {
			// We own this slot: dial unlocked, then publish the outcome.
			entry.client, entry.err = p.dial(ctx, host, port, user)
			close(entry.ready)
			if entry.err != nil {
				p.discard(key, entry)
				return nil, entry.err
			}
			return entry.client, nil
		}

		// Someone else is dialing, or already did.
		select {
		case <-entry.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}

		if entry.err != nil {
			// Share the failure instead of each waiter retrying in turn.
			p.discard(key, entry)
			return nil, entry.err
		}
		if entry.client.IsConnected() {
			return entry.client, nil
		}

		// Connection died since it was pooled: drop it and dial again.
		p.discard(key, entry)
	}
}

// dialHost opens a real connection. Called without the pool lock held.
func (p *Pool) dialHost(ctx context.Context, host string, port int, user string) (*Client, error) {
	cfg := *p.config
	cfg.Port = port
	if user != "" {
		cfg.User = user
	}

	client, err := NewClient(host, &cfg)
	if err != nil {
		return nil, fmt.Errorf("creating client for %s: %w", host, err)
	}

	if err := client.Connect(ctx); err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", host, err)
	}

	return client, nil
}

// discard removes an entry, but only if it is still the one in the map -- a
// newer dial for the same key must not be thrown away.
func (p *Pool) discard(key string, entry *poolEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.entries[key] == entry {
		delete(p.entries, key)
	}
}

// Close closes all connections in the pool
func (p *Pool) Close() error {
	close(p.cleanupStop)

	p.mu.Lock()
	defer p.mu.Unlock()

	var firstErr error
	for key, entry := range p.entries {
		if client := entry.settled(); client != nil {
			if err := client.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		delete(p.entries, key)
	}

	return firstErr
}

// settled returns the entry's client if the dial has finished successfully, and
// nil while it is still in flight -- reading the fields earlier would race.
func (e *poolEntry) settled() *Client {
	select {
	case <-e.ready:
		return e.client
	default:
		return nil
	}
}

// cleanupLoop periodically removes idle connections
func (p *Pool) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-p.cleanupStop:
			return
		case <-ticker.C:
			p.cleanupIdle()
		}
	}
}

func (p *Pool) cleanupIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for key, entry := range p.entries {
		client := entry.settled()
		if client == nil {
			continue // still dialing
		}

		client.mu.Lock()
		idle := now.Sub(client.lastUsed) > p.maxIdle
		client.mu.Unlock()

		if idle {
			client.Close()
			delete(p.entries, key)
		}
	}
}

// Remove removes and closes a specific connection from the pool
func (p *Pool) Remove(host string, port int) {
	key := fmt.Sprintf("%s:%d", host, port)

	p.mu.Lock()
	defer p.mu.Unlock()

	entry, ok := p.entries[key]
	if !ok {
		return
	}
	if client := entry.settled(); client != nil {
		client.Close()
	}
	delete(p.entries, key)
}

// Stats returns statistics about the pool
type PoolStats struct {
	TotalConnections  int
	ActiveConnections int
}

func (p *Pool) Stats() PoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()

	stats := PoolStats{
		TotalConnections: len(p.entries),
	}

	for _, entry := range p.entries {
		if client := entry.settled(); client != nil && client.IsConnected() {
			stats.ActiveConnections++
		}
	}

	return stats
}
