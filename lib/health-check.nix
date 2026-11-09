# One local executable is shared by controller-driven and pull deployments.
{
  lib,
  pkgs,
  checks,
}:
pkgs.writeShellScript "nixfleet-verify-health" ''
  set -eu
  ${lib.concatStringsSep "\n" (
    lib.mapAttrsToList (
      name: check:
      let
        valid =
          check.timeout > 0
          && (
            if check.type == "command" then
              check.command != null
            else if check.type == "http" then
              check.url != null
            else
              check.port != null && check.port > 0 && check.port <= 65535
          );
        command =
          if !valid then
            "exit 1"
          else if check.type == "command" then
            check.command
          else if check.type == "http" then
            ''
              status=$(${pkgs.curl}/bin/curl --silent --output /dev/null --write-out '%{http_code}' --url ${lib.escapeShellArg check.url})
              test "$status" = ${lib.escapeShellArg (toString check.expectedStatus)}
            ''
          else
            "exec 3<>" + lib.escapeShellArg "/dev/tcp/${check.host}/${toString check.port}";
      in
      ''
        if ! ${pkgs.coreutils}/bin/timeout ${toString (lib.max 1 check.timeout)}s ${pkgs.bash}/bin/bash -c ${lib.escapeShellArg command}; then
          echo ${lib.escapeShellArg "Health check failed: ${name}"} >&2
          exit 1
        fi
      ''
    ) checks
  )}
''
