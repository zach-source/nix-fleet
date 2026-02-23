# OCI image for the devops agent (OpenClaw)
{ n2c, pkgs }:

let
  mkAgent = import ../lib/mkAgent.nix { inherit n2c pkgs; };
  config = import ./config.nix;
in
mkAgent {
  name = "agent-devops";
  configFile = ./openclaw.json;
  soulFile = ./SOUL.md;
  inherit config;
}
