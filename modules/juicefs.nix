# NixFleet JuiceFS Client Module
#
# Mounts the shared JuiceFS filesystem at /mnt/fleet on a NixFleet host.
# Fetches the encryption keypair from 1Password Connect at mount time
# (via the existing CF tunnel + op-proxy pattern), stages it in tmpfs,
# and shreds after mount. Key never touches persistent disk.
#
# Applicability:
#   - Linux hosts only (systemd). macOS (mac-1) needs a separate LaunchDaemon.
#   - gti: works out of the box — can reach cluster.local via local CoreDNS.
#   - gtr-150..153: needs PG + MinIO exposed via NodePort or CF tunnel
#     (override metaUrlOverride + bucketUrl accordingly).
#
# Pre-deploy requirements (see .claude/juicefs-manual-steps.md in nixfleet repo):
#   - 1Password item "juicefs-encryption-key" in "Personal Agents" vault
#   - Age-encrypted secret deployed to host at /etc/nixfleet/juicefs-op-connect.env
#     with OP_CONNECT_TOKEN, CF_ACCESS_CLIENT_ID, CF_ACCESS_CLIENT_SECRET
#   - Age-encrypted secret at /etc/nixfleet/juicefs-metaurl with full postgres:// URL
{
  config,
  pkgs,
  lib,
  ...
}:

let
  cfg = config.nixfleet.modules.juicefs;
