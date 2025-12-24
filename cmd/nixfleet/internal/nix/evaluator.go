package nix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Evaluator handles Nix evaluation and builds
type Evaluator struct {
	flakePath string
	nixBin    string
}

// NewEvaluator creates a new Nix evaluator
func NewEvaluator(flakePath string) (*Evaluator, error) {
	// Find nix binary
	nixBin, err := exec.LookPath("nix")
	if err != nil {
		return nil, fmt.Errorf("nix not found in PATH: %w", err)
	}

	// Verify flake path exists
	if _, err := os.Stat(flakePath); err != nil {
		return nil, fmt.Errorf("flake path does not exist: %w", err)
	}

	return &Evaluator{
		flakePath: flakePath,
		nixBin:    nixBin,
	}, nil
}

// HostClosure represents a built host configuration
type HostClosure struct {
	HostName     string `json:"hostName"`
	StorePath    string `json:"storePath"`
	Base         string `json:"base"` // "ubuntu", "nixos", or "darwin"
	ManifestHash string `json:"manifestHash"`
}

// EvalHost evaluates a host configuration and returns its store path
func (e *Evaluator) EvalHost(ctx context.Context, hostName string, base string) (*HostClosure, error) {
	var attr string
	switch base {
	case "nixos":
		attr = fmt.Sprintf("nixosConfigurations.%s.config.system.build.toplevel", hostName)
	case "ubuntu":
		attr = fmt.Sprintf("nixfleetConfigurations.%s.system", hostName)
	case "darwin":
		attr = fmt.Sprintf("darwinConfigurations.%s.system", hostName)
	default:
		return nil, fmt.Errorf("unknown base: %s", base)
	}

	// Evaluate to get the derivation path
	flakeRef := fmt.Sprintf("%s#%s", e.flakePath, attr)

	cmd := exec.CommandContext(ctx, e.nixBin, "eval", "--raw", flakeRef)
	cmd.Dir = e.flakePath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("nix eval failed: %w\nstderr: %s", err, stderr.String())
	}

	storePath := strings.TrimSpace(stdout.String())

	return &HostClosure{
		HostName:  hostName,
		StorePath: storePath,
		Base:      base,
	}, nil
}

// BuildHost builds a host configuration and returns the store path
func (e *Evaluator) BuildHost(ctx context.Context, hostName string, base string) (*HostClosure, error) {
	var attr string
	switch base {
	case "nixos":
		attr = fmt.Sprintf("nixosConfigurations.%s.config.system.build.toplevel", hostName)
	case "ubuntu":
		attr = fmt.Sprintf("nixfleetConfigurations.%s.system", hostName)
	case "darwin":
		attr = fmt.Sprintf("darwinConfigurations.%s.system", hostName)
	default:
		return nil, fmt.Errorf("unknown base: %s", base)
	}

	flakeRef := fmt.Sprintf("%s#%s", e.flakePath, attr)

	// Build the configuration
	cmd := exec.CommandContext(ctx, e.nixBin, "build", "--no-link", "--print-out-paths", flakeRef)
	cmd.Dir = e.flakePath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("nix build failed: %w\nstderr: %s", err, stderr.String())
	}

	storePath := strings.TrimSpace(stdout.String())

	// Calculate manifest hash
	manifestHash, err := e.getManifestHash(ctx, storePath)
	if err != nil {
		// Non-fatal, just log
		manifestHash = ""
	}

	return &HostClosure{
		HostName:     hostName,
		StorePath:    storePath,
		Base:         base,
		ManifestHash: manifestHash,
	}, nil
}

// getManifestHash calculates a hash for the store path
func (e *Evaluator) getManifestHash(ctx context.Context, storePath string) (string, error) {
	cmd := exec.CommandContext(ctx, e.nixBin, "path-info", "--json", storePath)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return "", err
	}

	var pathInfo []struct {
		NarHash string `json:"narHash"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &pathInfo); err != nil {
		return "", err
	}

	if len(pathInfo) > 0 {
		return pathInfo[0].NarHash, nil
	}

	return "", nil
}

// GetClosureSize returns the size of a closure in bytes
func (e *Evaluator) GetClosureSize(ctx context.Context, storePath string) (int64, error) {
	cmd := exec.CommandContext(ctx, e.nixBin, "path-info", "-S", "--json", storePath)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return 0, err
	}

	var pathInfo []struct {
		ClosureSize int64 `json:"closureSize"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &pathInfo); err != nil {
		return 0, err
	}

	if len(pathInfo) > 0 {
		return pathInfo[0].ClosureSize, nil
	}

	return 0, nil
}

// ListFlakeOutputs lists available outputs in the flake
func (e *Evaluator) ListFlakeOutputs(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, e.nixBin, "flake", "show", "--json", e.flakePath)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("nix flake show failed: %w", err)
	}

	var outputs map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &outputs); err != nil {
		return nil, fmt.Errorf("parsing flake outputs: %w", err)
	}

	var result []string
	for key := range outputs {
		result = append(result, key)
	}

	return result, nil
}

// FlakePath returns the path to the flake
func (e *Evaluator) FlakePath() string {
	return e.flakePath
}

// ResolveFlakePath resolves a potentially relative flake path
func ResolveFlakePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	return filepath.Join(cwd, path), nil
}
