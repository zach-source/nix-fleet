package juicefs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

func (c Config) kubectlArgs() []string {
	args := []string{}
	if c.Kubeconfig != "" {
		args = append(args, "--kubeconfig", c.Kubeconfig)
	}
	if c.KubeContext != "" {
		args = append(args, "--context", c.KubeContext)
	}
	return args
}

// GetSecretField reads a single field from a k8s Secret and base64-decodes it.
// Missing secret/field returns ("", nil) so callers can poll. Returns an error
// for base64 decode failures only (malformed data).
func (c Config) GetSecretField(ctx context.Context, name, field string) (string, error) {
	args := append(c.kubectlArgs(),
		"-n", c.Namespace,
		"get", "secret", name,
		"-o", fmt.Sprintf("jsonpath={.data.%s}", field),
	)
	out, err := runOutput(ctx, "kubectl", args...)
	if err != nil {
		// Not-ready / not-found is the normal polling case — caller decides
		// when to give up via ctx deadline.
		return "", nil
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return "", nil
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("decode %s.%s: %w", name, field, err)
	}
	return string(decoded), nil
}

func (c Config) WaitForSecret(ctx context.Context, name string, fields []string, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		ready := true
		for _, f := range fields {
			v, err := c.GetSecretField(ctx, name, f)
			if err != nil {
				return err
			}
			if v == "" {
				ready = false
				break
			}
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for secret %s/%s", c.Namespace, name)
		case <-t.C:
		}
	}
}

type podList struct {
	Items []struct {
		Status struct {
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

func (c Config) WaitForPodsReady(ctx context.Context, selector string, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		args := append(c.kubectlArgs(),
			"-n", c.Namespace,
			"get", "pods",
			"-l", selector,
			"-o", "json",
		)
		out, err := runOutput(ctx, "kubectl", args...)
		if err == nil {
			var pl podList
			if err := json.Unmarshal(out, &pl); err == nil && len(pl.Items) > 0 {
				allReady := true
				for _, p := range pl.Items {
					ready := false
					for _, cond := range p.Status.Conditions {
						if cond.Type == "Ready" && cond.Status == "True" {
							ready = true
							break
						}
					}
					if !ready {
						allReady = false
						break
					}
				}
				if allReady {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for pods matching %q", selector)
		case <-t.C:
		}
	}
}

// PortForward starts `kubectl port-forward` and probes the local port via
// TCP dial until reachable (up to 10s). Returns a cancel that kills the
// subprocess and waits for it to exit, so no zombies leak.
func (c Config) PortForward(ctx context.Context, service string, localPort, targetPort int) (func(), error) {
	args := append(c.kubectlArgs(),
		"-n", c.Namespace,
		"port-forward",
		"svc/"+service,
		fmt.Sprintf("%d:%d", localPort, targetPort),
	)
	cmd := exec.CommandContext(ctx, "kubectl", args...)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("kubectl port-forward: %w", err)
	}

	// Reap the subprocess in the background so context-cancellation (which
	// sends SIGKILL via exec.CommandContext) doesn't leave a zombie.
	var once sync.Once
	waitDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waitDone)
	}()

	// Probe localhost:localPort until connectable.
	target := fmt.Sprintf("127.0.0.1:%d", localPort)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", target, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			cancel := func() {
				once.Do(func() {
					if cmd.Process != nil {
						_ = cmd.Process.Signal(syscall.SIGTERM)
					}
					<-waitDone
				})
			}
			return cancel, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Timeout — kill and report.
	if cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	<-waitDone
	return nil, fmt.Errorf("port-forward %s: local port %d never opened", service, localPort)
}
