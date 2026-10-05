# Backend regression tests.
#
# These are string assertions over the *compiled* output of a backend, not
# tests of a running host — the bugs they pin down were all "the compiler
# silently dropped something", which is exactly what you can catch without a
# machine. Wired into flake `checks`, so `nix flake check` (and CI) runs them.
{
  lib,
  pkgs,
  mkNixFleetConfiguration,
  mkDarwinFleetConfiguration,
  hostConfigs,
  hostConfigsDarwin,
}:

let
  # The text of a unit as a real host in nixfleetConfigurations declares it.
  # Asserting against the shipped hosts rather than a synthetic one is the
  # point for module-level fixes: the bug was always "the host we deploy is
  # missing this", not "the option cannot express it".
  unitText = host: unit: hostConfigs.${host}.config.nixfleet.systemd.units.${unit}.text;

  # Compile a throwaway Ubuntu host from `modules` and hand back the text of
  # its activation script.
  ubuntuActivation =
    modules:
    (mkNixFleetConfiguration {
      modules = [
        {
          nixfleet.host = {
            name = "test-host";
            base = "ubuntu";
            addr = "127.0.0.1";
          };
        }
      ]
      ++ modules;
    }).config.nixfleet.ubuntu.system.activateScript.text;

  # A check that passes when every (label, predicate) pair holds. Failures name
  # the labels that did not hold, which is the whole value of doing this in a
  # derivation rather than as a bare `assert`.
  mkCheck =
    name: assertions:
    let
      failures = lib.attrNames (lib.filterAttrs (_: ok: !ok) assertions);
    in
    pkgs.runCommand "check-${name}" { } (
      if failures == [ ] then
        "touch $out"
      else
        ''
          echo "${name}: failed expectations:" >&2
          ${lib.concatMapStringsSep "\n" (f: ''echo "  - ${f}" >&2'') failures}
          exit 1
        ''
    );

  hasSubstring = haystack: needle: lib.hasInfix needle haystack;

  # True when the first `needle` precedes the first `before`. Used for the
  # ordering assertions: "it is in the script" is not the claim, "it runs at
  # the right point" is.
  #
  # Decided by splitting on `needle` and asking whether `before` is already in
  # the prefix. Nothing here needs a character index, and the obvious
  # hand-rolled index search recurses once per character — which overflows the
  # evaluator's stack on an activation script of any real size.
  occursBefore =
    haystack: needle: before:
    hasSubstring haystack needle
    && hasSubstring haystack before
    && !(hasSubstring (lib.head (lib.splitString needle haystack)) before);

  # --- hooks.preActivate (ubuntu) -----------------------------------------
  #
  # The ubuntu backend used to compile hooks.postActivate and silently drop
  # hooks.preActivate, so modules/k0s.nix never installed the k0s binary on
  # any ubuntu worker. nixos and darwin both honoured it.
  preActivateScript = ubuntuActivation [
    {
      nixfleet.hooks = {
        preActivate = "echo PRE_ACTIVATE_MARKER";
        postActivate = "echo POST_ACTIVATE_MARKER";
      };
    }
  ];

  # --- stale unit removal (ubuntu) -----------------------------------------
  #
  # Step 7 only ever installed units. A unit dropped from the config stayed in
  # /etc/systemd/system forever, still enabled — most visibly a pull-mode
  # timer that kept firing every 15 minutes after pullMode was turned off.
  twoUnitsScript = ubuntuActivation [
    {
      nixfleet.systemd.units = {
        "keep-me.service" = {
          enabled = true;
          text = "[Service]\nExecStart=/bin/true";
        };
        "disabled-but-declared.service" = {
          enabled = false;
          text = "[Service]\nExecStart=/bin/true";
        };
      };
    }
  ];

  pullOnScript = ubuntuActivation [
    {
      nixfleet.pullMode = {
        enable = true;
        repoURL = "git@github.com:example/config.git";
        statusServer.enable = true;
      };
    }
  ];

  pullOffScript = ubuntuActivation [ ];

  # The unit list the activation writes to its ledger, parsed back out of the
  # heredoc. Membership, not equality: mkNixFleetConfiguration always imports
  # modules/nix-config.nix, whose two units are in there too and are not this
  # test's business.
  ledgerUnits =
    script:
    let
      afterOpen = lib.last (lib.splitString "cat > \"$MANAGED_UNITS_FILE\" << 'UNITS_EOF'\n" script);
      body = lib.head (lib.splitString "\nUNITS_EOF" afterOpen);
    in
    lib.filter (u: u != "") (lib.splitString "\n" body);

  # --- boot-time DNS wait (dsv4) -------------------------------------------
  #
  # dgx-spark-1 is the head, so it is the host that gets the service units.
  dsv4Unit = unitText "dgx-spark-1" "dspark-dsv4.service";
  glm53Unit = unitText "dgx-spark-1" "glm53-flash.service";

  dnsWaitFor = host: "until getent hosts ${host} >/dev/null 2>&1; do sleep 2; done";

  # --- darwin ---------------------------------------------------------------
  #
  # Compile a darwin host and hand back the assertion messages that failed.
  # The backend's whole contract now is "refuse the things you cannot do, by
  # name", so the messages are the behaviour under test.
  darwinFailures =
    modules:
    map (a: a.message) (
      lib.filter (a: !a.assertion)
        (mkDarwinFleetConfiguration {
          modules = [
            {
              nixfleet.host = {
                name = "test-mac";
                base = "darwin";
                addr = "127.0.0.1";
              };
              system.stateVersion = 4;
            }
          ]
          ++ modules;
        }).config.assertions
    );

  failsWith = modules: needle: lib.any (m: hasSubstring m needle) (darwinFailures modules);

