# NixFleet Synology (DSM-API) backend options — Model B.
#
# Declares desired Synology DSM state (iSCSI LUNs + NFS exports). The nixfleet
# CLI evaluates `config.nixfleet.synology` and reconciles it over the DSM Web
# API (see cmd/nixfleet/internal/synology). Nothing here is "built" — it's a
# pure declarative spec, so no backend compile step is involved.
{ lib, ... }:
let
  inherit (lib) mkOption types;

  lunType = types.submodule {
    options = {
      name = mkOption {
        type = types.str;
        description = "iSCSI LUN name (match key against the NAS).";
      };
      location = mkOption {
        type = types.str;
        default = "/volume1";
        description = "Volume the LUN lives on (e.g. /volume1).";
      };
      size = mkOption {
        type = types.str;
        example = "500G";
        description = "LUN size — human form (500G, 1T) or a raw byte count.";
      };
      type = mkOption {
        type = types.enum [
          "THIN"
          "FILE"
          "ADV"
        ];
        default = "THIN";
        description = "LUN type. THIN maps to btrfs block-level (BLUN) on create.";
      };
      thinProvisioned = mkOption {
        type = types.bool;
        default = true;
        description = "Thin-provision the LUN.";
      };
      description = mkOption {
        type = types.str;
        default = "";
        description = "Free-text LUN description.";
      };
      canSnapshot = mkOption {
        type = types.bool;
        default = true;
        description = "Enable snapshot capability on the LUN.";
      };
    };
  };

  nfsRuleType = types.submodule {
    options = {
      client = mkOption {
        type = types.str;
        example = "192.168.3.0/24";
        description = "Allowed client: IP, CIDR, hostname, or '*'.";
      };
      access = mkOption {
        type = types.enum [
          "rw"
          "ro"
        ];
        default = "rw";
        description = "Read-write or read-only.";
      };
      squash = mkOption {
        type = types.enum [
          "root_squash"
          "all_squash"
          "no_mapping"
        ];
        default = "root_squash";
        description = "UID/GID mapping policy.";
      };
      secure = mkOption {
        type = types.bool;
        default = true;
        description = "Require connections from a privileged source port (<1024).";
      };
      async = mkOption {
        type = types.bool;
        default = false;
        description = "Allow asynchronous writes (faster, less durable).";
      };
    };
  };

  nfsExportType = types.submodule {
    options = {
      name = mkOption {
        type = types.str;
        description = "Shared-folder name to attach NFS rules to (must already exist).";
      };
      path = mkOption {
        type = types.str;
        default = "";
        description = "Optional explicit path; defaults to <volume>/<name>.";
      };
      rules = mkOption {
        type = types.listOf nfsRuleType;
        default = [ ];
        description = "NFS client access rules for this share.";
      };
    };
  };

  settingType = types.submodule {
    options = {
      api = mkOption {
        type = types.str;
        example = "SYNO.Core.FileServ.NFS";
        description = "DSM API name.";
      };
      method = mkOption {
        type = types.str;
        default = "set";
        description = "API method (usually 'set').";
      };
      version = mkOption {
        type = types.int;
        default = 1;
        description = "API version.";
      };
      params = mkOption {
        type = types.attrsOf types.str;
        default = { };
        description = "Query parameters (values are strings; use \"true\"/\"false\" for bools).";
      };
    };
  };
in
{
  options.nixfleet.synology = {
    enable = mkOption {
      type = types.bool;
      default = false;
      description = "Manage this host as a Synology NAS via the DSM API (Model B).";
    };
    host = mkOption {
      type = types.str;
      default = "";
      example = "192.168.1.67";
      description = "DSM host/IP.";
    };
    port = mkOption {
      type = types.int;
      default = 5001;
      description = "DSM API port (5001 https / 5000 http).";
    };
    https = mkOption {
      type = types.bool;
      default = true;
      description = "Use HTTPS (DSM self-signed cert is accepted).";
    };
    user = mkOption {
      type = types.str;
      default = "botuser";
      description = "DSM account for API calls. Password is sourced out-of-band (never in Nix).";
    };
    iscsiLuns = mkOption {
      type = types.listOf lunType;
      default = [ ];
      description = "Declared iSCSI LUNs (e.g. the k0s CSI backing store).";
    };
    nfsExports = mkOption {
      type = types.listOf nfsExportType;
      default = [ ];
      description = "Declared NFS exports (e.g. the k0s/backup share).";
    };
    settings = mkOption {
      type = types.listOf settingType;
      default = [ ];
      example = [
        {
          api = "SYNO.Core.FileServ.NFS";
          method = "set";
          version = 1;
          params = {
            enable_nfs = "true";
            enable_nfs_v4 = "true";
          };
        }
      ];
      description = ''
        Generic DSM API settings calls — the escape hatch for configuring any
        SYNO.Core.* setting not yet covered by a typed option. The whole DSM
        settings surface (FileServ.NFS/SMB, Network, System, Service, SNMP, …)
        uses the same entry.cgi get/set shape. Applied idempotently on --apply.
      '';
    };
  };
}
