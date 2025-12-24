package inventory

// Host represents a managed host in the inventory
type Host struct {
	Name     string            `yaml:"name" json:"name"`
	Base     string            `yaml:"base" json:"base"` // "ubuntu" or "nixos"
	Addr     string            `yaml:"addr" json:"addr"`
	SSHUser  string            `yaml:"ssh_user" json:"ssh_user"`
	SSHPort  int               `yaml:"ssh_port" json:"ssh_port"`
	Roles    []string          `yaml:"roles" json:"roles"`
	Tags     map[string]string `yaml:"tags" json:"tags"`
	OSUpdate OSUpdateConfig    `yaml:"os_updates" json:"os_updates"`
	Rollout  RolloutConfig     `yaml:"rollout" json:"rollout"`
}

// OSUpdateConfig defines OS update behavior
type OSUpdateConfig struct {
	// Ubuntu-specific
	Mode                 string   `yaml:"mode" json:"mode"` // "security-daily", "full-weekly", "manual"
	AutoReboot           bool     `yaml:"auto_reboot" json:"auto_reboot"`
	RebootWindow         string   `yaml:"reboot_window" json:"reboot_window"` // e.g., "Sun 02:00-04:00"
	Holds                []string `yaml:"holds" json:"holds"`                 // packages to hold
	MaxConcurrentReboots int      `yaml:"max_concurrent_reboots" json:"max_concurrent_reboots"`

	// NixOS-specific
	AutoSwitch bool `yaml:"auto_switch" json:"auto_switch"`
}

// RolloutConfig defines deployment behavior
type RolloutConfig struct {
	CanaryPercent       int `yaml:"canary_percent" json:"canary_percent"`
	MaxParallel         int `yaml:"max_parallel" json:"max_parallel"`
	PauseBetweenBatches int `yaml:"pause_between_batches" json:"pause_between_batches"` // seconds
}

// Group represents a group of hosts
type Group struct {
	Name     string         `yaml:"name" json:"name"`
	Hosts    []string       `yaml:"hosts" json:"hosts"`
	Children []string       `yaml:"children" json:"children"` // nested groups
	Vars     map[string]any `yaml:"vars" json:"vars"`
}

// Inventory holds all hosts and groups
type Inventory struct {
	Hosts  map[string]*Host  `yaml:"hosts" json:"hosts"`
	Groups map[string]*Group `yaml:"groups" json:"groups"`
}

// NewInventory creates an empty inventory
func NewInventory() *Inventory {
	return &Inventory{
		Hosts:  make(map[string]*Host),
		Groups: make(map[string]*Group),
	}
}

// GetHost returns a host by name
func (inv *Inventory) GetHost(name string) (*Host, bool) {
	h, ok := inv.Hosts[name]
	return h, ok
}

// GetGroup returns a group by name
func (inv *Inventory) GetGroup(name string) (*Group, bool) {
	g, ok := inv.Groups[name]
	return g, ok
}

// HostsInGroup returns all hosts in a group (recursively resolving children)
func (inv *Inventory) HostsInGroup(groupName string) []*Host {
	group, ok := inv.Groups[groupName]
	if !ok {
		return nil
	}

	seen := make(map[string]bool)
	return inv.resolveGroupHosts(group, seen)
}

func (inv *Inventory) resolveGroupHosts(group *Group, seen map[string]bool) []*Host {
	var hosts []*Host

	// Add direct hosts
	for _, hostName := range group.Hosts {
		if seen[hostName] {
			continue
		}
		seen[hostName] = true
		if h, ok := inv.Hosts[hostName]; ok {
			hosts = append(hosts, h)
		}
	}

	// Recursively add hosts from child groups
	for _, childName := range group.Children {
		if child, ok := inv.Groups[childName]; ok {
			hosts = append(hosts, inv.resolveGroupHosts(child, seen)...)
		}
	}

	return hosts
}

// AllHosts returns all hosts in the inventory
func (inv *Inventory) AllHosts() []*Host {
	hosts := make([]*Host, 0, len(inv.Hosts))
	for _, h := range inv.Hosts {
		hosts = append(hosts, h)
	}
	return hosts
}

// FilterByBase returns hosts matching the given base OS
func (inv *Inventory) FilterByBase(base string) []*Host {
	var hosts []*Host
	for _, h := range inv.Hosts {
		if h.Base == base {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// FilterByTag returns hosts with a matching tag
func (inv *Inventory) FilterByTag(key, value string) []*Host {
	var hosts []*Host
	for _, h := range inv.Hosts {
		if h.Tags[key] == value {
			hosts = append(hosts, h)
		}
	}
	return hosts
}
