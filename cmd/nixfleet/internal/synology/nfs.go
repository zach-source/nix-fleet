package synology

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// ActualShare is a shared folder as reported by SYNO.Core.Share list.
type ActualShare struct {
	Name    string `json:"name"`
	VolPath string `json:"vol_path"` // e.g. /volume1
}

// ListShares returns all shared folders.
func (c *Client) ListShares(ctx context.Context) ([]ActualShare, error) {
	q := url.Values{"additional": {`["vol_path"]`}}
	raw, err := c.get(ctx, "SYNO.Core.Share", "list", 1, q)
	if err != nil {
		return nil, fmt.Errorf("list shares: %w", err)
	}
	var data struct {
		Shares []ActualShare `json:"shares"`
	}
	if err := unmarshalData(raw, &data); err != nil {
		return nil, fmt.Errorf("decode share list: %w", err)
	}
	return data.Shares, nil
}

// DSMNFSRule is one NFS client rule as stored by DSM. Schema confirmed live
// against SYNO.Core.FileServ.NFS.SharePrivilege (load/save, v1).
type DSMNFSRule struct {
	Client     string `json:"client"`
	Privilege  string `json:"privilege"`   // "rw" | "ro"
	RootSquash string `json:"root_squash"` // "root" | "all" | "no"
	Async      bool   `json:"async"`
	Insecure   bool   `json:"insecure"` // inverse of NFSRule.secure
	Crossmnt   bool   `json:"crossmnt"` // allow subfolder mounts
	Security   struct {
		Sys               bool `json:"sys"`
		Kerberos          bool `json:"kerberos"`
		KerberosIntegrity bool `json:"kerberos_integrity"`
		KerberosPrivacy   bool `json:"kerberos_privacy"`
	} `json:"security_flavor"`
}

const nfsPrivAPI = "SYNO.Core.FileServ.NFS.SharePrivilege"

// LoadNFSRules returns the NFS rules for a share (nil if NFS isn't configured).
func (c *Client) LoadNFSRules(ctx context.Context, shareName string) ([]DSMNFSRule, error) {
	q := url.Values{"share_name": {shareName}, "clear": {"false"}}
	raw, err := c.get(ctx, nfsPrivAPI, "load", 1, q)
	if err != nil {
		return nil, fmt.Errorf("load NFS rules for %q: %w", shareName, err)
	}
	var data struct {
		Rule []DSMNFSRule `json:"rule"`
	}
	if err := unmarshalData(raw, &data); err != nil {
		return nil, fmt.Errorf("decode NFS rules for %q: %w", shareName, err)
	}
	return data.Rule, nil
}

// SaveNFSRules sets the NFS rule list on a share (the declared set replaces
// what's there). Destructive → caller gates on --apply. Confirmed via a no-op
// round-trip; rules round-trip identically.
func (c *Client) SaveNFSRules(ctx context.Context, shareName string, rules []DSMNFSRule) error {
	b, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	q := url.Values{"share_name": {shareName}, "clear": {"false"}, "rule": {string(b)}}
	if _, err := c.get(ctx, nfsPrivAPI, "save", 1, q); err != nil {
		return fmt.Errorf("save NFS rules on %q: %w", shareName, err)
	}
	return nil
}

// toDSM maps a declared NFSRule to the DSM schema. Fields not exposed in Nix get
// sensible defaults (crossmnt on, sys security).
func (r NFSRule) toDSM() DSMNFSRule {
	priv := strings.ToLower(r.Access)
	if priv != "ro" {
		priv = "rw"
	}
	sq := map[string]string{"root_squash": "root", "all_squash": "all", "no_mapping": "no"}[r.Squash]
	if sq == "" {
		sq = "root"
	}
	d := DSMNFSRule{
		Client:     r.Client,
		Privilege:  priv,
		RootSquash: sq,
		Async:      r.Async,
		Insecure:   !r.Secure,
		Crossmnt:   true,
	}
	d.Security.Sys = true
	return d
}

// key is a stable comparison key for a rule (ignores ordering).
func (d DSMNFSRule) key() string {
	return fmt.Sprintf("%s|%s|%s|%t|%t|%t|%t", d.Client, d.Privilege, d.RootSquash, d.Async, d.Insecure, d.Crossmnt, d.Security.Sys)
}

// NFSRulesMatch reports whether the actual DSM rules already equal the declared
// set (order-insensitive on the fields nixfleet manages).
func NFSRulesMatch(declared []NFSRule, actual []DSMNFSRule) bool {
	if len(declared) != len(actual) {
		return false
	}
	want := make([]string, 0, len(declared))
	for _, r := range declared {
		want = append(want, r.toDSM().key())
	}
	got := make([]string, 0, len(actual))
	for _, a := range actual {
		// normalise: only the fields we manage participate in the key
		na := a
		na.Security = struct {
			Sys               bool `json:"sys"`
			Kerberos          bool `json:"kerberos"`
			KerberosIntegrity bool `json:"kerberos_integrity"`
			KerberosPrivacy   bool `json:"kerberos_privacy"`
		}{Sys: a.Security.Sys}
		got = append(got, na.key())
	}
	sort.Strings(want)
	sort.Strings(got)
	for i := range want {
		if want[i] != got[i] {
			return false
		}
	}
	return true
}
