# NixFleet ufw module
#
# Declarative UFW (Uncomplicated Firewall) rules for Ubuntu hosts.
# Renders an apply-script with a `nixfleet:` comment marker; a systemd
# path unit re-runs the script when its content changes (same pattern as
# modules/sysctl.nix).
#
# Additive: the script only manages rules whose comment carries the
# nixfleet: prefix. Pre-existing manual rules (port 22, etc.) are left
# untouched. Removing a rule from `modules.ufw.rules` deletes its
# corresponding nixfleet: rule on the next apply; user-added rules are
# never deleted by this module.
{
  config,
  pkgs,
  lib,
  ...
}:

let
  cfg = config.nixfleet.modules.ufw;

  # Render one rule's `ufw allow` invocation. Supports two shapes:
  # - port-based:  `ufw allow from <from> to any port <port> proto <proto?>`
  # - icmp:        `ufw allow from <from> to any proto icmp` (UFW handles
  #                ICMP echo via /etc/ufw/before.rules; this rule is
  #                primarily useful when the user has tightened defaults)
  ruleId = r: "${r.from}-${if r.port != null then toString r.port else r.proto}";

  renderRule =
    r:
    let
      proto = if r.proto != null then " proto ${r.proto}" else "";
      port = if r.port != null then " to any port ${toString r.port}" else " to any";
      comment = "nixfleet:${ruleId r}: ${r.comment}";
    in
    "/usr/sbin/ufw allow from ${r.from}${port}${proto} comment '${comment}'";

  # Stable id list — used by the script to know which rules to delete from
  # the live ruleset that aren't desired anymore.
  desiredIds = map ruleId cfg.rules;
in
{
  options.nixfleet.modules.ufw = {
    enable = lib.mkEnableOption "NixFleet-managed UFW rules";

    rules = lib.mkOption {
      default = [ ];
      description = ''
        List of additive UFW allow rules. Each rule renders to a
        `ufw allow from <from> ...` invocation tagged with a nixfleet:
        comment marker so the apply script can reconcile.

        Either `port` (TCP unless `proto` overrides) or `proto` must be
        set; both is fine. Use `proto = "icmp"` for ping-style rules.
      '';
      example = [
        {
          from = "10.244.0.0/16";
          port = 6443;
          comment = "k8s API from cluster pods";
        }
        {
          from = "192.168.0.0/16";
          proto = "icmp";
          comment = "ping from LAN";
        }
      ];
      type = lib.types.listOf (
        lib.types.submodule {
          options = {
            from = lib.mkOption {
              type = lib.types.str;
              description = "Source CIDR or address.";
            };
            port = lib.mkOption {
              type = lib.types.nullOr lib.types.port;
              default = null;
              description = "Destination port (omit for proto-only rules like icmp).";
            };
            proto = lib.mkOption {
              type = lib.types.nullOr lib.types.str;
              default = null;
              description = "Protocol: tcp, udp, icmp. Defaults to tcp when port is set.";
            };
            comment = lib.mkOption {
              type = lib.types.str;
              description = "Human-readable note. Surfaces in `ufw status`.";
            };
          };
        }
      );
    };
  };

  config = lib.mkIf cfg.enable {
    nixfleet.packages = with pkgs; [
      gawk
    ];

    nixfleet.files = {
      # The apply script. Path-watched; re-runs when its content changes.
      # Idempotency strategy:
      #   1. List current `nixfleet:<id>:` rules from `ufw status numbered`.
      #   2. Delete any whose <id> isn't in the desired set.
      #   3. Add desired rules (UFW dedupes identical rules silently).
      "/usr/local/bin/nixfleet-ufw-apply" = {
        mode = "0755";
        owner = "root";
        group = "root";
        text = ''
          #!/bin/bash
          # Idempotent applier for UFW rules tagged `nixfleet:<id>:`. Pure bash
          # so we don't depend on gawk's 3-arg match (Ubuntu's default awk is
          # mawk and rejects it). Stale nixfleet-marked rules whose <id> isn't
          # in the desired set get deleted; user-managed rules without the
          # marker are left alone.
          set -eu

          DESIRED_IDS=(${lib.concatStringsSep " " (map (i: "\"${i}\"") desiredIds)})

          is_desired() {
            local needle="$1"
            for id in "''${DESIRED_IDS[@]:-}"; do
              [ "$id" = "$needle" ] && return 0
            done
            return 1
          }

          # Find one stale rule's number, or empty if none. Iterating one-at-
          # a-time avoids the rule-number-shift problem after each delete.
          find_stale() {
            while IFS= read -r line; do
              [[ "$line" =~ ^\[\ *([0-9]+)\].*nixfleet:([^:]+): ]] || continue
              local num="''${BASH_REMATCH[1]}"
              local id="''${BASH_REMATCH[2]}"
              if ! is_desired "$id"; then
                echo "$num"
                return
              fi
            done < <(/usr/sbin/ufw status numbered)
          }

          while :; do
            stale=$(find_stale)
            [ -z "$stale" ] && break
            echo "[ufw] removing stale nixfleet rule #$stale"
            /usr/sbin/ufw --force delete "$stale"
          done

          # Add desired rules. UFW silently no-ops on identical re-adds.
          ${lib.concatStringsSep "\n          " (map renderRule cfg.rules)}

          /usr/sbin/ufw reload >/dev/null
          /usr/sbin/ufw status verbose | head -40
        '';
      };
    };

    nixfleet.systemd.units = {
      "nixfleet-ufw-apply.service" = {
        enabled = true;
        text = ''
          [Unit]
          Description=Apply NixFleet-managed UFW rules
          After=ufw.service network-online.target
          Wants=ufw.service network-online.target
          ConditionPathExists=/usr/local/bin/nixfleet-ufw-apply

          [Service]
          Type=oneshot
          ExecStart=/usr/local/bin/nixfleet-ufw-apply
          RemainAfterExit=yes

          [Install]
          WantedBy=multi-user.target
        '';
      };

      "nixfleet-ufw-apply.path" = {
        enabled = true;
        text = ''
          [Unit]
          Description=Watch /usr/local/bin/nixfleet-ufw-apply for changes

          [Path]
          PathChanged=/usr/local/bin/nixfleet-ufw-apply
          Unit=nixfleet-ufw-apply.service

          [Install]
          WantedBy=multi-user.target
        '';
      };
    };

    nixfleet.healthChecks = {
      nixfleet-ufw-applied = {
        type = "command";
        command = "systemctl show nixfleet-ufw-apply.service -p Result | grep -q success";
        timeout = 5;
      };
    };
  };
}
