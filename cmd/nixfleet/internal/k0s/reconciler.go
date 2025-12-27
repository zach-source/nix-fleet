// Package k0s implements k0s Kubernetes cluster management and resource reconciliation
package k0s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/nixfleet/nixfleet/internal/ssh"
	"github.com/nixfleet/nixfleet/internal/state"
)

const (
	// K0sPath is the path to the k0s binary
	K0sPath = "/usr/local/bin/k0s"

	// K0sConfigPath is the path to k0s configuration
	K0sConfigPath = "/etc/k0s/k0s.yaml"

	// K0sManifestsPath is where k0s auto-applies manifests from
	K0sManifestsPath = "/var/lib/k0s/manifests"
)

// Reconciler handles k0s resource reconciliation
type Reconciler struct {
	stateMgr *state.Manager
}

// NewReconciler creates a new k0s reconciler
func NewReconciler() *Reconciler {
	return &Reconciler{
		stateMgr: state.NewManager(),
	}
}

// ReconcileResult contains the results of a reconciliation
type ReconcileResult struct {
	Success           bool     `json:"success"`
	OrphanedCharts    []string `json:"orphaned_charts,omitempty"`
	DeletedCharts     []string `json:"deleted_charts,omitempty"`
	OrphanedResources []string `json:"orphaned_resources,omitempty"`
	DeletedResources  []string `json:"deleted_resources,omitempty"`
	Errors            []string `json:"errors,omitempty"`
	ConfigChanged     bool     `json:"config_changed"`
}

// ParsedK0sConfig represents parsed k0s configuration for state tracking
type ParsedK0sConfig struct {
	ConfigHash string
	HelmCharts []state.K0sHelmChartState
	Manifests  []state.K0sManifestState
}

// IsK0sEnabled checks if k0s is running on the host
func (r *Reconciler) IsK0sEnabled(ctx context.Context, client *ssh.Client) bool {
	result, err := client.Exec(ctx, "systemctl is-active k0scontroller.service 2>/dev/null || systemctl is-active k0sworker.service 2>/dev/null")
	if err != nil || result.ExitCode != 0 {
		return false
	}
	return strings.TrimSpace(result.Stdout) == "active"
}

// ParseCurrentConfig reads and parses the current k0s configuration
func (r *Reconciler) ParseCurrentConfig(ctx context.Context, client *ssh.Client) (*ParsedK0sConfig, error) {
	// Read k0s.yaml
	result, err := client.Exec(ctx, fmt.Sprintf("cat %s 2>/dev/null", K0sConfigPath))
	if err != nil || result.ExitCode != 0 {
		return nil, fmt.Errorf("k0s config not found")
	}

	configContent := result.Stdout
	configHash := hashString(configContent)

	parsed := &ParsedK0sConfig{
		ConfigHash: configHash,
		HelmCharts: []state.K0sHelmChartState{},
		Manifests:  []state.K0sManifestState{},
	}

	// Parse Helm charts from k0s.yaml using regex (simple approach)
	// Format: - name: xxx\n  chartname: yyy\n  version: zzz\n  namespace: www
	chartPattern := regexp.MustCompile(`(?m)- name: (\S+)\s+chartname: (\S+)\s+version: "?([^"\s]+)"?\s+namespace: (\S+)`)
	matches := chartPattern.FindAllStringSubmatch(configContent, -1)
	for _, m := range matches {
		if len(m) >= 5 {
			parsed.HelmCharts = append(parsed.HelmCharts, state.K0sHelmChartState{
				Name:      m[1],
				ChartName: m[2],
				Version:   m[3],
				Namespace: m[4],
			})
		}
	}

	// Parse manifests from /var/lib/k0s/manifests/
	manifestsResult, err := client.Exec(ctx, fmt.Sprintf("ls -1 %s/*.yaml 2>/dev/null || true", K0sManifestsPath))
	if err == nil && manifestsResult.ExitCode == 0 {
		for _, file := range strings.Split(manifestsResult.Stdout, "\n") {
			file = strings.TrimSpace(file)
			if file == "" {
				continue
			}

			// Read each manifest and extract resource info
			catResult, err := client.Exec(ctx, fmt.Sprintf("cat %s", file))
			if err != nil || catResult.ExitCode != 0 {
				continue
			}

			// Parse YAML to extract kind, name, apiVersion
			// Simple regex approach for reliability
			resources := r.parseManifestResources(file, catResult.Stdout)
			parsed.Manifests = append(parsed.Manifests, resources...)
		}
	}

	return parsed, nil
}

