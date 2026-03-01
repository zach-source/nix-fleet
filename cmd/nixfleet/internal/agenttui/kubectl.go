package agenttui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nixfleet/nixfleet/internal/ssh"
)

// AgentNamespaces lists all agent namespaces in the cluster.
var AgentNamespaces = []string{
	"agent-coder",
	"agent-sre",
	"agent-pm",
	"agent-sage",
	"agent-orchestrator",
	"agent-personal",
}

// agentDisplayNames maps namespace → human-readable display name.
var agentDisplayNames = map[string]string{
	"agent-coder":        "coder (Axel)",
	"agent-sre":          "sre (Quinn)",
	"agent-pm":           "pm (Marcus)",
	"agent-sage":         "sage",
	"agent-orchestrator": "orchestrator (Atlas)",
	"agent-personal":     "personal (Ada)",
}

// PodInfo holds parsed pod status from kubectl.
type PodInfo struct {
	Name      string
	Namespace string
	Status    string
	Ready     bool
	Restarts  int
	Age       string
	StartTime time.Time
}

// GatewayHealth holds parsed output from `openclaw health --json` via kubectl exec.
type GatewayHealth struct {
	Namespace string
	Raw       map[string]interface{} // full JSON for display
	Gateway   struct {
		Status string // "ok", "degraded", "error"
		Uptime string
	}
	Channels []ChannelStatus
	Sessions []SessionInfo
	Model    string
	Error    string // non-empty if health check failed
}

// ChannelStatus holds a channel's connection state.
type ChannelStatus struct {
	Name   string // "slack", "telegram", etc.
	Status string // "connected", "disconnected", "error"
}

// SessionInfo holds a session summary.
type SessionInfo struct {
	Key        string
	Agent      string
	Messages   int
	LastActive string
}

// DisplayName returns the human-readable agent name for a namespace.
func DisplayName(namespace string) string {
	if name, ok := agentDisplayNames[namespace]; ok {
		return name
	}
	return strings.TrimPrefix(namespace, "agent-")
}

// ShortName returns the short agent name (e.g. "coder") from namespace.
func ShortName(namespace string) string {
	return strings.TrimPrefix(namespace, "agent-")
}

// kubectlPodList is the JSON structure from kubectl get pods.
type kubectlPodList struct {
	Items []kubectlPod `json:"items"`
}

