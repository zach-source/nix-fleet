{
  lib,
  pkgs,
  ubuntuActivation,
}:
let
  strict = ubuntuActivation [
    {
      nixfleet.safeActivationPaths = true;
      nixfleet.files."/fixture/file".text = "same";
      nixfleet.directories."/fixture/directory" = { };
    }
  ];
  legacy = ubuntuActivation [ ];
  strictEmpty = ubuntuActivation [ { nixfleet.safeActivationPaths = true; } ];
  hashLine = script: lib.findFirst (lib.hasInfix "Manifest hash:") "" (lib.splitString "\n" script);
  helper = pkgs.writeText "activation-path-check-test" (
    builtins.replaceStrings [ "@stat@" ] [ "fixture_stat" ] (
      builtins.readFile ../lib/activation-paths.sh
    )
  );
in
assert lib.hasInfix "nixfleet_check_path /fixture/file" strict;
assert lib.hasInfix "nixfleet_check_path /fixture/directory" strict;
assert lib.hasInfix "(umask 077; mkdir -p /fixture/directory)" strict;
assert lib.hasInfix "(umask 077; mkdir -p /fixture)" strict;
assert !(lib.hasInfix "nixfleet_check_path" legacy);
assert hashLine legacy != hashLine strictEmpty;
pkgs.runCommand "check-safe-activation-paths" { } ''
  # Model root ownership inside the unprivileged builder. Permission bits,
  # symlinks and traversal are real filesystem fixtures.
  fixture_stat() {
    local path="''${@: -1}" uid=0 mode=755
    if [[ "$path" == "$PWD"* ]]; then
      mode=$(stat -c %a "$path")
    fi
    [[ "$path" != "$PWD/untrusted" ]] || uid=1000
    printf '%s %s\n' "$uid" "$mode"
  }
  source ${helper}
  mkdir trusted writable untrusted
  chmod 0755 trusted untrusted
  chmod 0770 writable
  nixfleet_check_path "$PWD/trusted/new/file"
  touch trusted/existing
  nixfleet_check_path "$PWD/trusted/existing"
  ln -s "$PWD/trusted" link
  ln -s "$PWD/trusted/existing" trusted/link
  for path in \
    "$PWD/link/new" "$PWD/trusted/link" \
    "$PWD/writable/file" "$PWD/untrusted/file" \
    "$PWD/trusted/../file" "$PWD/trusted/./file" \
    "$PWD/trusted//file" relative /; do
    if nixfleet_check_path "$path"; then
      echo "unsafe path accepted: $path" >&2
      exit 1
    fi
  done
  touch "$out"
''