// parseManifestResources extracts resource info from a manifest file
func (r *Reconciler) parseManifestResources(file, content string) []state.K0sManifestState {
	var resources []state.K0sManifestState

	// Split by --- for multi-document YAML
	docs := strings.Split(content, "---")
	for _, doc := range docs {
		doc = strings.TrimSpace(doc)
		if doc == "" {
			continue
		}

		// Extract apiVersion, kind, name, namespace using regex
		apiVersionMatch := regexp.MustCompile(`(?m)^apiVersion:\s*(\S+)`).FindStringSubmatch(doc)
		kindMatch := regexp.MustCompile(`(?m)^kind:\s*(\S+)`).FindStringSubmatch(doc)
		nameMatch := regexp.MustCompile(`(?m)^\s+name:\s*(\S+)`).FindStringSubmatch(doc)
		nsMatch := regexp.MustCompile(`(?m)^\s+namespace:\s*(\S+)`).FindStringSubmatch(doc)

		if len(apiVersionMatch) < 2 || len(kindMatch) < 2 || len(nameMatch) < 2 {
			continue
		}

		resource := state.K0sManifestState{
			APIVersion:   apiVersionMatch[1],
			Kind:         kindMatch[1],
			Name:         nameMatch[1],
			ManifestFile: strings.TrimPrefix(file, K0sManifestsPath+"/"),
		}

		// Set logical name based on file name (without .yaml)
		resource.LogicalName = strings.TrimSuffix(resource.ManifestFile, ".yaml")

		if len(nsMatch) >= 2 {
			resource.Namespace = nsMatch[1]
		}

		resources = append(resources, resource)
	}

	return resources
}

// Reconcile compares previous and current state, cleaning up orphaned resources
func (r *Reconciler) Reconcile(ctx context.Context, client *ssh.Client, previousState *state.K0sState, dryRun bool) (*ReconcileResult, error) {
	result := &ReconcileResult{Success: true}

	// Parse current config
	currentConfig, err := r.ParseCurrentConfig(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("parsing current config: %w", err)
	}

	// Check if config changed
	if previousState != nil && previousState.ConfigHash != "" {
		result.ConfigChanged = previousState.ConfigHash != currentConfig.ConfigHash
	} else {
		result.ConfigChanged = true
	}

	// Skip if no previous state (first deploy)
	if previousState == nil || len(previousState.HelmCharts) == 0 && len(previousState.Manifests) == 0 {
		log.Printf("[k0s] No previous state, skipping orphan cleanup")
		return result, nil
	}

	// Find orphaned Helm charts
	currentCharts := make(map[string]bool)
	for _, chart := range currentConfig.HelmCharts {
		currentCharts[chart.Name] = true
	}

	for _, prevChart := range previousState.HelmCharts {
		if !currentCharts[prevChart.Name] {
			result.OrphanedCharts = append(result.OrphanedCharts, prevChart.Name)
		}
	}

	// Find orphaned manifest resources
	currentResources := make(map[string]bool)
	for _, res := range currentConfig.Manifests {
		key := fmt.Sprintf("%s/%s/%s", res.Kind, res.Namespace, res.Name)
		currentResources[key] = true
	}

	for _, prevRes := range previousState.Manifests {
		key := fmt.Sprintf("%s/%s/%s", prevRes.Kind, prevRes.Namespace, prevRes.Name)
		if !currentResources[key] {
			result.OrphanedResources = append(result.OrphanedResources, key)
		}
	}

	// Delete orphaned Helm charts
	for _, chartName := range result.OrphanedCharts {
		if dryRun {
			log.Printf("[k0s] Would delete orphaned Helm chart: %s", chartName)
			continue
		}

		log.Printf("[k0s] Deleting orphaned Helm chart: %s", chartName)
		if err := r.deleteHelmChart(ctx, client, chartName); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("deleting chart %s: %v", chartName, err))
		} else {
			result.DeletedCharts = append(result.DeletedCharts, chartName)
		}
	}

	// Delete orphaned manifest resources
	for i, resKey := range result.OrphanedResources {
		prevRes := previousState.Manifests[i]

		if dryRun {
			log.Printf("[k0s] Would delete orphaned resource: %s", resKey)
			continue
		}

		log.Printf("[k0s] Deleting orphaned resource: %s", resKey)
		if err := r.deleteResource(ctx, client, prevRes); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("deleting resource %s: %v", resKey, err))
		} else {
			result.DeletedResources = append(result.DeletedResources, resKey)
		}
	}

	if len(result.Errors) > 0 {
		result.Success = false
	}

	return result, nil
}

