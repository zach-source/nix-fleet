# Shared Node.js environment for OpenClaw agents
#
# Provides the Node.js runtime, system dependencies, and tools
# used by all agent containers. OpenClaw is installed via npm at
# first container start (cached in an emptyDir volume).
{ pkgs }:

let
  nodejs = pkgs.nodejs_22;
in
{
  inherit nodejs;

  # System packages included in every agent container image
  systemDeps = [
    pkgs.gh # GitHub CLI — OpenClaw shells out to gh for GitHub operations
    pkgs.git
    pkgs.cacert
    pkgs.coreutils
    pkgs.bashInteractive
    pkgs.jq # JSON processor — OpenClaw default safeBin
    pkgs.curl # HTTP client — useful for API calls and web fetching
    pkgs.gnugrep # grep — pattern matching in exec commands
    pkgs.findutils # find, xargs — file discovery in exec commands
    pkgs.gnused # sed — stream editing in exec commands
    pkgs.gawk # awk — text processing in exec commands
  ];
}
