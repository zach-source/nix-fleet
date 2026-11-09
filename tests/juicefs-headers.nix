{
  pkgs,
  lib,
  mkNixFleetConfiguration,
}:
let
  host = mkNixFleetConfiguration {
    modules = [
      ../modules/juicefs.nix
      {
        nixfleet.host = {
          name = "test";
          base = "ubuntu";
          addr = "127.0.0.1";
        };
        nixfleet.modules.juicefs.enable = true;
      }
    ];
  };
  helper = pkgs.writeText "juicefs-fetch-key-test" (
    lib.replaceStrings
      [ "/run/juicefs" "/etc/nixfleet/juicefs-op-connect.env" ]
      [ "$TMPDIR/juicefs" "$TMPDIR/connect.env" ]
      host.config.nixfleet.files."/usr/local/bin/juicefs-fetch-key".text
  );
in
pkgs.runCommand "check-juicefs-private-headers"
  {
    nativeBuildInputs = [
      pkgs.bash
      pkgs.jq
    ];
  }
  ''
    cat > "$TMPDIR/connect.env" <<'ENV'
    OP_CONNECT_TOKEN=fixture-token
    CF_ACCESS_CLIENT_ID=fixture-id
    CF_ACCESS_CLIENT_SECRET=fixture-secret
    ENV
    curl() {
      [[ "$*" != *fixture-* ]] || return 80
      [[ "$2" == "-H" && "$3" == @* ]] || return 81
      header="''${3#@}"
      [[ "$(stat -c %a "$header")" == 600 ]] || return 82
      grep -qx 'X-OP-Token: fixture-token' "$header" || return 83
      grep -qx 'CF-Access-Client-Id: fixture-id' "$header" || return 84
      grep -qx 'CF-Access-Client-Secret: fixture-secret' "$header" || return 85
      [[ "''${FAIL_CURL:-}" != 1 ]] || return 86
      case "''${@: -1}" in
        */v1/vaults) echo '[{"name":"Personal Agents","id":"vault"}]' ;;
        *filter=*) echo '[{"id":"item"}]' ;;
        */items/item) echo '{"fields":[{"label":"private-key","value":"BEGIN RSA PRIVATE KEY"},{"label":"passphrase","value":"fixture-passphrase"}]}' ;;
        *) return 87 ;;
      esac
    }
    export -f curl
    bash ${helper}
    test -s "$TMPDIR/juicefs/fleet-key.pem"
    test -z "$(find "$TMPDIR/juicefs" -name 'headers.*')"
    if FAIL_CURL=1 bash ${helper}; then
      echo "curl failure was ignored" >&2
      exit 1
    fi
    test -z "$(find "$TMPDIR/juicefs" -name 'headers.*')"
    touch "$out"
  ''
