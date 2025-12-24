package inventory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewInventory(t *testing.T) {
	inv := NewInventory()

	if inv.Hosts == nil {
		t.Error("Hosts map should not be nil")
	}
	if inv.Groups == nil {
		t.Error("Groups map should not be nil")
	}
	if len(inv.Hosts) != 0 {
		t.Errorf("Expected 0 hosts, got %d", len(inv.Hosts))
	}
	if len(inv.Groups) != 0 {
		t.Errorf("Expected 0 groups, got %d", len(inv.Groups))
	}
}

func TestGetHost(t *testing.T) {
	inv := NewInventory()
	inv.Hosts["web1"] = &Host{Name: "web1", Base: "ubuntu", Addr: "10.0.0.1"}

	// Test existing host
	host, ok := inv.GetHost("web1")
	if !ok {
		t.Error("Expected to find host web1")
	}
	if host.Name != "web1" {
		t.Errorf("Expected name 'web1', got '%s'", host.Name)
	}
	if host.Addr != "10.0.0.1" {
		t.Errorf("Expected addr '10.0.0.1', got '%s'", host.Addr)
	}

	// Test non-existing host
	_, ok = inv.GetHost("nonexistent")
	if ok {
		t.Error("Should not find nonexistent host")
	}
}

func TestGetGroup(t *testing.T) {
	inv := NewInventory()
	inv.Groups["webservers"] = &Group{Name: "webservers", Hosts: []string{"web1", "web2"}}

	// Test existing group
	group, ok := inv.GetGroup("webservers")
	if !ok {
		t.Error("Expected to find group webservers")
	}
	if group.Name != "webservers" {
		t.Errorf("Expected name 'webservers', got '%s'", group.Name)
	}
	if len(group.Hosts) != 2 {
		t.Errorf("Expected 2 hosts, got %d", len(group.Hosts))
	}

	// Test non-existing group
	_, ok = inv.GetGroup("nonexistent")
	if ok {
		t.Error("Should not find nonexistent group")
	}
}

func TestHostsInGroup(t *testing.T) {
	inv := NewInventory()

	// Add hosts
	inv.Hosts["web1"] = &Host{Name: "web1", Base: "ubuntu"}
	inv.Hosts["web2"] = &Host{Name: "web2", Base: "ubuntu"}
	inv.Hosts["db1"] = &Host{Name: "db1", Base: "nixos"}

	// Add groups
	inv.Groups["webservers"] = &Group{Name: "webservers", Hosts: []string{"web1", "web2"}}
	inv.Groups["databases"] = &Group{Name: "databases", Hosts: []string{"db1"}}
	inv.Groups["all"] = &Group{
		Name:     "all",
		Children: []string{"webservers", "databases"},
	}

	// Test direct group
	webHosts := inv.HostsInGroup("webservers")
	if len(webHosts) != 2 {
		t.Errorf("Expected 2 hosts in webservers, got %d", len(webHosts))
	}

	// Test nested group
	allHosts := inv.HostsInGroup("all")
	if len(allHosts) != 3 {
		t.Errorf("Expected 3 hosts in all, got %d", len(allHosts))
	}

	// Test non-existing group
	noHosts := inv.HostsInGroup("nonexistent")
	if len(noHosts) != 0 {
		t.Errorf("Expected 0 hosts for nonexistent group, got %d", len(noHosts))
	}
}

func TestHostsInGroupNoDuplicates(t *testing.T) {
	inv := NewInventory()

	// Add hosts
	inv.Hosts["web1"] = &Host{Name: "web1", Base: "ubuntu"}

	// Add groups with same host
	inv.Groups["group1"] = &Group{Name: "group1", Hosts: []string{"web1"}}
	inv.Groups["group2"] = &Group{Name: "group2", Hosts: []string{"web1"}}
	inv.Groups["parent"] = &Group{
		Name:     "parent",
		Children: []string{"group1", "group2"},
	}

	// Should not have duplicates
	hosts := inv.HostsInGroup("parent")
	if len(hosts) != 1 {
		t.Errorf("Expected 1 unique host, got %d", len(hosts))
	}
}

func TestAllHosts(t *testing.T) {
	inv := NewInventory()

	// Empty inventory
	if len(inv.AllHosts()) != 0 {
		t.Error("Expected empty hosts list")
	}

	// Add hosts
	inv.Hosts["web1"] = &Host{Name: "web1"}
	inv.Hosts["web2"] = &Host{Name: "web2"}

	hosts := inv.AllHosts()
	if len(hosts) != 2 {
		t.Errorf("Expected 2 hosts, got %d", len(hosts))
	}
}

func TestFilterByBase(t *testing.T) {
	inv := NewInventory()

	inv.Hosts["ubuntu1"] = &Host{Name: "ubuntu1", Base: "ubuntu"}
	inv.Hosts["ubuntu2"] = &Host{Name: "ubuntu2", Base: "ubuntu"}
	inv.Hosts["nixos1"] = &Host{Name: "nixos1", Base: "nixos"}

	ubuntuHosts := inv.FilterByBase("ubuntu")
	if len(ubuntuHosts) != 2 {
		t.Errorf("Expected 2 ubuntu hosts, got %d", len(ubuntuHosts))
	}

	nixosHosts := inv.FilterByBase("nixos")
	if len(nixosHosts) != 1 {
		t.Errorf("Expected 1 nixos host, got %d", len(nixosHosts))
	}

	darwinHosts := inv.FilterByBase("darwin")
	if len(darwinHosts) != 0 {
		t.Errorf("Expected 0 darwin hosts, got %d", len(darwinHosts))
	}
}

