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

// EvalAttrJSON evaluates an arbitrary flake attribute to JSON (e.g.
// "nixfleetConfigurations.znas.synology"). Used by API-driven backends that
// reconcile a declarative spec rather than building/copying a closure.
func (e *Evaluator) EvalAttrJSON(ctx context.Context, attr string) ([]byte, error) {
	flakeRef := fmt.Sprintf("%s#%s", e.flakePath, attr)
	cmd := exec.CommandContext(ctx, e.nixBin, "eval", "--json", flakeRef)
	cmd.Dir = e.flakePath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("nix eval %s failed: %w\nstderr: %s", attr, err, stderr.String())
	}
	return stdout.Bytes(), nil
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
	// Use --impure and NIXPKGS_ALLOW_UNFREE=1 to allow unfree packages in user configs
	cmd := exec.CommandContext(ctx, e.nixBin, "build", "--no-link", "--print-out-paths", "--impure", flakeRef)
	cmd.Dir = e.flakePath
	cmd.Env = append(os.Environ(), "NIXPKGS_ALLOW_UNFREE=1")

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

// DeclaredFile is a file managed by the host config (nixfleet.files.<path>).
// Either Text or Source is set (the other is null).
type DeclaredFile struct {
	Text         *string  `json:"text"`
	Source       *string  `json:"source"`
	Mode         string   `json:"mode"`
	Owner        string   `json:"owner"`
	Group        string   `json:"group"`
	RestartUnits []string `json:"restartUnits"`
}

// EvalManagedFiles returns the files declared by a host's config
// (config.nixfleet.files), keyed by absolute destination path. Used to
// compute the expected on-host state when adopting an out-of-band host.
func (e *Evaluator) EvalManagedFiles(ctx context.Context, hostName string) (map[string]DeclaredFile, error) {
	attr := fmt.Sprintf("nixfleetConfigurations.%s.config.nixfleet.files", hostName)
	flakeRef := fmt.Sprintf("%s#%s", e.flakePath, attr)

	cmd := exec.CommandContext(ctx, e.nixBin, "eval", "--json", flakeRef)
	cmd.Dir = e.flakePath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("nix eval files failed: %w\nstderr: %s", err, stderr.String())
	}

	var files map[string]DeclaredFile
	if err := json.Unmarshal(stdout.Bytes(), &files); err != nil {
		return nil, fmt.Errorf("parsing declared files: %w", err)
	}

	return files, nil
}

// getManifestHash calculates a hash for the store path
func (e *Evaluator) getManifestHash(ctx context.Context, storePath string) (string, error) {
	cmd := exec.CommandContext(ctx, e.nixBin, "path-info", "--json", storePath)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return "", err
	}

	// Newer Nix renders `path-info --json` as an object keyed by store path;
	// older Nix renders an array. Handle both.
	var asMap map[string]struct {
		NarHash string `json:"narHash"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &asMap); err == nil && len(asMap) > 0 {
		if info, ok := asMap[storePath]; ok && info.NarHash != "" {
			return info.NarHash, nil
		}
		for _, info := range asMap {
			if info.NarHash != "" {
				return info.NarHash, nil
			}
		}
	}

	var asArray []struct {
		NarHash string `json:"narHash"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &asArray); err == nil && len(asArray) > 0 {
		return asArray[0].NarHash, nil
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

// FlakeUpdate runs `nix flake update [inputs...]` against the flake, refreshing
// flake.lock. With no inputs it updates every input; otherwise only the named
// ones (e.g. "nixpkgs"). Returns the combined nix output, which on a real
// change includes the "Updated input ...: 'old' → 'new'" lines.
func (e *Evaluator) FlakeUpdate(ctx context.Context, inputs ...string) (string, error) {
	args := []string{"flake", "update"}
	args = append(args, inputs...)
	// nix flake update operates on the flake in the current dir; point it at ours.
	cmd := exec.CommandContext(ctx, e.nixBin, args...)
	cmd.Dir = e.flakePath

	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	if err := cmd.Run(); err != nil {
		return combined.String(), fmt.Errorf("nix flake update failed: %w", err)
	}
	return combined.String(), nil
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
