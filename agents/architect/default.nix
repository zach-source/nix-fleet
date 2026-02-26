# OCI image for the architect agent (OpenClaw + ZAI GLM 5)
{ n2c, pkgs }:

let
  mkAgent = import ../lib/mkAgent.nix { inherit n2c pkgs; };
  config = import ./config.nix;
in
mkAgent {
  name = "agent-architect";
  configFile = ./openclaw.json;
  soulFile = ./SOUL.md;
  plugins = [
    "slack"
  ];
  inherit config;
}
