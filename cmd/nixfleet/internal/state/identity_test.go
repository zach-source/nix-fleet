package state

import "testing"

func TestDeploymentIdentityAcrossPushAndPull(t *testing.T) {
	cases := []struct {
		name       string
		state      HostState
		path, hash string
		want       bool
	}{
		{"pull", HostState{DeploymentID: "/nix/store/new", ManifestHash: "config-hash", DeploymentManifestHash: "config-hash"}, "/nix/store/new", "nar-hash", true},
		{"new identity wins", HostState{DeploymentID: "/nix/store/old", ManifestHash: "nar-hash", DeploymentManifestHash: "nar-hash"}, "/nix/store/new", "nar-hash", false},
		{"legacy push", HostState{ManifestHash: "nar-hash"}, "/nix/store/new", "nar-hash", true},
		{"legacy stale path is not authoritative", HostState{StorePath: "/nix/store/new", ManifestHash: "other"}, "/nix/store/new", "nar-hash", false},
		{"old writer changed generation", HostState{DeploymentID: "/nix/store/new", CurrentGeneration: 2, DeploymentGeneration: 1, ManifestHash: "same-config-hash", DeploymentManifestHash: "same-config-hash"}, "/nix/store/new", "nar-hash", false},
		{"old writer changed hash", HostState{DeploymentID: "/nix/store/new", ManifestHash: "other", DeploymentManifestHash: "old-hash"}, "/nix/store/new", "nar-hash", false},
		{"missing", HostState{}, "/nix/store/new", "", false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.state.MatchesDeployment(tt.path, tt.hash); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
