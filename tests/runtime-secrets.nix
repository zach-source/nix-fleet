{
  pkgs,
  lib,
  mkNixFleetConfiguration,
}:
let
  mkHost =
    extra:
    mkNixFleetConfiguration {
      system = pkgs.stdenv.hostPlatform.system;
      modules = [
        ../modules/dolt.nix
        {
          nixfleet.host = {
            name = "test";
            base = "ubuntu";
            addr = "127.0.0.1";
          };
          nixfleet.modules.dolt = {
            enable = true;
            package = pkgs.writeShellScriptBin "dolt" "cat";
          };
        }
        extra
      ];
    };
  secure =
    (mkHost {
      nixfleet.modules.dolt = {
        serverConfigFile = "/run/private/dolt.yaml";
        provisionRemoteRoot = false;
      };
      nixfleet.pullMode = {
        enable = true;
        repoURL = "https://example.invalid/repo";
        webhook = {
          url = "https://example.invalid/hook";
          secretFile = "/run/private/webhook";
        };
      };
    }).config.nixfleet;
  pullScript = builtins.readFile (
    lib.head (
      lib.splitString " " (lib.last (lib.splitString "cp " secure.ubuntu.system.pullMode.buildCommand))
    )
  );
  rejects = extra: lib.any (a: !a.assertion) (mkHost extra).config.assertions;
  acceptsWebhook =
    webhook:
    (builtins.tryEval (
      builtins.deepSeq
        (mkHost { nixfleet.pullMode.webhook = webhook; }).config.nixfleet.pullMode.webhook.secretFile
        true
    )).success;
  checks = {
    "webhook rejects conflicting inline key" =
      !(acceptsWebhook {
        secret = "fixture";
        secretFile = "/run/private/key";
      });
    "webhook rejects store-backed key" = !(acceptsWebhook { secretFile = "/nix/store/fixture-key"; });

    "Dolt migration rejects mixed inline and runtime secrets" = rejects {
      nixfleet.modules.dolt = {
        serverConfigFile = "/run/private/dolt.yaml";
        password = "fixture";
      };
    };
    "runtime paths reject Nix store references" = rejects {
      nixfleet.modules.dolt = {
        provisionRemoteRoot = false;
        serverConfigFile = "/nix/store/fixture-config";
      };
    };
    "runtime webhook signs with the file, without an argv key" =
      lib.hasInfix "webhook-signature --secret-file /run/private/webhook" pullScript
      && !(lib.hasInfix "WEBHOOK_SECRET=" pullScript)
      && !(lib.hasInfix "-hmac" pullScript);

    "runtime Dolt config is not a managed store file" = !(secure.files ? "/etc/dolt/server.yaml");
    "runtime Dolt config is used" =
      lib.hasInfix "--config /run/private/dolt.yaml"
        secure.systemd.units."dolt-server.service".text;
    "disabled provisioning has no grant script" =
      !(lib.hasInfix "dolt-ensure-remote-grants" secure.systemd.units."dolt-server.service".text);

  };
  failures = lib.attrNames (lib.filterAttrs (_: ok: !ok) checks);
in
pkgs.runCommand "check-runtime-secrets" { } ''
  ${lib.concatMapStringsSep "\n" (f: "echo ${lib.escapeShellArg f} >&2") failures}
  ${if failures == [ ] then "touch $out" else "exit 1"}
''
