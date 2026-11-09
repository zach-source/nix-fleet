{
  lib,
  pkgs,
  mkNixFleetConfiguration,
}:
let
  host = mkNixFleetConfiguration {
    system = pkgs.stdenv.hostPlatform.system;
    modules = [
      {
        nixfleet.host = {
          name = "fixture";
          base = "ubuntu";
          addr = "127.0.0.1";
        };
      }
    ];
  };
  activation = host.config.nixfleet.ubuntu.system.activateScript.text;
  rawFragment = lib.head (
    lib.splitString "# Record what this generation" (
      lib.last (lib.splitString ''log "Updating state..."'' activation)
    )
  );
  fragment = lib.concatStringsSep "\n" (
    map (line: if lib.hasInfix "JQ=" line then "JQ=jq" else line) (lib.splitString "\n" rawFragment)
  );
  writer = pkgs.writeShellScript "state-writer" ''
    set -eu
    NIXFLEET_STATE="$1"
    SYSTEM_LINK="$1/system"
    SYSTEM_PATH=/nix/store/fixture
    ${builtins.unsafeDiscardStringContext fragment}
  '';
in
pkgs.runCommand "check-state-commit"
  {
    nativeBuildInputs = [
      pkgs.coreutils
      pkgs.jq
    ];
  }
  ''
    mkdir state
    ln -s system-42-link state/system
    echo '{"state_version":1,"future_field":{"kept":true}}' > state/state.json
    ${writer} "$PWD/state"
    jq -e '.current_generation == 42 and .future_field.kept' state/state.json
    for invalid in '{broken' 'null' '{"state_version":99}'; do
      printf '%s' "$invalid" > state/state.json
      cp state/state.json before
      if ${writer} "$PWD/state"; then echo 'invalid state accepted'; exit 1; fi
      cmp state/state.json before
      test ! -d state/.state.lock
    done
    touch "$out"
  ''
