# OCI image for the personal assistant agent (OpenClaw)
{ n2c, pkgs }:

let
  mkAgent = import ../lib/mkAgent.nix { inherit n2c pkgs; };
  config = import ./config.nix;
in
mkAgent {
  name = "agent-personal";
  configFile = ./openclaw.json;
  soulFile = ./SOUL.md;
  inherit config;
}
