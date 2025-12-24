# NixOS Backend Compiler
# Translates NixFleet intent modules to native NixOS configuration
# This allows using the same nixfleet.* interface for both Ubuntu and NixOS hosts
{
  config,
  lib,
  pkgs,
  ...
}:

with lib;

let
  cfg = config.nixfleet;

  # Only compile if this is a NixOS host
  isNixOS = cfg.host.base == "nixos";

in
{
  config = mkIf isNixOS {
    # Translate nixfleet.packages to environment.systemPackages
    environment.systemPackages = cfg.packages;

    # Translate nixfleet.files to environment.etc
    environment.etc = mapAttrs' (
      path: fileCfg:
      let
        # Remove leading /etc/ from path for environment.etc
        etcPath =
          if hasPrefix "/etc/" path then
            removePrefix "/etc/" path
          else
            throw "NixOS backend: file path must start with /etc/, got: ${path}";
      in
      nameValuePair etcPath {
        text = fileCfg.text;
        source = fileCfg.source;
        mode = fileCfg.mode;
        user = fileCfg.owner;
        group = fileCfg.group;
      }
    ) (filterAttrs (path: _: hasPrefix "/etc/" path) cfg.files);

    # Translate nixfleet.systemd.units to systemd.services/timers
    systemd.services = mapAttrs' (
      name: unitCfg:
      let
        # Only handle .service units here
        isService = hasSuffix ".service" name;
        serviceName = removeSuffix ".service" name;
      in
      if isService then
        nameValuePair serviceName {
          enable = unitCfg.enabled;
          # Parse the unit file text - this is a simplified approach
          # In practice, you might want to use native NixOS service options
          serviceConfig = {
            # Basic service config extracted from unit text
            # For full control, use native NixOS options
          };
          # Use the raw unit text via script or ExecStart
          description = "NixFleet managed service: ${serviceName}";
          wantedBy = optional unitCfg.enabled "multi-user.target";
        }
      else
        nameValuePair name { }
    ) (filterAttrs (name: _: hasSuffix ".service" name) cfg.systemd.units);

    # Translate nixfleet.users to users.users
    users.users = mapAttrs (name: userCfg: {
      isSystemUser = userCfg.system;
      uid = userCfg.uid;
      group = userCfg.group;
      extraGroups = userCfg.extraGroups;
      home = userCfg.home;
      createHome = userCfg.createHome;
      shell =
        if userCfg.shell != null then
          pkgs.${baseNameOf userCfg.shell} or "/run/current-system/sw/bin/bash"
        else
          null;
      description = userCfg.description;
    }) cfg.users;

    # Translate nixfleet.groups to users.groups
    users.groups = mapAttrs (name: groupCfg: {
      gid = groupCfg.gid;
    }) cfg.groups;

    # Create directories via systemd.tmpfiles
    systemd.tmpfiles.rules = mapAttrsToList (
      path: dirCfg: "d ${path} ${dirCfg.mode} ${dirCfg.owner} ${dirCfg.group} -"
    ) cfg.directories;

    # Run hooks via activation scripts
    system.activationScripts = {
      nixfleet-pre = mkIf (cfg.hooks.preActivate != "") {
        text = cfg.hooks.preActivate;
        deps = [ ];
      };
      nixfleet-post = mkIf (cfg.hooks.postActivate != "") {
        text = cfg.hooks.postActivate;
        deps = [ "etc" ];
      };
    };
  };
}
