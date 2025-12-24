# Example NixOS host configuration
# This can use either:
# 1. nixfleet.* options (translated to NixOS by the backend)
# 2. Native NixOS options directly
{ pkgs, ... }:

# NOTE: In a real deployment, you would import hardware-configuration.nix
# and set appropriate boot/filesystem options

{
  nixfleet = {
    host = {
      name = "db-1";
      base = "nixos";
      addr = "10.0.2.10";
    };

    # Packages - translated to environment.systemPackages
    packages = with pkgs; [
      postgresql_16
      htop
      vim
      git
    ];

    # Managed directories - translated to systemd.tmpfiles.rules
    directories = {
      "/var/lib/postgresql/backups" = {
        mode = "0750";
        owner = "postgres";
        group = "postgres";
      };
    };

    # Groups - translated to users.groups
    groups = {
      dbadmin = {
        gid = 990;
      };
    };

    # Users - translated to users.users
    users = {
      dbadmin = {
        system = false;
        uid = 990;
        group = "dbadmin";
        extraGroups = [ "postgres" ];
        home = "/home/dbadmin";
        createHome = true;
        description = "Database administrator";
      };
    };

    # Managed files - translated to environment.etc
    files = {
      "/etc/postgresql-backup.conf" = {
        text = ''
          BACKUP_DIR=/var/lib/postgresql/backups
          RETENTION_DAYS=7
          COMPRESS=true
        '';
        mode = "0640";
        owner = "postgres";
        group = "postgres";
      };
    };

    # Health checks (used by NixFleet CLI)
    healthChecks = {
      postgres = {
        type = "tcp";
        host = "localhost";
        port = 5432;
        timeout = 5;
      };
    };

    hooks = {
      postActivate = ''
        echo "NixOS db-1 activation complete"
      '';
    };
  };

  # Native NixOS options can also be used alongside nixfleet.*
  # These take precedence and give full NixOS flexibility

  services.postgresql = {
    enable = true;
    package = pkgs.postgresql_16;
    enableTCPIP = true;
    authentication = ''
      local all all trust
      host all all 127.0.0.1/32 scram-sha-256
      host all all ::1/128 scram-sha-256
    '';
    settings = {
      max_connections = 100;
      shared_buffers = "256MB";
      effective_cache_size = "768MB";
      maintenance_work_mem = "64MB";
      checkpoint_completion_target = 0.9;
      wal_buffers = "16MB";
      default_statistics_target = 100;
      random_page_cost = 1.1;
      effective_io_concurrency = 200;
    };
  };

  # Firewall
  networking.firewall.allowedTCPPorts = [ 5432 ];

  # Automatic backups via systemd timer
  systemd.services.postgresql-backup = {
    description = "PostgreSQL daily backup";
    serviceConfig = {
      Type = "oneshot";
      User = "postgres";
      ExecStart = "${pkgs.postgresql_16}/bin/pg_dumpall | ${pkgs.gzip}/bin/gzip > /var/lib/postgresql/backups/backup-$(date +%Y%m%d).sql.gz";
    };
  };

  systemd.timers.postgresql-backup = {
    description = "Run PostgreSQL backup daily";
    wantedBy = [ "timers.target" ];
    timerConfig = {
      OnCalendar = "daily";
      Persistent = true;
    };
  };

  # Required NixOS option
  system.stateVersion = "24.05";
}