in
{
  # Portable: needs no nix-darwin, so it runs in CI (linux) too.
  darwin-base-is-declarable = mkCheck "darwin-base-is-declarable" {
    # `darwin` was missing from the enum, so hosts/mac-1.nix could not be
    # evaluated by anything — the base the Go side has always had a case for
    # was not expressible in a host config.
    # Reading the option back is the test: the enum rejecting "darwin" is an
    # option-type error raised when the value is forced, not a silent default.
    "host.base accepts darwin" =
      (mkNixFleetConfiguration {
        modules = [
          {
            nixfleet.host = {
              name = "test-mac";
              base = "darwin";
              addr = "127.0.0.1";
            };
          }
        ];
      }).config.nixfleet.host.base == "darwin";
  };
}
// lib.optionalAttrs pkgs.stdenv.isDarwin {
  # These evaluate a real nix-darwin system, which only works on a darwin
  # builder. CI is linux-only, so they run on a dev Mac via `nix flake check`
  # rather than in the pipeline.
  darwin-host-evaluates = mkCheck "darwin-host-evaluates" {
    # The whole darwin path was unreachable: `assertions` collided with
    # nix-darwin's own declaration, `darwin` was not in the base enum, and the
    # etc translation passed a null `source` to an option typed as an absolute
    # path. Each one was a hard eval error, so no darwin host had ever been
    # built.
    "the shipped darwin host evaluates" =
      lib.isString hostConfigsDarwin.mac-1.system.drvPath && hostConfigsDarwin.mac-1.system.drvPath != "";

    "a minimal darwin host raises no assertions" = darwinFailures [ ] == [ ];
  };

  darwin-refuses-what-it-cannot-do = mkCheck "darwin-refuses-what-it-cannot-do" {
    # Each of these used to be dropped or mistranslated in silence, which on a
    # deployment tool means the apply succeeds and the host is not what the
    # config says.
    #
    # Units are the worst of them: the old translation read `enabled` and threw
    # the unit body away, compiling every declared service to a plist with no
    # program to run.
    "systemd units are refused by name" = failsWith [
      {
        nixfleet.systemd.units."web.service" = {
          enabled = true;
          text = "[Service]\nExecStart=/bin/true";
        };
      }
    ] "web.service";

    "files outside /etc are refused by name" = failsWith [
      { nixfleet.files."/opt/app/config.json".text = "{}"; }
    ] "/opt/app/config.json";

    # environment.etc deploys store symlinks, so a declared mode or owner is
    # not what lands on disk.
    "a file asking for a mode it cannot get is refused" =
      let
        modules = [
          {
            nixfleet.files."/etc/app.conf" = {
              text = "x";
              mode = "0600";
            };
          }
        ];
      in
      failsWith modules "cannot set mode/owner/group" && failsWith modules "/etc/app.conf";

    "apt is refused" = failsWith [ { nixfleet.apt.packages = [ "nginx" ]; } ] "macOS has no apt";

    # A plain /etc file with default permissions is the supported case and must
    # still go through.
    "an ordinary /etc file is accepted" =
      darwinFailures [
        { nixfleet.files."/etc/app.conf".text = "x"; }
      ] == [ ];
  };
}
// {
  dsv4-waits-for-dns = mkCheck "dsv4-waits-for-dns" {
    # network-online.target does not imply name resolution: on DGX OS
    # NetworkManager-wait-online is disabled, so the target is reached
    # immediately. glm53-flash has waited since the 2026-09-02 reboot, where it
    # asked for ghcr.io two seconds before resolved had its server list; dsv4
    # is the boot-enabled one of the pair now, so it is the exposed one.
    "dsv4 waits for the registry to resolve" = hasSubstring dsv4Unit (dnsWaitFor "ghcr.io");

    # Bounded by timeout(1) rather than a shell loop counter, because systemd
    # expands $NAME in Exec lines before sh ever sees it.
    "the wait is bounded" = hasSubstring dsv4Unit "/usr/bin/timeout 120 /bin/sh -c";

    # Ordered ahead of the GLM eviction: the other way round stops GLM and then
    # fails on DNS, leaving both models down.
    "the DNS wait precedes the GLM eviction" =
      occursBefore dsv4Unit (dnsWaitFor "ghcr.io")
        "systemctl stop glm53-flash.service";

    "the DNS wait precedes ExecStart" = occursBefore dsv4Unit (dnsWaitFor "ghcr.io") "ExecStart=/opt";

    # The module this was modelled on must keep its own wait.
    "glm53 still waits too" = hasSubstring glm53Unit (dnsWaitFor "ghcr.io");
  };

  dsv4-dns-wait-follows-image = mkCheck "dsv4-dns-wait-follows-image" {
    # The host waited on is derived from `image`, not hardcoded. That option
    # exists so the image can be repointed at a mirror, and waiting on ghcr.io
    # on a host configured not to use it would be the wrong gate.
    "a repointed image moves the wait to that registry" =
      let
        unit =
          (mkNixFleetConfiguration {
            modules = [
              ../modules/dspark-dsv4.nix
              (import ../hosts/dgx-spark-dsv4.nix { nodeRank = 0; })
              {
                nixfleet.host = {
                  name = "mirror-host";
                  base = "dgx";
                  addr = "127.0.0.1";
                };
                nixfleet.modules.dsparkDsv4.image = "registry.example.com/x/y:1@sha256:deadbeef";
              }
            ];
          }).config.nixfleet.systemd.units."dspark-dsv4.service".text;
      in
      hasSubstring unit (dnsWaitFor "registry.example.com")
      && !(hasSubstring unit (dnsWaitFor "ghcr.io"));
  };

  stale-units-removed = mkCheck "stale-units-removed" {
    # The ledger is the mechanism: without a record of what the previous
    # generation installed there is nothing safe to diff against.
    "the previous generation's unit ledger is read" =
      hasSubstring twoUnitsScript ''MANAGED_UNITS_FILE="$NIXFLEET_STATE/managed-units"'';

    "units absent from the config are disabled and deleted" =
      hasSubstring twoUnitsScript "Removing stale unit"
      && hasSubstring twoUnitsScript ''rm -f "/etc/systemd/system/$unit"'';

    # Scanning /etc/systemd/system instead of the ledger would delete units
    # NixFleet does not own. That is the one failure mode worse than the bug.
    "removal never scans /etc/systemd/system" =
      !(hasSubstring twoUnitsScript "ls /etc/systemd/system")
      && !(hasSubstring twoUnitsScript "/etc/systemd/system/*");

    # Both declared units must survive, including the one declared
    # `enabled = false` — that is "installed and disabled", not "retired".
    "declared units are in the new ledger" =
      lib.elem "keep-me.service" (ledgerUnits twoUnitsScript)
      && lib.elem "disabled-but-declared.service" (ledgerUnits twoUnitsScript);

    "the ledger is removal's only input, and is rewritten after" =
      occursBefore twoUnitsScript "Removing stale unit"
        ''cat > "$MANAGED_UNITS_FILE"'';

    # Removal has to precede the daemon-reload, or systemd keeps serving the
    # unit it was just told to forget.
    "stale removal happens before daemon-reload" =
      occursBefore twoUnitsScript "Removing stale unit"
        "Reloading systemd daemon...";

    "a removal alone triggers the daemon-reload" =
      hasSubstring twoUnitsScript ''[ -n "$CHANGED_UNITS$REMOVED_UNITS" ]'';
  };

  stale-units-cover-pullmode = mkCheck "stale-units-cover-pullmode" {
    # pullMode's units are installed outside nixfleet.systemd.units, so they
    # have to be listed in the ledger explicitly or disabling pullMode leaves
    # the timer behind — the symptom that motivated this.
    "pull mode units are in the ledger while pull mode is on" =
      lib.subtractLists (ledgerUnits pullOnScript) [
        "nixfleet-pull.service"
        "nixfleet-pull.timer"
        "nixfleet-status.service"
      ] == [ ];

    "the status server unit is dropped from the ledger when it is off" =
      !(lib.elem "nixfleet-status.service" (
        ledgerUnits (ubuntuActivation [
          {
            nixfleet.pullMode = {
              enable = true;
              repoURL = "git@github.com:example/config.git";
            };
          }
        ])
      ));

    # The point of the whole change: with pullMode off these are absent from
    # the new ledger, so the previous generation's copy makes them stale and
    # step 7b deletes them.
    "no pull units in the ledger once pull mode is off" =
      lib.intersectLists (ledgerUnits pullOffScript) [
        "nixfleet-pull.service"
        "nixfleet-pull.timer"
        "nixfleet-status.service"
      ] == [ ];

    # The ledger starts empty, so a host that already carries orphaned pull
    # units has nothing to diff against. Their names are NixFleet's own, so
    # they are swept without the ledger — otherwise the hosts that actually
    # have the bug today would keep it.
    "retired pull units are candidates without a ledger" =
      hasSubstring pullOffScript ''STALE_CANDIDATES="nixfleet-pull.service nixfleet-pull.timer nixfleet-status.service"'';

    # ...and are not swept while pull mode is on, which would uninstall the
    # units step 10 just installed.
    "pull units are not candidates while pull mode is on" =
      hasSubstring pullOnScript ''STALE_CANDIDATES=""'';

    # Pull mode applies from inside nixfleet-pull.service, so a blanket
    # `disable --now` over stale units would have the script stop itself
    # halfway through turning pull mode off.
    "the unit this activation runs under is not stopped" =
      hasSubstring pullOffScript "SELF_UNIT=" && hasSubstring pullOffScript ''"$unit" = "$SELF_UNIT"'';
  };

  hooks-preactivate-runs = mkCheck "hooks-preactivate-runs" {
    "preActivate body is compiled into the activation script" =
      hasSubstring preActivateScript "echo PRE_ACTIVATE_MARKER";

    "preActivate runs before the system profile is installed" =
      occursBefore preActivateScript "echo PRE_ACTIVATE_MARKER"
        ''nix-env --profile "$SYSTEM_LINK"'';

    "preActivate runs before systemd units are deployed" =
      occursBefore preActivateScript "echo PRE_ACTIVATE_MARKER"
        "Deploying systemd units...";

    "postActivate still runs, and after preActivate" =
      occursBefore preActivateScript "echo PRE_ACTIVATE_MARKER"
        "echo POST_ACTIVATE_MARKER";
  };

  hooks-empty-by-default = mkCheck "hooks-empty-by-default" {
    # An unset hook must not emit a hook block at all. Guards against a fix
    # that logs "Running pre-activate hook..." on every host whether or not
    # one is declared. Matches the log line, not the step comment, which is
    # unconditional by design.
    "no pre-activate block when the hook is unset" =
      !(hasSubstring (ubuntuActivation [ ]) ''log "Running pre-activate hook..."'');

    "the pre-activate block is emitted when a hook is declared" =
      hasSubstring preActivateScript ''log "Running pre-activate hook..."'';
  };
}
