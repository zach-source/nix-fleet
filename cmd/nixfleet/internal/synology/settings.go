package synology

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Setting is a declarative DSM API call applied during reconcile — the generic
// escape hatch for configuring *any* DSM setting not yet covered by a typed
// option (mirrors the Terraform provider's generic API resource). The whole DSM
// settings surface (SYNO.Core.FileServ.NFS/SMB, SYNO.Core.Network, .System,
// .Service, .SNMP, …) follows the same entry.cgi get/set shape, so one wrapper
// reaches all of it.
//
//	nixfleet.synology.settings = [{
//	  api = "SYNO.Core.FileServ.NFS"; method = "set"; version = 1;
//	  params = { enable_nfs = "true"; enable_nfs_v4 = "true"; };
//	}];
type Setting struct {
	API     string            `json:"api"`
	Method  string            `json:"method"`
	Version int               `json:"version"`
	Params  map[string]string `json:"params"`
}

func (s Setting) version() int {
	if s.Version <= 0 {
		return 1
	}
	return s.Version
}

func (s Setting) values() url.Values {
	v := url.Values{}
	keys := make([]string, 0, len(s.Params))
	for k := range s.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys) // stable ordering for logging/idempotency
	for _, k := range keys {
		v.Set(k, s.Params[k])
	}
	return v
}

// String renders a setting for plans/logs (params keys only — values may be
// sensitive and aren't printed).
func (s Setting) String() string {
	keys := make([]string, 0, len(s.Params))
	for k := range s.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return fmt.Sprintf("%s.%s v%d {%s}", s.API, s.Method, s.version(), strings.Join(keys, ","))
}

// Call issues a generic DSM API call and returns the raw data payload. Works for
// reads (method=get/list/load) and writes (method=set) alike.
func (c *Client) Call(ctx context.Context, api, method string, version int, params map[string]string) (json.RawMessage, error) {
	if version <= 0 {
		version = 1
	}
	extra := url.Values{}
	for k, v := range params {
		extra.Set(k, v)
	}
	raw, err := c.get(ctx, api, method, version, extra)
	if err != nil {
		return nil, fmt.Errorf("%s.%s v%d: %w", api, method, version, err)
	}
	return raw, nil
}

// ApplySetting issues a declared setting (an idempotent write). DSM set calls
// are idempotent — re-applying the same params is a no-op.
func (c *Client) ApplySetting(ctx context.Context, s Setting) error {
	if _, err := c.get(ctx, s.API, s.Method, s.version(), s.values()); err != nil {
		return fmt.Errorf("apply %s: %w", s, err)
	}
	return nil
}
