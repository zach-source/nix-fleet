# OCI image for the code-review agent (OpenClaw)
{ n2c, pkgs }:

let
  mkAgent = import ../lib/mkAgent.nix { inherit n2c pkgs; };
  config = import ./config.nix;
in
mkAgent {
  name = "agent-code-review";
  configFile = ./openclaw.json;
  soulFile = ./SOUL.md;
  inherit config;
}