// deleteHelmChart deletes a Helm chart and its k0s Chart CR
func (r *Reconciler) deleteHelmChart(ctx context.Context, client *ssh.Client, chartName string) error {
	// Find the namespace from helm releases
	nsResult, err := client.ExecSudo(ctx, fmt.Sprintf(
		"%s kubectl get chart k0s-addon-chart-%s -n kube-system -o jsonpath='{.spec.namespace}' 2>/dev/null || echo ''",
		K0sPath, chartName))

	namespace := strings.TrimSpace(strings.Trim(nsResult.Stdout, "'"))
	if namespace == "" {
		namespace = "kube-system" // fallback
	}

	// Delete helm release secrets (to fully clean up)
	_, _ = client.ExecSudo(ctx, fmt.Sprintf(
		"%s kubectl delete secret -n %s -l owner=helm,name=%s --ignore-not-found",
		K0sPath, namespace, chartName))

	// Delete the k0s Chart CR
	_, err = client.ExecSudo(ctx, fmt.Sprintf(
		"%s kubectl delete chart k0s-addon-chart-%s -n kube-system --ignore-not-found",
		K0sPath, chartName))
	if err != nil {
		return fmt.Errorf("deleting chart CR: %w", err)
	}

	// Delete namespace if it's empty (and not kube-system)
	if namespace != "kube-system" && namespace != "default" && namespace != "cert-manager" {
		// Check if namespace is empty
		podsResult, _ := client.ExecSudo(ctx, fmt.Sprintf(
			"%s kubectl get pods -n %s --no-headers 2>/dev/null | wc -l",
			K0sPath, namespace))
		if strings.TrimSpace(podsResult.Stdout) == "0" {
			_, _ = client.ExecSudo(ctx, fmt.Sprintf(
				"%s kubectl delete namespace %s --ignore-not-found",
				K0sPath, namespace))
		}
	}

	return nil
}

// deleteResource deletes a Kubernetes resource
func (r *Reconciler) deleteResource(ctx context.Context, client *ssh.Client, res state.K0sManifestState) error {
	var cmd string
	if res.Namespace != "" {
		cmd = fmt.Sprintf("%s kubectl delete %s %s -n %s --ignore-not-found",
			K0sPath, strings.ToLower(res.Kind), res.Name, res.Namespace)
	} else {
		cmd = fmt.Sprintf("%s kubectl delete %s %s --ignore-not-found",
			K0sPath, strings.ToLower(res.Kind), res.Name)
	}

	result, err := client.ExecSudo(ctx, cmd)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("kubectl delete failed: %s", result.Stderr)
	}
	return nil
}

// BuildNewState creates a new K0sState from the current config
func (r *Reconciler) BuildNewState(ctx context.Context, client *ssh.Client) (*state.K0sState, error) {
	if !r.IsK0sEnabled(ctx, client) {
		return &state.K0sState{Enabled: false}, nil
	}

	config, err := r.ParseCurrentConfig(ctx, client)
	if err != nil {
		return nil, err
	}

	return &state.K0sState{
		Enabled:       true,
		ConfigHash:    config.ConfigHash,
		HelmCharts:    config.HelmCharts,
		Manifests:     config.Manifests,
		LastReconcile: time.Now(),
	}, nil
}

