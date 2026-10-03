# Restarts a local model endpoint that has stopped answering.
#
# systemd's Restart=on-failure only sees a dead process, and every real outage
# this fleet has had left the process (or the unit) reporting healthy:
#   - dspark-dsv4 is Type=oneshot + RemainAfterExit around docker compose, so
#     systemd says `active` indefinitely while docker restart-loops a rank.
#     2026-09-29: rank 0 hit RestartCount=94 over a day, `systemctl is-active`
#     green the whole time, and LiteLLM served hard 500s for the REASONING tier.
#   - llama-server can wedge on the shared iGPU with the port still open.
#
# Producers (modules/llm-inference.nix, modules/dspark-*.nix) register their own
# probes, so a host inherits this by declaring models — there is nothing to add
# per host.
{ config, lib, ... }:

let
  inherit (lib)
    mkOption
    mkIf
    types
    mapAttrsToList
    concatStringsSep
    escapeShellArg
    ;
  cfg = config.nixfleet.modules.modelWatchdog;

  probeType = types.submodule {
    options = {
      url = mkOption {
        type = types.str;
        description = "Readiness URL on loopback. Non-2xx or timeout counts as down.";
      };
      unit = mkOption {
        type = types.str;
        description = "Systemd unit to restart. Probed only while it is active.";
      };
      graceSec = mkOption {
        type = types.int;
        default = 300;
        description = ''
          Seconds after the unit becomes active during which a failing probe is
          ignored, i.e. the model's load time. Only ever delays the FIRST probe
          after a (re)start, so being generous is nearly free — an outage on a
          long-active unit is still caught on the next tick.
        '';
      };
      maxRestartsPerHour = mkOption {
        type = types.int;
        default = 2;
        description = ''
          After this many restarts in a rolling hour the watchdog gives up and
          logs loudly. Reproducing a 94-restart loop is worse than being down
          visibly.
        '';
      };
    };
  };

  # probe_one <name> <url> <unit> <graceSec> <max>
  probeCalls = concatStringsSep "\n" (
    mapAttrsToList (
      name: p:
      "probe_one ${escapeShellArg name} ${escapeShellArg p.url} ${escapeShellArg p.unit} "
      + "${toString p.graceSec} ${toString p.maxRestartsPerHour}"
    ) cfg.probes
  );

in
{
  options.nixfleet.modules.modelWatchdog = {
    enable = mkOption {
      type = types.bool;
      default = cfg.probes != { };
      description = "Defaults on once anything registers a probe; set false to opt a host out.";
    };

    intervalSec = mkOption {
      type = types.int;
      default = 60;
      description = "Seconds between sweeps.";
    };

    probes = mkOption {
      type = types.attrsOf probeType;
      default = { };
      description = "Endpoints to watch, keyed by a short name used for state files and logs.";
    };
  };

  config = mkIf cfg.enable {
    nixfleet.files."/usr/local/bin/model-watchdog" = {
      mode = "0755";
      owner = "root";
      group = "root";
      text = ''
        #!/bin/bash
        # Managed by NixFleet — modules/model-watchdog.nix (do not edit).
        #
        # DRY_RUN=1 model-watchdog  — report only, restart nothing.
        #
        # NOT `set -e`: one unhealthy probe must not abort the rest of the sweep.
        set -uo pipefail

        STATE=/var/lib/nixfleet/model-watchdog
        mkdir -p "$STATE"

        # Both writes are load-bearing, and yes, `journalctl -t model-watchdog`
        # therefore shows each line twice. journald runs with ForwardToSyslog=no
        # on these hosts, so `logger` (straight to /dev/log -> rsyslog -> Loki)
        # is the only path off the box, while the echo is what `journalctl -u
        # model-watchdog` and a DRY_RUN on a terminal show. Deleting the logger
        # to tidy the duplicate would silently take the fleet's only view of
        # this with it.
        log() { logger -t model-watchdog -- "$*"; echo "$*"; }

        probe_one() {
          local name=$1 url=$2 unit=$3 grace=$4 max=$5

          # Inactive means deliberately stopped — or losing the mutual exclusion
          # with a sibling on the same port, as dspark-dsv4 and glm53-flash do.
          # Either way it is not ours to start.
          if ! systemctl is-active --quiet "$unit"; then
            [ -n "''${DRY_RUN:-}" ] && echo "$name: SKIP ($unit inactive)"
            return 0
          fi

          local since age now
          now=$(date +%s)
          since=$(date -d "$(systemctl show -P ActiveEnterTimestamp "$unit")" +%s 2>/dev/null || echo 0)
          age=$(( now - since ))

          if curl -sf -m 10 -o /dev/null "$url"; then
            [ -n "''${DRY_RUN:-}" ] && echo "$name: OK ($url, active ''${age}s)"
            return 0
          fi

          # Still loading. A cold dsv4 mmaps 156GB of weights before it answers.
          if [ "$since" -gt 0 ] && [ "$age" -lt "$grace" ]; then
            log "$name loading ($url not ready, ''${age}s < ''${grace}s grace) — leaving alone"
            return 0
          fi

          # A single timed-out probe under load is not an outage.
          sleep 5
          curl -sf -m 10 -o /dev/null "$url" && return 0

          # Flap guard: prune the rolling hour, then count what is left.
          local f="$STATE/$name.restarts" cutoff n
          cutoff=$(( now - 3600 ))
          n=0
          if [ -f "$f" ]; then
            awk -v c="$cutoff" '$1 > c' "$f" > "$f.tmp" && mv "$f.tmp" "$f"
            n=$(wc -l < "$f")
          fi
          if [ "$n" -ge "$max" ]; then
            log "GIVING UP on $name: $url still down after $n restarts of $unit this hour — needs a human"
            return 1
          fi

          log "$name DOWN ($url, $unit active ''${age}s) — restarting $unit (attempt $(( n + 1 ))/$max this hour)"
          if [ -n "''${DRY_RUN:-}" ]; then
            echo "$name: DRY_RUN, not restarting"
            return 0
          fi
          echo "$now" >> "$f"
          systemctl restart "$unit" || log "restart of $unit FAILED"
        }

        ${probeCalls}
      '';
    };

    nixfleet.systemd.units = {
      "model-watchdog.service" = {
        enabled = true;
        text = ''
          [Unit]
          Description=Restart local model endpoints that stop answering

          [Service]
          Type=oneshot
          ExecStart=/usr/local/bin/model-watchdog
          # Each probe can spend 10s + 5s + 10s before giving a verdict, and a
          # dsv4 restart stops both ranks over SSH before starting them.
          TimeoutStartSec=1800
        '';
      };

      "model-watchdog.timer" = {
        enabled = true;
        text = ''
          [Unit]
          Description=Sweep local model endpoints every ${toString cfg.intervalSec}s

          [Timer]
          # Not OnBootSec=0: at boot the models are staggered and loading, and
          # every probe would be inside its grace window anyway.
          OnBootSec=5min
          OnUnitActiveSec=${toString cfg.intervalSec}
          AccuracySec=10s

          [Install]
          WantedBy=timers.target
        '';
      };
    };

    nixfleet.healthChecks.model-watchdog-timer = {
      type = "command";
      command = "systemctl is-active --quiet model-watchdog.timer";
      timeout = 5;
    };
  };
}
