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
}:

let
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

in
{
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
