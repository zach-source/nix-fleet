package juicefs

import "testing"

func TestConfigWithDefaults(t *testing.T) {
	t.Run("fills all zero fields", func(t *testing.T) {
		c := Config{}.WithDefaults()
		if c.Vault != "Personal Agents" {
			t.Errorf("Vault = %q, want %q", c.Vault, "Personal Agents")
		}
		if c.Namespace != "juicefs-system" {
			t.Errorf("Namespace = %q, want %q", c.Namespace, "juicefs-system")
		}
		if c.FSName != "fleet" {
			t.Errorf("FSName = %q, want %q", c.FSName, "fleet")
		}
		if c.CacheDir != "/var/lib/juicefs/cache" {
			t.Errorf("CacheDir = %q, want %q", c.CacheDir, "/var/lib/juicefs/cache")
		}
		if c.K0sNode != "gti" {
			t.Errorf("K0sNode = %q, want %q", c.K0sNode, "gti")
		}
		if c.KeyItem != "juicefs-encryption-key" {
			t.Errorf("KeyItem = %q", c.KeyItem)
		}
		if c.PGItem != "juicefs-postgres" {
			t.Errorf("PGItem = %q", c.PGItem)
		}
		if c.MinIOItem != "juicefs-minio" {
			t.Errorf("MinIOItem = %q", c.MinIOItem)
		}
		if c.CSIItem != "juicefs-csi-secret" {
			t.Errorf("CSIItem = %q", c.CSIItem)
		}
		if c.PGHost != "localhost" || c.PGPort != 5432 {
			t.Errorf("PG endpoint = %s:%d, want localhost:5432", c.PGHost, c.PGPort)
		}
		if c.MinIOHost != "localhost" || c.MinIOPort != 9000 {
			t.Errorf("MinIO endpoint = %s:%d, want localhost:9000", c.MinIOHost, c.MinIOPort)
		}
	})

	t.Run("preserves user overrides", func(t *testing.T) {
		in := Config{
			Vault:   "Fleet Infra",
			FSName:  "scratch",
			K0sNode: "gtr-150",
			PGPort:  15432,
			KeyItem: "custom-key",
		}
		out := in.WithDefaults()
		if out.Vault != "Fleet Infra" {
			t.Errorf("Vault override lost: %q", out.Vault)
		}
		if out.FSName != "scratch" {
			t.Errorf("FSName override lost: %q", out.FSName)
		}
		if out.K0sNode != "gtr-150" {
			t.Errorf("K0sNode override lost: %q", out.K0sNode)
		}
		if out.PGPort != 15432 {
			t.Errorf("PGPort override lost: %d", out.PGPort)
		}
		if out.KeyItem != "custom-key" {
			t.Errorf("KeyItem override lost: %q", out.KeyItem)
		}
		// Non-overridden fields still get defaults.
		if out.Namespace != "juicefs-system" {
			t.Errorf("Namespace default not applied: %q", out.Namespace)
		}
	})
}

func TestOpReference(t *testing.T) {
	cases := []struct {
		vault, item, field, want string
	}{
		{"Personal Agents", "juicefs-encryption-key", "passphrase",
			"op://Personal Agents/juicefs-encryption-key/passphrase"},
		{"Fleet Infra", "csi", "encrypt_rsa_key",
			"op://Fleet Infra/csi/encrypt_rsa_key"},
	}
	for _, tc := range cases {
		got := OpReference(tc.vault, tc.item, tc.field)
		if got != tc.want {
			t.Errorf("OpReference(%q,%q,%q) = %q, want %q",
				tc.vault, tc.item, tc.field, got, tc.want)
		}
	}
}
