# nix-darwin Backend Compiler
# Translates NixFleet intent modules to native nix-darwin configuration
# This allows using the same nixfleet.* interface for Ubuntu, NixOS, and macOS hosts
{
  config,
  lib,
  pkgs,
  ...
}:

with lib;

let
  cfg = config.nixfleet;

  # Only compile if this is a darwin host
  isDarwin = cfg.host.base == "darwin";

  # Convert systemd-style unit to launchd plist
  # This is a best-effort mapping since the models are different
  mkLaunchdFromSystemd = name: unitCfg: {
    # Basic service configuration
    serviceConfig = {
      # Run at load (equivalent to WantedBy=multi-user.target)
      RunAtLoad = unitCfg.enabled;

      # Keep alive (restart on failure)
      KeepAlive = true;

      # Standard output/error logging
      StandardOutPath = "/var/log/nixfleet/${name}.log";
      StandardErrorPath = "/var/log/nixfleet/${name}.error.log";
    };
  };

in
{
  config = mkIf isDarwin {
    # Translate nixfleet.packages to environment.systemPackages
    environment.systemPackages = cfg.packages;

    # Translate nixfleet.files to environment.etc
    # nix-darwin uses the same environment.etc structure as NixOS
    environment.etc = mapAttrs' (
      path: fileCfg:
      let
        # Remove leading /etc/ from path for environment.etc
        etcPath =
          if hasPrefix "/etc/" path then
            removePrefix "/etc/" path
          else
            throw "nix-darwin backend: file path must start with /etc/, got: ${path}";
      in
      nameValuePair etcPath {
        text = fileCfg.text;
        source = fileCfg.source;
        # nix-darwin doesn't support mode/user/group on etc files directly
        # They inherit from the Nix store permissions
      }
    ) (filterAttrs (path: _: hasPrefix "/etc/" path) cfg.files);

    # Translate nixfleet.users to users.users
    # nix-darwin has a simpler user model than NixOS
    users.users = mapAttrs (
      name: userCfg:
      {
        uid = userCfg.uid;
        gid = userCfg.gid;
        home = userCfg.home;
        shell =
          if userCfg.shell != null then pkgs.${baseNameOf userCfg.shell} or "/bin/zsh" else "/bin/zsh";
        description = userCfg.description;
      }
      // optionalAttrs (userCfg.group != null) { gid = config.users.groups.${userCfg.group}.gid or 20; }
    ) cfg.users;

    # Translate nixfleet.groups to users.groups
    users.groups = mapAttrs (name: groupCfg: { gid = groupCfg.gid; }) cfg.groups;

    # Translate nixfleet.systemd.units to launchd.daemons
    # Services become system-wide launchd daemons
    launchd.daemons = mapAttrs' (
      name: unitCfg:
      let
        isService = hasSuffix ".service" name;
        serviceName = "nixfleet.${removeSuffix ".service" name}";
      in
      if isService then
        nameValuePair serviceName (mkLaunchdFromSystemd (removeSuffix ".service" name) unitCfg)
      else
        nameValuePair name { }
    ) (filterAttrs (name: _: hasSuffix ".service" name) cfg.systemd.units);

    # Create directories and run hooks via activation scripts
    system.activationScripts.postActivation.text =
      let
        # Create managed directories
        dirCommands = concatStringsSep "\n" (
          mapAttrsToList (path: dirCfg: ''
            mkdir -p "${path}"
            chmod ${dirCfg.mode} "${path}"
            chown ${dirCfg.owner}:${dirCfg.group} "${path}"
          '') cfg.directories
        );

        # Pre-activate hook
        preHook = optionalString (cfg.hooks.preActivate != "") ''
          echo "Running NixFleet pre-activate hook..."
          ${cfg.hooks.preActivate}
        '';

        # Post-activate hook
        postHook = optionalString (cfg.hooks.postActivate != "") ''
          echo "Running NixFleet post-activate hook..."
          ${cfg.hooks.postActivate}
        '';
      in
      ''
        # NixFleet activation for darwin
        ${preHook}

        # Create managed directories
        ${dirCommands}

        # Create log directory for launchd services
        mkdir -p /var/log/nixfleet
        chmod 755 /var/log/nixfleet

        ${postHook}
      '';

    # System defaults that are commonly useful
    # These can be overridden by user configuration
    system.defaults = {
      # Keep dock minimal
      dock.autohide = mkDefault true;

      # Finder preferences
      finder.AppleShowAllExtensions = mkDefault true;
      finder.FXEnableExtensionChangeWarning = mkDefault false;
    };
  };
}
