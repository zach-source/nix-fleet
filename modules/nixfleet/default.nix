# NixFleet module - combines options and backend compilation
{
  config,
  lib,
  pkgs,
  ...
}:

{
  imports = [
    ./options.nix
    ../../backends/ubuntu/compile.nix
  ];

  # Add ubuntu output options
  options.nixfleet.ubuntu = {
    system = lib.mkOption {
      type = lib.types.package;
      description = "The compiled Ubuntu system derivation";
      internal = true;
    };

    manifestHash = lib.mkOption {
      type = lib.types.str;
      description = "Hash of the system manifest for change detection";
      internal = true;
    };
  };
}