in
{
  options.nixfleet.modules.juicefs = {
    enable = lib.mkEnableOption "JuiceFS client mount at /mnt/fleet";

    mountPoint = lib.mkOption {
      type = lib.types.str;
      default = "/mnt/fleet";
      description = "Where to mount the JuiceFS filesystem";
    };

    cacheDir = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/juicefs/cache";
      description = "Local NVMe cache directory. Must be on fast storage.";
    };

    cacheSizeMiB = lib.mkOption {
      type = lib.types.int;
      default = 51200;
      description = ''
        Local cache size in MiB. Defaults to 50 GiB.
        Recommended: gti 100 GiB (102400), gtr-150..153 50 GiB (51200).
      '';
    };

    writeback = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        Enable writeback cache. Writes return as soon as they hit the local
        cache; upload to object storage is async. Huge perf win but a
        full-node crash can lose the last few seconds of writes.
      '';
    };

    bucketUrl = lib.mkOption {
      type = lib.types.str;
      default = "http://juicefs-minio.juicefs-system.svc.cluster.local:9000/fleet";
      description = ''
        JuiceFS S3 bucket URL (MinIO endpoint).
        Default works on k0s nodes. For off-cluster hosts, override to
        a NodePort or CF tunnel endpoint (e.g. http://gti.stigen.lan:32900/fleet).
      '';
    };

    opConnectHost = lib.mkOption {
      type = lib.types.str;
      default = "https://op.nixfleet.private.stigen.ai";
      description = "1Password Connect endpoint (via CF tunnel + op-proxy)";
    };

    vaultName = lib.mkOption {
      type = lib.types.str;
      default = "Personal Agents";
      description = "1Password vault holding juicefs-encryption-key";
    };

    keyItemName = lib.mkOption {
      type = lib.types.str;
      default = "juicefs-encryption-key";
      description = "1Password item title for the RSA keypair";
    };
  };

  config = lib.mkIf cfg.enable {
    nixfleet.packages = with pkgs; [
      juicefs
      curl
      jq
      coreutils
    ];

    nixfleet.directories = {
      "${cfg.cacheDir}" = {
        mode = "0700";
        owner = "root";
        group = "root";
      };
      "${cfg.mountPoint}" = {
        mode = "0755";
        owner = "root";
        group = "root";
      };
    };

    nixfleet.files = {
      # 1Password Connect + CF Access credentials. Age-encrypted in the secrets
      # system; deployed by `nixfleet secrets deploy`. Format:
      #   OP_CONNECT_TOKEN=ops_...
      #   CF_ACCESS_CLIENT_ID=<id>.access
      #   CF_ACCESS_CLIENT_SECRET=<secret>
      "/etc/nixfleet/juicefs-op-connect.env" = {
        mode = "0600";
        owner = "root";
        group = "root";
        text = "# Placeholder — deploy with: nixfleet secrets deploy\n";
      };

      # Full postgres:// URL for the JuiceFS metadata DB. Age-encrypted.
      # Example: postgres://juicefs:PW@gti.stigen.lan:32432/juicefs?sslmode=disable
      "/etc/nixfleet/juicefs-metaurl" = {
        mode = "0600";
        owner = "root";
        group = "root";
        text = "# Placeholder — deploy with: nixfleet secrets deploy\n";
      };

      # Key-fetch helper: pulls juicefs-encryption-key from 1P Connect via CF
      # tunnel, writes PEM + passphrase to tmpfs at /run/juicefs/.
      "/usr/local/bin/juicefs-fetch-key" = {
        mode = "0755";
        owner = "root";
        group = "root";
        text = ''
          #!/bin/bash
          set -euo pipefail

          OP_HOST="${cfg.opConnectHost}"
          VAULT_NAME="${cfg.vaultName}"
          ITEM_NAME="${cfg.keyItemName}"
          OUT_DIR=/run/juicefs

          mkdir -p "$OUT_DIR"
          chmod 0700 "$OUT_DIR"

          # Load Connect + CF Access credentials into env.
          set -a
          # shellcheck disable=SC1091
          source /etc/nixfleet/juicefs-op-connect.env
          set +a

          : "''${OP_CONNECT_TOKEN:?missing in juicefs-op-connect.env}"
          : "''${CF_ACCESS_CLIENT_ID:?missing in juicefs-op-connect.env}"
          : "''${CF_ACCESS_CLIENT_SECRET:?missing in juicefs-op-connect.env}"

          auth=(-sSfL \
            -H "X-OP-Token: ''${OP_CONNECT_TOKEN}" \
            -H "CF-Access-Client-Id: ''${CF_ACCESS_CLIENT_ID}" \
            -H "CF-Access-Client-Secret: ''${CF_ACCESS_CLIENT_SECRET}")

          # Resolve vault + item IDs.
          VAULT_ID=$(curl "''${auth[@]}" "$OP_HOST/v1/vaults" \
            | jq -r --arg n "$VAULT_NAME" '.[] | select(.name==$n) | .id' | head -n1)
          [ -n "$VAULT_ID" ] || { echo "vault '$VAULT_NAME' not found"; exit 1; }

          # URL-encode the item title for the filter query.
          ITEM_NAME_ENC=$(jq -rn --arg v "$ITEM_NAME" '$v | @uri')
          ITEM_ID=$(curl "''${auth[@]}" \
            "$OP_HOST/v1/vaults/$VAULT_ID/items?filter=title%20eq%20%22$ITEM_NAME_ENC%22" \
            | jq -r '.[0].id // empty')
          [ -n "$ITEM_ID" ] || { echo "item '$ITEM_NAME' not found in '$VAULT_NAME'"; exit 1; }

          # Fetch full item with all field values.
          ITEM_JSON=$(curl "''${auth[@]}" "$OP_HOST/v1/vaults/$VAULT_ID/items/$ITEM_ID")

          echo "$ITEM_JSON" \
            | jq -r '.fields[] | select(.label=="private-key") | .value' \
            > "$OUT_DIR/fleet-key.pem"
          echo "$ITEM_JSON" \
            | jq -r '.fields[] | select(.label=="passphrase") | .value' \
            > "$OUT_DIR/passphrase"

          # Sanity: non-empty + PEM header.
          [ -s "$OUT_DIR/fleet-key.pem" ] || { echo "empty PEM"; exit 1; }
          grep -q "BEGIN RSA PRIVATE KEY" "$OUT_DIR/fleet-key.pem" \
            || grep -q "BEGIN ENCRYPTED PRIVATE KEY" "$OUT_DIR/fleet-key.pem" \
            || { echo "not a valid PEM file"; exit 1; }
          [ -s "$OUT_DIR/passphrase" ] || { echo "empty passphrase"; exit 1; }

          chmod 0400 "$OUT_DIR/fleet-key.pem" "$OUT_DIR/passphrase"
        '';
      };

      # Mount wrapper: sources passphrase, mounts, shreds key material.
      "/usr/local/bin/juicefs-mount-fleet" = {
        mode = "0755";
        owner = "root";
        group = "root";
        text = ''
          #!/bin/bash
          set -euo pipefail

          MOUNT_POINT="${cfg.mountPoint}"
          CACHE_DIR="${cfg.cacheDir}"
          CACHE_SIZE="${toString cfg.cacheSizeMiB}"
          WRITEBACK="${if cfg.writeback then "--writeback" else ""}"

          # Fetch key + passphrase to tmpfs.
          /usr/local/bin/juicefs-fetch-key

          # Read + export passphrase for juicefs.
          export JFS_RSA_PASSPHRASE
          JFS_RSA_PASSPHRASE=$(cat /run/juicefs/passphrase)

          # Read metaurl (age-decrypted secret).
          META_URL=$(head -n1 /etc/nixfleet/juicefs-metaurl)

          # Mount in background mode.
          juicefs mount \
            --encrypt-rsa-key /run/juicefs/fleet-key.pem \
            --cache-dir "$CACHE_DIR" \
            --cache-size "$CACHE_SIZE" \
            $WRITEBACK \
            --background \
            "$META_URL" \
            "$MOUNT_POINT"

          # Wipe staged key material (juicefs holds it in process memory).
          shred -u /run/juicefs/fleet-key.pem /run/juicefs/passphrase 2>/dev/null || true
          unset JFS_RSA_PASSPHRASE META_URL
        '';
      };
    };

    nixfleet.systemd.units = {
      "juicefs-fleet.service" = {
        enabled = true;
        text = ''
          [Unit]
          Description=JuiceFS mount at ${cfg.mountPoint}
          After=network-online.target
          Wants=network-online.target

          [Service]
          Type=forking
          RuntimeDirectory=juicefs
          RuntimeDirectoryMode=0700
          ExecStart=/usr/local/bin/juicefs-mount-fleet
          ExecStop=${pkgs.juicefs}/bin/juicefs umount ${cfg.mountPoint}
          Restart=on-failure
          RestartSec=30s
          TimeoutStartSec=120s

          [Install]
          WantedBy=multi-user.target
        '';
      };
    };

    nixfleet.healthChecks = {
      juicefs-fleet-mount = {
        type = "command";
        command = "mountpoint -q ${cfg.mountPoint}";
        timeout = 5;
      };
    };
  };
}
