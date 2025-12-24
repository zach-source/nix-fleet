# Example nix-darwin host configuration
# This can use either:
# 1. nixfleet.* options (translated to nix-darwin by the backend)
# 2. Native nix-darwin options directly
{ pkgs, ... }:

# NOTE: This is a managed macOS workstation example
# For headless Mac minis/servers, you'd omit the GUI-related settings

{
  nixfleet = {
    host = {
      name = "mac-1";
      base = "darwin";
      addr = "10.0.3.10";
    };

    # Packages - translated to environment.systemPackages
    packages = with pkgs; [
      # CLI tools
      git
      htop
      jq
      ripgrep
      fd
      bat
      eza

      # Development
      gnumake
      cmake

      # System utilities
      coreutils
      findutils
      gnused
      gawk
    ];

    # Managed directories
    directories = {
      "/var/log/nixfleet" = {
        mode = "0755";
        owner = "root";
        group = "wheel";
      };
      "/opt/nixfleet" = {
        mode = "0755";
        owner = "root";
        group = "wheel";
      };
    };

    # Groups
    groups = {
      developers = {
        gid = 501;
      };
    };

    # Managed files
    files = {
      "/etc/nixfleet/config.json" = {
        text = builtins.toJSON {
          managed = true;
          host = "mac-1";
          environment = "production";
        };
        mode = "0644";
        owner = "root";
        group = "wheel";
      };
    };

    # Health checks (used by NixFleet CLI)
    healthChecks = {
      nix-daemon = {
        type = "command";
        command = "launchctl list | grep -q org.nixos.nix-daemon";
        timeout = 5;
      };
    };

    hooks = {
      postActivate = ''
        echo "nix-darwin mac-1 activation complete"
        # Refresh launchd services if needed
        # launchctl kickstart -k system/org.nixos.nix-daemon
      '';
    };
  };

  # Native nix-darwin options can also be used alongside nixfleet.*
  # These take precedence and give full nix-darwin flexibility

  # Homebrew integration (if using nix-darwin's homebrew module)
  # homebrew = {
  #   enable = true;
  #   onActivation = {
  #     autoUpdate = true;
  #     cleanup = "zap";
  #   };
  #   brews = [
  #     "mas"  # Mac App Store CLI
  #   ];
  #   casks = [
  #     "iterm2"
  #     "visual-studio-code"
  #   ];
  # };

  # System preferences
  system.defaults = {
    # Dock settings
    dock = {
      autohide = true;
      mru-spaces = false;
      minimize-to-application = true;
    };

    # Finder settings
    finder = {
      AppleShowAllExtensions = true;
      FXEnableExtensionChangeWarning = false;
      _FXShowPosixPathInTitle = true;
    };

    # Global settings
    NSGlobalDomain = {
      AppleShowAllExtensions = true;
      InitialKeyRepeat = 15;
      KeyRepeat = 2;
    };

    # Trackpad
    trackpad = {
      Clicking = true;
      TrackpadRightClick = true;
    };
  };

  # Security settings
  security.pam.enableSudoTouchIdAuth = true;

  # Nix settings
  nix.settings = {
    experimental-features = [
      "nix-command"
      "flakes"
    ];
    trusted-users = [
      "root"
      "@admin"
    ];
  };

  # Environment variables
  environment.variables = {
    EDITOR = "vim";
    LANG = "en_US.UTF-8";
  };

  # Shell configuration
  programs.zsh.enable = true;

  # Required for nix-darwin
  system.stateVersion = 4;
}