// UpdateState updates the host state with current k0s state
func (r *Reconciler) UpdateState(ctx context.Context, client *ssh.Client) error {
	hostState, err := r.stateMgr.ReadState(ctx, client)
	if err != nil {
		hostState = state.NewHostState("", "")
	}

	k0sState, err := r.BuildNewState(ctx, client)
	if err != nil {
		return fmt.Errorf("building k0s state: %w", err)
	}

	hostState.K0s = k0sState
	return r.stateMgr.WriteState(ctx, client, hostState)
}

// GetStatus returns the current k0s cluster status
func (r *Reconciler) GetStatus(ctx context.Context, client *ssh.Client) (*K0sStatus, error) {
	status := &K0sStatus{
		Enabled: r.IsK0sEnabled(ctx, client),
	}

	if !status.Enabled {
		return status, nil
	}

	// Get node status
	nodesResult, _ := client.ExecSudo(ctx, fmt.Sprintf("%s kubectl get nodes -o json", K0sPath))
	if nodesResult.ExitCode == 0 {
		var nodeList struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Status struct {
					Conditions []struct {
						Type   string `json:"type"`
						Status string `json:"status"`
					} `json:"conditions"`
				} `json:"status"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(nodesResult.Stdout), &nodeList); err == nil {
			for _, node := range nodeList.Items {
				ns := NodeStatus{Name: node.Metadata.Name}
				for _, cond := range node.Status.Conditions {
					if cond.Type == "Ready" {
						ns.Ready = cond.Status == "True"
						break
					}
				}
				status.Nodes = append(status.Nodes, ns)
			}
		}
	}

	// Get helm releases
	chartsResult, _ := client.ExecSudo(ctx, fmt.Sprintf(
		"%s kubectl get chart -n kube-system -o jsonpath='{range .items[*]}{.metadata.name}{\" \"}{.status.releaseName}{\" \"}{.status.appVersion}{\"\\n\"}{end}'",
		K0sPath))
	if chartsResult.ExitCode == 0 {
		for _, line := range strings.Split(chartsResult.Stdout, "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				version := ""
				if len(parts) > 2 {
					version = parts[2]
				}
				status.HelmReleases = append(status.HelmReleases, HelmReleaseStatus{
					Name:    parts[1],
					Version: version,
					Status:  "deployed",
				})
			}
		}
	}

	// Get CiliumLoadBalancerIPPool
	poolsResult, _ := client.ExecSudo(ctx, fmt.Sprintf(
		"%s kubectl get ciliumloadbalancerippool -o jsonpath='{range .items[*]}{.metadata.name}{\" \"}{.spec.blocks[0].cidr}{\"\\n\"}{end}'",
		K0sPath))
	if poolsResult.ExitCode == 0 {
		for _, line := range strings.Split(poolsResult.Stdout, "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				status.IPPools = append(status.IPPools, IPPoolStatus{
					Name: parts[0],
					CIDR: parts[1],
				})
			}
		}
	}

	return status, nil
}

// K0sStatus represents the current k0s cluster status
type K0sStatus struct {
	Enabled      bool                `json:"enabled"`
	Nodes        []NodeStatus        `json:"nodes,omitempty"`
	HelmReleases []HelmReleaseStatus `json:"helm_releases,omitempty"`
	IPPools      []IPPoolStatus      `json:"ip_pools,omitempty"`
}

// NodeStatus represents a Kubernetes node status
type NodeStatus struct {
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
}

// HelmReleaseStatus represents a Helm release status
type HelmReleaseStatus struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Status  string `json:"status"`
}

// IPPoolStatus represents a Cilium IP pool status
type IPPoolStatus struct {
	Name string `json:"name"`
	CIDR string `json:"cidr"`
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
