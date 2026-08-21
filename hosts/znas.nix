# Synology NAS (znas, 192.168.1.67) — managed via the DSM API (Model B).
#
# This host is NOT deployed over SSH; `nixfleet synology reconcile znas`
# reconciles the declared DSM state below over the DSM Web API.
#
# IMPORTANT: the synology-csi driver creates/deletes iSCSI LUNs DYNAMICALLY for
# k0s PVCs. Those are NOT declared here and must never be pruned — only declare
# static, manually-provisioned LUNs in `iscsiLuns`, and never run reconcile with
# --prune against this NAS.
{ ... }:
{
  nixfleet.host = {
    name = "znas";
    base = "synology";
    addr = "192.168.1.67";
  };

  nixfleet.synology = {
    enable = true;
    host = "192.168.1.67";
    port = 5001;
    # botuser is already in the DSM administrators group — verified 2026-08-21 on
    # DSM 7.3.2-86009 U4 via SYNO.Core.Group.Member list group=administrators,
    # which returns admin, ztaylor, botuser. DSM writes, NFS apply included, are
    # permitted; nothing needs granting.
    #
    # An earlier version of this note claimed the opposite and sent readers to
    # "Application Privileges → File Station/shared-folder admin". Both halves
    # were wrong, and both are easy to fall for again:
    #   - the 403 behind it came from a MALFORMED call (array `name` + a bogus
    #     `additional`), not from permission. SYNO.Core.User get still fails that
    #     way today, so don't read a 403 there as an authorization signal.
    #   - Application Privileges gate which apps an account may open, not API
    #     write scope. DSM 7 has three groups here (administrators, http, users)
    #     and no share- or NFS-specific privilege, so administrators membership
    #     is the only lever that exists anyway.
    #
    # Blast radius worth knowing: botuser's DSM password lives in the
    # synology-csi client-info-secret, so anything with RBAC to read that secret
    # holds full DSM admin. Pointing the CSI driver at a non-admin account would
    # contain that, with nixfleet keeping botuser for reconcile.
    user = "botuser";

    # Static iSCSI LUNs (CSI-managed dynamic LUNs are intentionally absent).
    iscsiLuns = [ ];

    # NFS exports. k0s-gti (/volume1/k0s-gti) is the cluster backup target the
    # zfs/k0s backups write to (see modules/backup.nix). These rules MATCH the
    # live export (client "*", verified via `synology status`), so reconcile is
    # a no-op. TODO security: tighten client "*" to the real mount source subnet
    # once the node→.67 source IPs are confirmed (don't break the backup mount).
    nfsExports = [
      {
        name = "k0s-gti";
        rules = [
          {
            client = "*";
            access = "rw";
            squash = "root_squash";
            secure = false;
            async = true;
          }
        ];
      }
    ];
  };
}
