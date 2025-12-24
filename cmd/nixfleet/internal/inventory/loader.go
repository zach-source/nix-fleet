package inventory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// rawInventory is the structure as stored in YAML files
type rawInventory struct {
	Hosts  map[string]rawHost  `yaml:"hosts"`
	Groups map[string]rawGroup `yaml:"groups"`
}

type rawHost struct {
	Base      string            `yaml:"base"`
	Addr      string            `yaml:"addr"`
	SSHUser   string            `yaml:"ssh_user"`
	SSHPort   int               `yaml:"ssh_port"`
	Roles     []string          `yaml:"roles"`
	Tags      map[string]string `yaml:"tags"`
	OSUpdates rawOSUpdates      `yaml:"os_updates"`
	Rollout   rawRollout        `yaml:"rollout"`
}

type rawOSUpdates struct {
	Mode                 string   `yaml:"mode"`
	AutoReboot           bool     `yaml:"auto_reboot"`
	RebootWindow         string   `yaml:"reboot_window"`
	Holds                []string `yaml:"holds"`
	MaxConcurrentReboots int      `yaml:"max_concurrent_reboots"`
	AutoSwitch           bool     `yaml:"auto_switch"`
}

type rawRollout struct {
	CanaryPercent       int `yaml:"canary_percent"`
	MaxParallel         int `yaml:"max_parallel"`
	PauseBetweenBatches int `yaml:"pause_between_batches"`
}

type rawGroup struct {
	Hosts    []string       `yaml:"hosts"`
	Children []string       `yaml:"children"`
	Vars     map[string]any `yaml:"vars"`
}

// LoadFromDir loads inventory from a directory of YAML files
func LoadFromDir(dir string) (*Inventory, error) {
	inv := NewInventory()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading inventory dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		path := filepath.Join(dir, name)
		if err := loadFile(inv, path); err != nil {
			return nil, fmt.Errorf("loading %s: %w", path, err)
		}
	}

	return inv, nil
}

// LoadFromFile loads inventory from a single YAML file
func LoadFromFile(path string) (*Inventory, error) {
	inv := NewInventory()
	if err := loadFile(inv, path); err != nil {
		return nil, err
	}
	return inv, nil
}

func loadFile(inv *Inventory, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}

	var raw rawInventory
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing yaml: %w", err)
	}

	// Convert raw hosts to typed hosts
	for name, rh := range raw.Hosts {
		host := &Host{
			Name:    name,
			Base:    rh.Base,
			Addr:    rh.Addr,
			SSHUser: rh.SSHUser,
			SSHPort: rh.SSHPort,
			Roles:   rh.Roles,
			Tags:    rh.Tags,
			OSUpdate: OSUpdateConfig{
				Mode:                 rh.OSUpdates.Mode,
				AutoReboot:           rh.OSUpdates.AutoReboot,
				RebootWindow:         rh.OSUpdates.RebootWindow,
				Holds:                rh.OSUpdates.Holds,
				MaxConcurrentReboots: rh.OSUpdates.MaxConcurrentReboots,
				AutoSwitch:           rh.OSUpdates.AutoSwitch,
			},
			Rollout: RolloutConfig{
				CanaryPercent:       rh.Rollout.CanaryPercent,
				MaxParallel:         rh.Rollout.MaxParallel,
				PauseBetweenBatches: rh.Rollout.PauseBetweenBatches,
			},
		}

		// Apply defaults
		applyHostDefaults(host)

		inv.Hosts[name] = host
	}

	// Convert raw groups to typed groups
	for name, rg := range raw.Groups {
		group := &Group{
			Name:     name,
			Hosts:    rg.Hosts,
			Children: rg.Children,
			Vars:     rg.Vars,
		}
		inv.Groups[name] = group
	}

	return nil
}

func applyHostDefaults(h *Host) {
	if h.SSHUser == "" {
		h.SSHUser = "deploy"
	}
	if h.SSHPort == 0 {
		h.SSHPort = 22
	}
	if h.Base == "" {
		h.Base = "ubuntu"
	}
	if h.OSUpdate.Mode == "" {
		h.OSUpdate.Mode = "manual"
	}
	if h.OSUpdate.MaxConcurrentReboots == 0 {
		h.OSUpdate.MaxConcurrentReboots = 1
	}
	if h.Rollout.MaxParallel == 0 {
		h.Rollout.MaxParallel = 5
	}
	if h.Rollout.CanaryPercent == 0 {
		h.Rollout.CanaryPercent = 10
	}
	if h.Rollout.PauseBetweenBatches == 0 {
		h.Rollout.PauseBetweenBatches = 30
	}
	if h.Tags == nil {
		h.Tags = make(map[string]string)
	}
}

// Validate checks inventory for consistency
func (inv *Inventory) Validate() error {
	// Check all group hosts exist
	for groupName, group := range inv.Groups {
		for _, hostName := range group.Hosts {
			if _, ok := inv.Hosts[hostName]; !ok {
				return fmt.Errorf("group %q references unknown host %q", groupName, hostName)
			}
		}
		for _, childName := range group.Children {
			if _, ok := inv.Groups[childName]; !ok {
				return fmt.Errorf("group %q references unknown child group %q", groupName, childName)
			}
		}
	}

	// Validate host configurations
	for name, host := range inv.Hosts {
		if host.Addr == "" {
			return fmt.Errorf("host %q has no address", name)
		}
		if host.Base != "ubuntu" && host.Base != "nixos" {
			return fmt.Errorf("host %q has invalid base %q (must be 'ubuntu' or 'nixos')", name, host.Base)
		}
	}

	return nil
}
