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

  # Files this backend can place, and the ones it has to refuse. nix-darwin's
  # environment.etc is the only file mechanism here, so anything outside /etc
  # has nowhere to go.
  etcFiles = filterAttrs (path: _: hasPrefix "/etc/" path) cfg.files;
  nonEtcFiles = attrNames (filterAttrs (path: _: !hasPrefix "/etc/" path) cfg.files);

  # environment.etc files are store symlinks, so they carry the store's
  # ownership and mode. A file that asked for something else would be deployed
  # silently wrong, which is worse than being told it cannot be done.
  filesWantingPerms = attrNames (
    filterAttrs (
      _: f:
      f.mode != "0644"
      || !(builtins.elem f.owner [
        "root"
        "wheel"
      ])
      || !(builtins.elem f.group [
        "root"
        "wheel"
      ])
    ) etcFiles
  );

in
{
  config = mkIf isDarwin {
    # What this backend will not pretend to do.
    #
    # Each of these used to be dropped or mistranslated silently, which on a
    # deployment tool is the worst available outcome: the apply succeeds and
    # the host is not what the config says. Failing at eval costs a message.
    assertions = [
      {
        # launchd has no equivalent of a systemd unit file. The translation
        # that used to live here read only `enabled` and threw the unit body
        # away, so every declared service compiled to a plist with no program
        # to run — an apply that reported success and started nothing.
        #
        # Not a gap to paper over with a better parser: Type/After/Wants/
        # Requires/Condition*/Restart/ExecStartPre ordering have no launchd
        # counterparts, and this fleet's units lean on all of them. Declare
        # launchd.daemons natively alongside nixfleet.* instead, which the
        # backend has always allowed.
        assertion = cfg.systemd.units == { };
        message =
          "nixfleet.systemd.units is not supported on darwin hosts "
          + "(${concatStringsSep ", " (attrNames cfg.systemd.units)}). "
          + "launchd has no systemd-unit equivalent and NixFleet does not "
          + "translate one. Declare launchd.daemons natively instead.";
      }
      {
        # The ubuntu backend decrypts age secrets in activation step 5 with
        # the host's SSH key; nothing in this backend does any of that.
        assertion = cfg.secrets.items == { };
        message =
          "nixfleet.secrets is not supported on darwin hosts "
          + "(${concatStringsSep ", " (attrNames cfg.secrets.items)}). "
          + "The darwin backend has no secret decryption or deployment step.";
      }
      {
        # environment.etc is the only file mechanism nix-darwin offers.
        assertion = nonEtcFiles == [ ];
        message =
          "nixfleet.files outside /etc is not supported on darwin hosts "
          + "(${concatStringsSep ", " nonEtcFiles}). "
          + "The darwin backend places files via environment.etc, which can "
          + "only write under /etc.";
      }
      {
        # Store symlinks, so the store's mode and ownership are what lands.
        assertion = filesWantingPerms == [ ];
        message =
          "nixfleet.files on darwin cannot set mode/owner/group "
          + "(${concatStringsSep ", " filesWantingPerms}). "
          + "environment.etc deploys store symlinks, so these files are "
          + "root-owned and 0644 whatever is declared. Remove the mode/owner/"
          + "group, or place the file from hooks.postActivate.";
      }
      {
        # apt on macOS is not a thing worth explaining in a failed apply.
        assertion = cfg.apt.packages == [ ] && cfg.apt.absent == [ ] && cfg.apt.hold == [ ];
        message = "nixfleet.apt is not supported on darwin hosts: macOS has no apt.";
      }
    ];

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
      # Exactly one of text/source, never both: environment.etc types `source`
      # as an absolute path, so passing the null that nixfleet.files leaves
      # there fails the option type outright ("is not of type `absolute
      # path'"). mode/owner/group are dropped — see the assertion below, which
      # refuses to drop them silently.
      nameValuePair etcPath (
        if fileCfg.text != null then { text = fileCfg.text; } else { source = fileCfg.source; }
      )
    ) etcFiles;

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

    # nixfleet.systemd.units is NOT translated to launchd. See the assertion
    # below: there is no honest mapping, and the version that used to be here
    # produced plists with no program to run.

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

    # No system.defaults here on purpose.
    #
    # This used to mkDefault dock.autohide, finder.AppleShowAllExtensions and
    # finder.FXEnableExtensionChangeWarning. Those are all
    # primary-user-requiring options, so merely declaring base = "darwin"
    # forced every host to also set system.primaryUser or fail nix-darwin's
    # migration assertion — a config error about a Dock preference the host
    # never asked for. A fleet deployment tool has no business holding
    # opinions about the Dock; hosts that want them can set them natively.
  };
}
