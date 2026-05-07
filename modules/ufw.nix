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
    # Insert at position 1 so nixfleet allow rules take precedence over any
    # pre-existing `limit`/deny rules the user (or Ubuntu defaults) may have
    # appended. Without this, e.g. `limit 22/tcp` will tarpit-drop SSH from
    # LAN sources before our explicit `allow from 192.168.0.0/16 ... 22` is
    # consulted, since UFW evaluates in numbered order. UFW silently no-ops
    # on identical re-inserts, so this stays idempotent.
    "/usr/sbin/ufw insert 1 allow from ${r.from}${port}${proto} comment '${comment}'";

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
          # Idempotent applier for UFW rules tagged `nixfleet:<id>:`. Pure
          # bash (no gawk 3-arg match — Ubuntu's mawk rejects it).
          #
          # Strategy: flush all nixfleet-marked rules, then re-insert from
          # the desired set. Each insert goes to position 1 so nixfleet
          # rules outrank any pre-existing user `limit`/deny rules (e.g.
          # Ubuntu's default `limit 22/tcp`). User-managed rules without
          # the marker are never touched.
          set -eu

          # Find one nixfleet-marked rule number, or empty if none. We
          # delete one at a time because each deletion shifts the numbering.
          find_nixfleet() {
            while IFS= read -r line; do
              [[ "$line" =~ ^\[\ *([0-9]+)\].*nixfleet: ]] || continue
              echo "''${BASH_REMATCH[1]}"
              return
            done < <(/usr/sbin/ufw status numbered)
          }

          while :; do
            n=$(find_nixfleet)
            [ -z "$n" ] && break
            /usr/sbin/ufw --force delete "$n" >/dev/null
          done

          # Re-insert desired rules. Inserting in reverse so the first rule
          # in cfg.rules ends up at the lowest position number after all
          # inserts have shifted prior entries down.
          ${lib.concatStringsSep "\n          " (lib.reverseList (map renderRule cfg.rules))}

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