type kubectlPod struct {
	Metadata struct {
		Name              string `json:"name"`
		Namespace         string `json:"namespace"`
		CreationTimestamp string `json:"creationTimestamp"`
	} `json:"metadata"`
	Status struct {
		Phase             string `json:"phase"`
		ContainerStatuses []struct {
			Ready        bool `json:"ready"`
			RestartCount int  `json:"restartCount"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

// GetPods fetches pod status for all agent namespaces via SSH + k0s kubectl.
func GetPods(ctx context.Context, client *ssh.Client) ([]PodInfo, error) {
	cmd := "for ns in " + strings.Join(AgentNamespaces, " ") + "; do sudo k0s kubectl get pods -n $ns -o json 2>/dev/null; done"
	result, err := client.Exec(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("kubectl get pods: %w", err)
	}

	var pods []PodInfo
	decoder := json.NewDecoder(strings.NewReader(result.Stdout))
	for decoder.More() {
		var podList kubectlPodList
		if err := decoder.Decode(&podList); err != nil {
			continue
		}
		for _, p := range podList.Items {
			pod := PodInfo{
				Name:      p.Metadata.Name,
				Namespace: p.Metadata.Namespace,
				Status:    p.Status.Phase,
			}
			if len(p.Status.ContainerStatuses) > 0 {
				pod.Ready = p.Status.ContainerStatuses[0].Ready
				pod.Restarts = p.Status.ContainerStatuses[0].RestartCount
			}
			if t, err := time.Parse(time.RFC3339, p.Metadata.CreationTimestamp); err == nil {
				pod.StartTime = t
				pod.Age = formatAge(time.Since(t))
			}
			pods = append(pods, pod)
		}
	}

	// Fill in missing namespaces
	foundNS := make(map[string]bool)
	for _, p := range pods {
		foundNS[p.Namespace] = true
	}
	for _, ns := range AgentNamespaces {
		if !foundNS[ns] {
			pods = append(pods, PodInfo{
				Name:      ns + "-0",
				Namespace: ns,
				Status:    "NotFound",
			})
		}
	}

	return pods, nil
}

// GetGatewayHealth queries the OpenClaw gateway health endpoint via kubectl exec.
// Runs `openclaw health --json` inside the agent pod to get structured gateway data.
func GetGatewayHealth(ctx context.Context, client *ssh.Client, namespace, podName string) (*GatewayHealth, error) {
	cmd := fmt.Sprintf(
		"sudo k0s kubectl exec -n %s %s -- openclaw health --json 2>/dev/null",
		namespace, podName,
	)
	result, err := client.Exec(ctx, cmd)
	if err != nil {
		return &GatewayHealth{
			Namespace: namespace,
			Error:     fmt.Sprintf("exec failed: %v", err),
		}, nil
	}

	health := &GatewayHealth{Namespace: namespace}

	// Try to parse as JSON
	output := strings.TrimSpace(result.Stdout)
	if output == "" {
		health.Error = "empty response"
		return health, nil
	}

	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		// Not JSON — store raw text as error context
		health.Error = "non-JSON response"
		health.Raw = map[string]interface{}{"raw": output}
		return health, nil
	}
	health.Raw = raw

	// Parse gateway status
	if gw, ok := raw["gateway"].(map[string]interface{}); ok {
		if s, ok := gw["status"].(string); ok {
			health.Gateway.Status = s
		}
		if u, ok := gw["uptime"].(string); ok {
			health.Gateway.Uptime = u
		}
	}

	// Parse channels
	if channels, ok := raw["channels"].(map[string]interface{}); ok {
		for name, v := range channels {
			cs := ChannelStatus{Name: name}
			if ch, ok := v.(map[string]interface{}); ok {
				if s, ok := ch["status"].(string); ok {
					cs.Status = s
				} else if s, ok := ch["state"].(string); ok {
					cs.Status = s
				}
			}
			health.Channels = append(health.Channels, cs)
		}
	}

	// Parse model
	if agents, ok := raw["agents"].(map[string]interface{}); ok {
		if defaults, ok := agents["defaults"].(map[string]interface{}); ok {
			if model, ok := defaults["model"].(string); ok {
				health.Model = model
			}
		}
	}
	if health.Model == "" {
		if model, ok := raw["model"].(string); ok {
			health.Model = model
		}
	}

	// Parse sessions
	if sessions, ok := raw["sessions"].([]interface{}); ok {
		for _, s := range sessions {
			if sess, ok := s.(map[string]interface{}); ok {
				si := SessionInfo{}
				if k, ok := sess["key"].(string); ok {
					si.Key = k
				}
				if a, ok := sess["agent"].(string); ok {
					si.Agent = a
				}
				if m, ok := sess["messages"].(float64); ok {
					si.Messages = int(m)
				}
				if la, ok := sess["lastActive"].(string); ok {
					si.LastActive = la
				}
				health.Sessions = append(health.Sessions, si)
			}
		}
	}

	return health, nil
}

// GetGatewayStatus queries the OpenClaw gateway via `openclaw status --deep`.
// Returns the full text output for display in the TUI.
func GetGatewayStatus(ctx context.Context, client *ssh.Client, namespace, podName string) (string, error) {
	cmd := fmt.Sprintf(
		"sudo k0s kubectl exec -n %s %s -- openclaw status --deep 2>&1",
		namespace, podName,
	)
	result, err := client.Exec(ctx, cmd)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), nil
	}
	return result.Stdout, nil
}

// GetLogs fetches the last N lines of pod logs (fallback/supplementary view).
func GetLogs(ctx context.Context, client *ssh.Client, namespace, podName string, lines int) (string, error) {
	cmd := fmt.Sprintf("sudo k0s kubectl logs --tail=%d -n %s %s 2>&1", lines, namespace, podName)
	result, err := client.Exec(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("kubectl logs: %w", err)
	}
	return result.Stdout, nil
}

// formatAge formats a duration into a human-readable age string.
func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		days := int(d.Hours()) / 24
		h := int(d.Hours()) % 24
		return fmt.Sprintf("%dd%dh", days, h)
	}
}
