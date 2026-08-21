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
    # botuser has iSCSI permission (created for synology-csi) but is not in the
    # administrators group, so every DSM write 403s — including NFS apply. Reads
    # all work today (verified 2026-08-21 on DSM 7.3.2-86009 U4 against
    # SYNO.Core.Share list, SYNO.Core.FileServ.NFS get and
    # …NFS.SharePrivilege load), so `status` and dry-run `reconcile` are safe.
    #
    # Application Privileges will NOT fix this: they gate which apps an account
    # may open, not API write scope. DSM 7 has exactly three groups here
    # (administrators, http, users) and no share- or NFS-specific privilege, so
    # SYNO.Core.* set/save requires administrators membership — nothing narrower
    # exists. Note the blast radius before promoting botuser: its password lives
    # in the synology-csi client-info-secret, so anything that can read that
    # secret would then hold full DSM admin. A separate admin account whose
    # password never enters the cluster keeps the CSI credential unprivileged.
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
