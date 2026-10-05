# Top-level `assertions` and `warnings`, NixOS-compatible.
#
# A separate module because nixos and nix-darwin each declare these themselves.
# Declared alongside the rest of the nixfleet options, they collide — "The
# option `assertions' ... is already declared" — and every host on those two
# backends fails to evaluate at all. Only the bare-evalModules backend
# (ubuntu/dgx, via mkNixFleetConfiguration) needs them supplied.
{ lib, ... }:

let
  inherit (lib) mkOption types;

  assertionType = types.submodule {
    options = {
      assertion = mkOption {
        type = types.bool;
        description = "Condition that must hold";
      };
      message = mkOption {
        type = types.str;
        description = "Message shown when the assertion fails";
      };
    };
  };
in
{
  options.assertions = mkOption {
    type = types.listOf assertionType;
    default = [ ];
    description = ''
      List of assertions that must pass for the build to succeed.
      Each assertion has an `assertion` boolean and a `message` string.
    '';
    example = [
      {
        assertion = true;
        message = "Example assertion that always passes";
      }
    ];
  };

  options.warnings = mkOption {
    type = types.listOf types.str;
    default = [ ];
    description = ''
      List of warning messages to display during evaluation.
      Warnings do not fail the build but alert the user to potential issues.
    '';
    example = [ "This feature is deprecated" ];
  };
}
