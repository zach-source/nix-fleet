# Expected content comes only from immutable declared sources, never live files.
{
  lib,
  pkgs,
  files,
  modeFromSource ? false,
}:
pkgs.runCommand "nixfleet-managed-files.json"
  {
    nativeBuildInputs = [
      pkgs.jq
      pkgs.coreutils
    ];
  }
  ''
    set -euo pipefail
    (
      echo '{}'
      ${lib.concatStringsSep "\n" (
        lib.mapAttrsToList (
          path: file:
          let
            source = if file.text != null then pkgs.writeText "managed-file" file.text else file.source;
            metadata = builtins.toJSON {
              inherit path;
              inherit (file) mode owner group;
              restart_units = file.restartUnits;
              follow_symlink = modeFromSource;
            };
          in
          ''
            mode=${
              if modeFromSource then
                "$(stat -c %a ${lib.escapeShellArg (toString source)})"
              else
                lib.escapeShellArg file.mode
            }
            hash=$(sha256sum ${lib.escapeShellArg (toString source)} | cut -d ' ' -f 1)
            jq -n --arg path ${lib.escapeShellArg path} --arg hash "$hash" --arg mode "$mode" \
              --argjson metadata ${lib.escapeShellArg metadata} \
              '{($path): ($metadata + {hash: $hash, mode: $mode} | .mode |= (sub("^0+"; "") | if . == "" then "0" else . end))}'
          ''
        ) files
      )}
    ) | jq -s add > "$out"
  ''
