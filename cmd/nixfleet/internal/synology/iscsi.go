package synology

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// ActualLUN is an iSCSI LUN as reported by DSM.
type ActualLUN struct {
	UUID     string `json:"uuid"`
	Name     string `json:"name"`
	Size     int64  `json:"size"` // bytes
	Type     string `json:"type"`
	Location string `json:"location"`
	LunID    int    `json:"lun_id"`
}

// ListLUNs returns all iSCSI LUNs on the NAS.
func (c *Client) ListLUNs(ctx context.Context) ([]ActualLUN, error) {
	raw, err := c.get(ctx, "SYNO.Core.ISCSI.LUN", "list", 1, nil)
	if err != nil {
		return nil, fmt.Errorf("list LUNs: %w", err)
	}
	var data struct {
		LUNs []ActualLUN `json:"luns"`
	}
	if err := unmarshalData(raw, &data); err != nil {
		return nil, fmt.Errorf("decode LUN list: %w", err)
	}
	return data.LUNs, nil
}

// CreateLUN creates a new iSCSI LUN from a declared spec.
//
// NOTE: DSM's LUN `type` is storage-backend specific. On a btrfs volume (our
// .67 /volume1) thin LUNs are type "BLUN" — which is what the friendly "THIN"
// maps to here, matching what the synology-csi driver creates. Verify against
// `synology status` (the actual type of existing LUNs) before trusting create.
func (c *Client) CreateLUN(ctx context.Context, l LUN) (string, error) {
	sz, err := l.SizeBytes()
	if err != nil {
		return "", err
	}
	q := url.Values{
		"name":        {l.Name},
		"type":        {dsmCreateType(l)},
		"location":    {l.Location},
		"size":        {strconv.FormatInt(sz, 10)},
		"description": {l.Description},
	}
	if l.CanSnapshot {
		q.Set("can_snapshot", "true")
	}
	raw, err := c.get(ctx, "SYNO.Core.ISCSI.LUN", "create", 1, q)
	if err != nil {
		return "", fmt.Errorf("create LUN %q: %w", l.Name, err)
	}
	var data struct {
		UUID string `json:"uuid"`
	}
	_ = unmarshalData(raw, &data)
	return data.UUID, nil
}

// GrowLUN expands a LUN to newSize bytes (online grow only; DSM rejects shrink).
// Gated behind --allow-resize. Validate against your DSM before relying on it.
func (c *Client) GrowLUN(ctx context.Context, uuid string, newSize int64) error {
	q := url.Values{
		"uuid": {uuid},
		"size": {strconv.FormatInt(newSize, 10)},
	}
	if _, err := c.get(ctx, "SYNO.Core.ISCSI.LUN", "set", 1, q); err != nil {
		return fmt.Errorf("grow LUN %s: %w", uuid, err)
	}
	return nil
}

// DeleteLUN deletes a LUN by uuid (destructive — caller must gate on --prune).
func (c *Client) DeleteLUN(ctx context.Context, uuid string) error {
	q := url.Values{"uuid": {uuid}}
	if _, err := c.get(ctx, "SYNO.Core.ISCSI.LUN", "safe_delete", 1, q); err != nil {
		return fmt.Errorf("delete LUN %s: %w", uuid, err)
	}
	return nil
}

// dsmCreateType maps the friendly LUN type to the DSM create `type` string.
// THIN on btrfs => BLUN; thick/file => FILE; advanced => ADV.
func dsmCreateType(l LUN) string {
	switch l.dsmType() {
	case "THIN":
		return "BLUN"
	default:
		return l.dsmType()
	}
}
