package synology

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ActualShare is a shared folder as reported by DSM, including its NFS rules.
type ActualShare struct {
	Name     string          `json:"name"`
	VolPath  string          `json:"vol_path"` // e.g. /volume1
	NFSRules json.RawMessage `json:"nfs_rule"` // raw; schema varies by DSM version
}

// HasNFS reports whether the share has any NFS rules configured.
func (s ActualShare) HasNFS() bool {
	t := strings.TrimSpace(string(s.NFSRules))
	return t != "" && t != "null" && t != "[]"
}

// ListShares returns all shared folders with their NFS rules.
func (c *Client) ListShares(ctx context.Context) ([]ActualShare, error) {
	q := url.Values{
		// request the nfs_rule + volume info in the listing
		"additional": {`["nfs_rule","vol_path"]`},
	}
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

// nfsRulePayload is DSM's per-client NFS rule. Field names follow DSM 7.x; if a
// future DSM rejects this, capture the schema via `synology status --raw-nfs`
// from an existing export and adjust.
type nfsRulePayload struct {
	Client      string `json:"client"`
	Privilege   string `json:"privilege"` // "rw" | "ro"
	Squash      string `json:"squash"`    // "root_squash" | "all_squash" | "no_mapping"
	Security    string `json:"security"`  // "sys"
	EnableAsync bool   `json:"enable_async"`
	AllowSubdir bool   `json:"is_allow_subfolder"`
	NonPrivPort bool   `json:"is_allow_nonprivileged_port"` // inverse of "secure"
}

func (r NFSRule) toPayload() nfsRulePayload {
	priv := strings.ToLower(r.Access)
	if priv != "ro" {
		priv = "rw"
	}
	sq := r.Squash
	if sq == "" {
		sq = "root_squash"
	}
	return nfsRulePayload{
		Client:      r.Client,
		Privilege:   priv,
		Squash:      sq,
		Security:    "sys",
		EnableAsync: r.Async,
		AllowSubdir: true,
		NonPrivPort: !r.Secure,
	}
}

// SetNFSRules sets the NFS rule list on an existing share.
//
// NOTE: live probing showed per-share NFS rules are NOT under SYNO.Core.Share
// (it returns only share metadata). They live under the internal
// SYNO.Core.FileServ.NFS.SharePrivilege API (method "load"/set), whose exact
// params we haven't pinned yet (returns 2301). Until then, configure NFS share
// rules via the generic escape hatch (nixfleet.synology.settings) once the
// SharePrivilege params are confirmed, or set them in the DSM UI. This call is
// left as the typed target and will be repointed at SharePrivilege.
func (c *Client) SetNFSRules(ctx context.Context, share string, rules []NFSRule) error {
	payload := make([]nfsRulePayload, 0, len(rules))
	for _, r := range rules {
		payload = append(payload, r.toPayload())
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	q := url.Values{
		"name":     {share},
		"nfs_rule": {string(b)},
	}
	if _, err := c.get(ctx, "SYNO.Core.Share", "set", 1, q); err != nil {
		return fmt.Errorf("set NFS rules on %q: %w", share, err)
	}
	return nil
}