func TestFilterByTag(t *testing.T) {
	inv := NewInventory()

	inv.Hosts["prod1"] = &Host{Name: "prod1", Tags: map[string]string{"env": "production"}}
	inv.Hosts["prod2"] = &Host{Name: "prod2", Tags: map[string]string{"env": "production"}}
	inv.Hosts["dev1"] = &Host{Name: "dev1", Tags: map[string]string{"env": "development"}}
	inv.Hosts["notag"] = &Host{Name: "notag"}

	prodHosts := inv.FilterByTag("env", "production")
	if len(prodHosts) != 2 {
		t.Errorf("Expected 2 production hosts, got %d", len(prodHosts))
	}

	devHosts := inv.FilterByTag("env", "development")
	if len(devHosts) != 1 {
		t.Errorf("Expected 1 development host, got %d", len(devHosts))
	}

	noHosts := inv.FilterByTag("env", "staging")
	if len(noHosts) != 0 {
		t.Errorf("Expected 0 staging hosts, got %d", len(noHosts))
	}
}

func TestLoadFromFile(t *testing.T) {
	// Create temp file
	tmpDir := t.TempDir()
	invFile := filepath.Join(tmpDir, "inventory.yaml")

	content := `
hosts:
  web1:
    name: web1
    base: ubuntu
    addr: 10.0.0.1
    ssh_port: 22
  db1:
    name: db1
    base: nixos
    addr: 10.0.0.2

groups:
  webservers:
    name: webservers
    hosts:
      - web1
`
	if err := os.WriteFile(invFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	inv, err := LoadFromFile(invFile)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if len(inv.Hosts) != 2 {
		t.Errorf("Expected 2 hosts, got %d", len(inv.Hosts))
	}

	web1, ok := inv.GetHost("web1")
	if !ok {
		t.Error("Expected to find web1")
	}
	if web1.Base != "ubuntu" {
		t.Errorf("Expected base 'ubuntu', got '%s'", web1.Base)
	}
	if web1.SSHPort != 22 {
		t.Errorf("Expected port 22, got %d", web1.SSHPort)
	}

	group, ok := inv.GetGroup("webservers")
	if !ok {
		t.Error("Expected to find webservers group")
	}
	if len(group.Hosts) != 1 {
		t.Errorf("Expected 1 host in group, got %d", len(group.Hosts))
	}
}

func TestLoadFromDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Create hosts file
	hostsContent := `
hosts:
  server1:
    name: server1
    base: ubuntu
    addr: 192.168.1.1
`
	if err := os.WriteFile(filepath.Join(tmpDir, "hosts.yaml"), []byte(hostsContent), 0644); err != nil {
		t.Fatalf("Failed to write hosts file: %v", err)
	}

	// Create groups file
	groupsContent := `
groups:
  servers:
    name: servers
    hosts:
      - server1
`
	if err := os.WriteFile(filepath.Join(tmpDir, "groups.yaml"), []byte(groupsContent), 0644); err != nil {
		t.Fatalf("Failed to write groups file: %v", err)
	}

	inv, err := LoadFromDir(tmpDir)
	if err != nil {
		t.Fatalf("LoadFromDir failed: %v", err)
	}

	if len(inv.Hosts) != 1 {
		t.Errorf("Expected 1 host, got %d", len(inv.Hosts))
	}

	if len(inv.Groups) != 1 {
		t.Errorf("Expected 1 group, got %d", len(inv.Groups))
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		inv     *Inventory
		wantErr bool
	}{
		{
			name:    "empty inventory",
			inv:     NewInventory(),
			wantErr: false, // empty is valid (no hosts to validate)
		},
		{
			name: "valid inventory",
			inv: &Inventory{
				Hosts: map[string]*Host{
					"web1": {Name: "web1", Base: "ubuntu", Addr: "10.0.0.1"},
				},
				Groups: make(map[string]*Group),
			},
			wantErr: false,
		},
		{
			name: "missing base",
			inv: &Inventory{
				Hosts: map[string]*Host{
					"web1": {Name: "web1", Addr: "10.0.0.1"},
				},
				Groups: make(map[string]*Group),
			},
			wantErr: true,
		},
		{
			name: "missing addr",
			inv: &Inventory{
				Hosts: map[string]*Host{
					"web1": {Name: "web1", Base: "ubuntu"},
				},
				Groups: make(map[string]*Group),
			},
			wantErr: true,
		},
		{
			name: "invalid base",
			inv: &Inventory{
				Hosts: map[string]*Host{
					"web1": {Name: "web1", Base: "windows", Addr: "10.0.0.1"},
				},
				Groups: make(map[string]*Group),
			},
			wantErr: true,
		},
		{
			name: "group references nonexistent host",
			inv: &Inventory{
				Hosts: map[string]*Host{
					"web1": {Name: "web1", Base: "ubuntu", Addr: "10.0.0.1"},
				},
				Groups: map[string]*Group{
					"webservers": {Name: "webservers", Hosts: []string{"web1", "web2"}},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.inv.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
