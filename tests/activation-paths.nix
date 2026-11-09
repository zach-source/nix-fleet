{
  lib,
  pkgs,
  ubuntuActivation,
}:
let
  script = ubuntuActivation [
    {
      nixfleet.files."/fixture/managed" = {
        text = "same";
        mode = "0600";
      };
    }
  ];
  step = lib.head (lib.splitString "# Step 7:" (lib.last (lib.splitString "# Step 6:" script)));
  # Execute the emitted managed-file block on a private fixture, without root
  # operations. chown is recorded because Nix builds cannot change ownership.
  fixture = builtins.unsafeDiscardStringContext ("# Step 6:" + step);

in
pkgs.runCommand "check-activation-permissions" { nativeBuildInputs = [ pkgs.python3 ]; } ''
  cat > block <<'BLOCK'
  ${fixture}
  BLOCK
  python3 - <<'PY'
  from pathlib import Path
  import re
  p = Path('block')
  s = p.read_text()
  s = re.sub(r'"/nix/store/[^"\n]+/fixture/managed"', '"$PWD/source"', s)
  s = s.replace('/fixture/managed', str(Path('managed').resolve()))
  p.write_text(s)
  PY
  printf same > source
  cp source managed
  chmod 0644 managed
  log() { :; }
  chown() { printf '%s\n' "$*" >> ownership; }
  source block
  test "$(stat -c %a managed)" = 600
  test -s ownership
  touch $out
''
