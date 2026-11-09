{
  lib,
  pkgs,
  mkNixOSFleetConfiguration,
}:
let
  mkHost =
    strict:
    mkNixOSFleetConfiguration {
      modules = [
        {
          system.stateVersion = "24.11";
          nixfleet.host = {
            name = "fixture";
            base = "nixos";
            addr = "127.0.0.1";
          };
          nixfleet.nixos.strictTranslation = strict;
          nixfleet.files = {
            "/etc/fixture.conf".text = "fixture";
            "/etc/source.conf".source = builtins.toFile "source.conf" "source";
            "/opt/unsupported".text = "not silently deployed";
          };
          nixfleet.systemd.units = {
            "fixture.service" = {
              enabled = true;
              text = "[Service]\nExecStart=/bin/true\n";
            };
            "fixture.timer" = {
              enabled = true;
              text = "[Timer]\nOnBootSec=1min\n";
            };
          };
        }
      ];
    };
  strict = (mkHost true).config;
  legacy = (mkHost false).config;
  failures = lib.filter (a: !a.assertion) strict.assertions;
in
assert lib.hasPrefix "/nix/store/" (toString legacy.environment.etc."fixture.conf".source);
assert legacy.environment.etc."source.conf".source == builtins.toFile "source.conf" "source";
assert strict.systemd.units."fixture.service".text == "[Service]\nExecStart=/bin/true\n";
assert strict.systemd.units."fixture.timer".text == "[Timer]\nOnBootSec=1min\n";
assert builtins.elem "timers.target" strict.systemd.units."fixture.timer".wantedBy;
assert lib.any (a: lib.hasInfix "/opt/unsupported" a.message) failures;
assert legacy.systemd.services.fixture.serviceConfig == { };
pkgs.runCommand "check-nixos-translation" { } "touch $out"
