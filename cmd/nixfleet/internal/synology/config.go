// Package synology implements nixfleet's Model-B backend: declarative management
// of a Synology NAS via the DSM Web API (not SSH). Desired state is declared in
// Nix under nixfleet.synology and reconciled here. This slice covers iSCSI LUNs
// and NFS exports — the resources the k0s CSI driver and backups already depend on.
package synology

import (
	"fmt"
	"strconv"
	"strings"
)

// Config is the desired DSM state, produced by
// `nix eval --json .#nixfleetConfigurations.<host>.synology`.
type Config struct {
	Enable     bool        `json:"enable"`
	Host       string      `json:"host"`
	Port       int         `json:"port"`
	HTTPS      bool        `json:"https"`
	User       string      `json:"user"`
	ISCSILUNs  []LUN       `json:"iscsiLuns"`
	NFSExports []NFSExport `json:"nfsExports"`
}

// LUN is a declared iSCSI LUN.
type LUN struct {
	Name            string `json:"name"`
	Location        string `json:"location"` // e.g. /volume1
	Size            string `json:"size"`     // human size, e.g. "500G"
	Type            string `json:"type"`     // THIN | FILE | ADV
	ThinProvisioned bool   `json:"thinProvisioned"`
	Description     string `json:"description"`
	CanSnapshot     bool   `json:"canSnapshot"`
}

// NFSExport is a declared NFS-shared folder.
type NFSExport struct {
	Name  string    `json:"name"`  // share name (and default path component)
	Path  string    `json:"path"`  // optional; defaults to <vol>/<name>
	Rules []NFSRule `json:"rules"` // NFS client access rules
}

// NFSRule is one NFS client access entry.
type NFSRule struct {
	Client string `json:"client"` // IP, CIDR, hostname, or *
	Access string `json:"access"` // "rw" | "ro"
	Squash string `json:"squash"` // "root_squash" | "all_squash" | "no_mapping"
	Secure bool   `json:"secure"` // require source port < 1024
	Async  bool   `json:"async"`  // async writes
}

// SizeBytes converts the human size (e.g. "500G", "1T", "10737418240") to bytes.
func (l LUN) SizeBytes() (int64, error) {
	return parseSize(l.Size)
}

var sizeUnits = []struct {
	suffix string
	mult   int64
}{
	{"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10},
}

func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	s = strings.TrimSuffix(s, "B") // accept GB/TB as G/T
	for _, u := range sizeUnits {
		if strings.HasSuffix(s, u.suffix) {
			n, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
			if err != nil {
				return 0, fmt.Errorf("invalid size %q: %w", s, err)
			}
			return int64(n * float64(u.mult)), nil
		}
	}
	// plain byte count
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q (use e.g. 500G, 1T, or a byte count)", s)
	}
	return n, nil
}

// lunType maps the friendly type to the DSM LUN type string.
func (l LUN) dsmType() string {
	switch strings.ToUpper(l.Type) {
	case "", "THIN":
		return "THIN"
	case "FILE", "THICK":
		return "FILE"
	case "ADV", "ADVANCED":
		return "ADV"
	default:
		return strings.ToUpper(l.Type)
	}
}
