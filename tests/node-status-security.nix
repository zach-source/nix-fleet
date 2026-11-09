{
  pkgs,
  mkNixFleetConfiguration,
}:
let
  artifacts =
    settings:
    (mkNixFleetConfiguration {
      system = pkgs.stdenv.hostPlatform.system;
      modules = [
        {
          nixfleet.host = {
            name = "status-test";
            base = "ubuntu";
            addr = "127.0.0.1";
          };
          nixfleet.pullMode = {
            enable = true;
            repoURL = "https://example.invalid/config.git";
            statusServer = {
              enable = true;
            }
            // settings;
          };
        }
      ];
    }).system.pullMode;
  plain = artifacts { };
  protected = artifacts {
    apiTokenFile = "/run/credentials/status token";
    tlsCertFile = "/run/credentials/status.crt";
    tlsKeyFile = "/run/credentials/status.key";
  };
  quoted = artifacts { apiTokenFile = "/run/token$USER%h\""; };
in
pkgs.runCommand "check-node-status-security" { } ''
  plain=${plain}/nixfleet-status.service
  protected=${protected}/nixfleet-status.service
  grep -q -- '--port 9100 --bind 0.0.0.0$' "$plain"
  if grep -Eq -- '--(api-token|tls-)' "$plain"; then exit 1; fi
  grep -Fq -- '--api-token-file "/run/credentials/status token"' "$protected"
  grep -Fq -- '--tls-cert "/run/credentials/status.crt"' "$protected"
  grep -Fq -- '--tls-key "/run/credentials/status.key"' "$protected"
  grep -Fq -- '--api-token-file "/run/token$$USER%%h\""' ${quoted}/nixfleet-status.service
  touch "$out"
''
