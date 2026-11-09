{ lib, pkgs }:
let
  checker =
    command: timeout:
    import ../lib/health-check.nix {
      inherit lib pkgs;
      checks.fixture = {
        type = "command";
        inherit command timeout;
      };
    };
  passing = checker "test 1 = 1" 1;
  failing = checker "exit 7" 1;
  hanging = checker "sleep 5" 1;
  invalid = checker null 0;
in
pkgs.runCommand "check-deployment-health" { } ''
  ${passing}
  if ${failing}; then echo 'failed check passed'; exit 1; fi
  if ${hanging}; then echo 'timed out check passed'; exit 1; fi
  if ${invalid}; then echo 'invalid check passed'; exit 1; fi
  touch "$out"
''
