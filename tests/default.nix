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

  # True when `needle` appears before `before` in `haystack`. Used for the
  # ordering assertions: "it is in the script" is not the claim, "it runs at
  # the right point" is.
  occursBefore =
    haystack: needle: before:
    let
      i = indexOfSubstring haystack needle;
      j = indexOfSubstring haystack before;
    in
    i != null && j != null && i < j;

  # Index of the first occurrence of `needle` in `s`, or null. lib has no
  # such function and a regex cannot report a position.
  indexOfSubstring =
    s: needle:
    let
      n = lib.stringLength s;
      k = lib.stringLength needle;
      go =
        i:
        if i + k > n then
          null
        else if lib.substring i k s == needle then
          i
        else
          go (i + 1);
    in
    go 0;

  hasSubstring = haystack: needle: indexOfSubstring haystack needle != null;

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

in
{
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
