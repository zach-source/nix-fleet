package ssh

import (
	"context"
	"fmt"
	"sync"

	"github.com/nixfleet/nixfleet/internal/inventory"
)

// HostResult holds the result of an operation on a single host
type HostResult struct {
	Host    *inventory.Host
	Result  *ExecResult
	Error   error
	Success bool
}

// Executor runs commands across multiple hosts in parallel
type Executor struct {
	pool        *Pool
	maxParallel int
}

// NewExecutor creates a new parallel executor
func NewExecutor(pool *Pool, maxParallel int) *Executor {
	if maxParallel <= 0 {
		maxParallel = 5
	}
	return &Executor{
		pool:        pool,
		maxParallel: maxParallel,
	}
}

// ExecOnHosts executes a command on multiple hosts in parallel
func (e *Executor) ExecOnHosts(ctx context.Context, hosts []*inventory.Host, cmd string, sudo bool) []HostResult {
	results := make([]HostResult, len(hosts))

	// Use semaphore for concurrency control
	sem := make(chan struct{}, e.maxParallel)
	var wg sync.WaitGroup

	for i, host := range hosts {
		wg.Add(1)
		go func(idx int, h *inventory.Host) {
			defer wg.Done()

			// Acquire semaphore
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[idx] = HostResult{
					Host:  h,
					Error: ctx.Err(),
				}
				return
			}

			result := e.execOnHost(ctx, h, cmd, sudo)
			results[idx] = result
		}(i, host)
	}

	wg.Wait()
	return results
}

func (e *Executor) execOnHost(ctx context.Context, host *inventory.Host, cmd string, sudo bool) HostResult {
	client, err := e.pool.GetWithUser(ctx, host.Addr, host.SSHPort, host.SSHUser)
	if err != nil {
		return HostResult{
			Host:  host,
			Error: fmt.Errorf("connecting: %w", err),
		}
	}

	var result *ExecResult
	if sudo {
		result, err = client.ExecSudo(ctx, cmd)
	} else {
		result, err = client.Exec(ctx, cmd)
	}

	if err != nil {
		return HostResult{
			Host:  host,
			Error: fmt.Errorf("executing: %w", err),
		}
	}

	return HostResult{
		Host:    host,
		Result:  result,
		Success: result.ExitCode == 0,
	}
}

// RunFunc runs a function for each host in parallel
func (e *Executor) RunFunc(ctx context.Context, hosts []*inventory.Host, fn func(context.Context, *Client, *inventory.Host) error) []HostResult {
	results := make([]HostResult, len(hosts))

	sem := make(chan struct{}, e.maxParallel)
	var wg sync.WaitGroup

	for i, host := range hosts {
		wg.Add(1)
		go func(idx int, h *inventory.Host) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[idx] = HostResult{
					Host:  h,
					Error: ctx.Err(),
				}
				return
			}

			client, err := e.pool.GetWithUser(ctx, h.Addr, h.SSHPort, h.SSHUser)
			if err != nil {
				results[idx] = HostResult{
					Host:  h,
					Error: fmt.Errorf("connecting: %w", err),
				}
				return
			}

			err = fn(ctx, client, h)
			results[idx] = HostResult{
				Host:    h,
				Success: err == nil,
				Error:   err,
			}
		}(i, host)
	}

	wg.Wait()
	return results
}

// CountSuccess returns the number of successful results
func CountSuccess(results []HostResult) int {
	count := 0
	for _, r := range results {
		if r.Success {
			count++
		}
	}
	return count
}

// CountErrors returns the number of failed results
func CountErrors(results []HostResult) int {
	count := 0
	for _, r := range results {
		if r.Error != nil || !r.Success {
			count++
		}
	}
	return count
}

// FilterFailed returns only the failed results
func FilterFailed(results []HostResult) []HostResult {
	var failed []HostResult
	for _, r := range results {
		if r.Error != nil || !r.Success {
			failed = append(failed, r)
		}
	}
	return failed
}
