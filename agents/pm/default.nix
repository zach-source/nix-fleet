# OCI image for the PM agent (OpenClaw)
{ n2c, pkgs }:

let
  mkAgent = import ../lib/mkAgent.nix { inherit n2c pkgs; };
  config = import ./config.nix;
in
mkAgent {
  name = "agent-pm";
  configFile = ./openclaw.json;
  soulFile = ./SOUL.md;
  plugins = [ "slack" ];
  inherit config;
}
