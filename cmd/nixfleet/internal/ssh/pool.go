package ssh

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Pool manages a pool of SSH connections
type Pool struct {
	clients     map[string]*Client
	mu          sync.RWMutex
	config      *ClientConfig
	maxIdle     time.Duration
	cleanupStop chan struct{}
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
		clients:     make(map[string]*Client),
		config:      cfg.ClientConfig,
		maxIdle:     cfg.MaxIdleTime,
		cleanupStop: make(chan struct{}),
	}

	// Start background cleanup
	go p.cleanupLoop()

	return p
}

// Get returns an SSH client for the given host, creating one if necessary
func (p *Pool) Get(ctx context.Context, host string, port int) (*Client, error) {
	return p.GetWithUser(ctx, host, port, "")
}

// GetWithUser returns an SSH client for the given host with a specific user
func (p *Pool) GetWithUser(ctx context.Context, host string, port int, user string) (*Client, error) {
	key := fmt.Sprintf("%s@%s:%d", user, host, port)
	if user == "" {
		key = fmt.Sprintf("%s:%d", host, port)
	}

	// Try to get existing client
	p.mu.RLock()
	client, ok := p.clients[key]
	p.mu.RUnlock()

	if ok && client.IsConnected() {
		return client, nil
	}

	// Create new client
	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock
	if client, ok := p.clients[key]; ok && client.IsConnected() {
		return client, nil
	}

	// Create new config with specific port and user
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

	p.clients[key] = client
	return client, nil
}

// Close closes all connections in the pool
func (p *Pool) Close() error {
	close(p.cleanupStop)

	p.mu.Lock()
	defer p.mu.Unlock()

	var firstErr error
	for key, client := range p.clients {
		if err := client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(p.clients, key)
	}

	return firstErr
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
	for key, client := range p.clients {
		client.mu.Lock()
		idle := now.Sub(client.lastUsed) > p.maxIdle
		client.mu.Unlock()

		if idle {
			client.Close()
			delete(p.clients, key)
		}
	}
}

// Remove removes and closes a specific connection from the pool
func (p *Pool) Remove(host string, port int) {
	key := fmt.Sprintf("%s:%d", host, port)

	p.mu.Lock()
	defer p.mu.Unlock()

	if client, ok := p.clients[key]; ok {
		client.Close()
		delete(p.clients, key)
	}
}

// Stats returns statistics about the pool
type PoolStats struct {
	TotalConnections  int
	ActiveConnections int
}

func (p *Pool) Stats() PoolStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	stats := PoolStats{
		TotalConnections: len(p.clients),
	}

	for _, client := range p.clients {
		if client.IsConnected() {
			stats.ActiveConnections++
		}
	}

	return stats
}
