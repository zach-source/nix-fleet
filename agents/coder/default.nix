# OCI image for the coder agent (OpenClaw + ZAI GLM 5)
{ n2c, pkgs }:

let
  mkAgent = import ../lib/mkAgent.nix { inherit n2c pkgs; };
  config = import ./config.nix;
in
mkAgent {
  name = "agent-coder";
  configFile = ./openclaw.json;
  soulFile = ./SOUL.md;
  plugins = [
    "slack"
  ];
  inherit config;
}
