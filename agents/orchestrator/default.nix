# OCI image for the Orchestrator agent (OpenClaw)
{ n2c, pkgs }:

let
  mkAgent = import ../lib/mkAgent.nix { inherit n2c pkgs; };
  config = import ./config.nix;
in
mkAgent {
  name = "agent-orchestrator";
  configFile = ./openclaw.json;
  soulFile = ./SOUL.md;
  plugins = [ "slack" ];
  inherit config;
}
