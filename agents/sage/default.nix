# OCI image for the sage agent (OpenClaw + GPT-5.2 Codex)
{ n2c, pkgs }:

let
  mkAgent = import ../lib/mkAgent.nix { inherit n2c pkgs; };
  config = import ./config.nix;
in
mkAgent {
  name = "agent-sage";
  configFile = ./openclaw.json;
  soulFile = ./SOUL.md;
  plugins = [
    "slack"
  ];
  inherit config;
}
