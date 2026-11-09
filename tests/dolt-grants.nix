{
  pkgs,
  lib,
  mkNixFleetConfiguration,
}:
let
  host = mkNixFleetConfiguration {
    system = pkgs.stdenv.hostPlatform.system;
    modules = [
      ../modules/dolt.nix
      {
        nixfleet.host = {
          name = "test";
          base = "ubuntu";
          addr = "127.0.0.1";
        };
        nixfleet.modules.dolt = {
          enable = true;
          password = "fixture'password\\value";
          dataDir = "/tmp/nixfleet-dolt-test-data";
        };
      }
    ];
  };
  grantPath = lib.head (
    lib.splitString "\n" (
      lib.last (
        lib.splitString "ExecStartPre=" host.config.nixfleet.systemd.units."dolt-server.service".text
      )
    )
  );
  script = pkgs.writeText "test-dolt-grants" (
    lib.replaceStrings [ "/tmp/nixfleet-dolt-test-data" ] [ "$TMPDIR/data" ] (
      builtins.readFile grantPath
    )
  );
in
pkgs.runCommand "check-dolt-remote-auth" { nativeBuildInputs = [ pkgs.dolt ]; } ''
  export HOME="$TMPDIR/home"
  mkdir -p "$HOME" "$TMPDIR/data"
  # Offline SQL only, no listener or network client.
  bash ${script}
  authenticated=$(dolt --data-dir "$TMPDIR/data" sql -r csv -q "SELECT COUNT(*) FROM mysql.user WHERE User='root' AND Host='%' AND authentication_string='*43DF956D45A474EC4E8C8EA4DB2B040B63E8C5B3';" | tail -n1)
  test "$authenticated" = 1
  before=$(dolt --data-dir "$TMPDIR/data" sql -r csv -q "SELECT authentication_string FROM mysql.user WHERE User='root' AND Host='%';")
  # A changed existing password must survive repeated pre-start provisioning.
  dolt --data-dir "$TMPDIR/data" sql -q "ALTER USER 'root'@'%' IDENTIFIED BY 'existing-authenticated-client';" >/dev/null
  existing=$(dolt --data-dir "$TMPDIR/data" sql -r csv -q "SELECT authentication_string FROM mysql.user WHERE User='root' AND Host='%';")
  test "$before" != "$existing"
  bash ${script}
  after=$(dolt --data-dir "$TMPDIR/data" sql -r csv -q "SELECT authentication_string FROM mysql.user WHERE User='root' AND Host='%';")
  test "$existing" = "$after"
  touch "$out"
''
