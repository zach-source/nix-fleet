# Example Ubuntu host configuration
{ pkgs, ... }:

{
  nixfleet = {
    host = {
      name = "web-1";
      base = "ubuntu";
      addr = "10.0.1.11";
    };

    # Packages to install via Nix profile
    packages = with pkgs; [
      nginx
      git
      htop
      curl
      jq
    ];

    # Managed directories
    directories = {
      "/var/lib/myapp" = {
        mode = "0750";
        owner = "myapp";
        group = "myapp";
      };
      "/etc/myapp" = {
        mode = "0755";
        owner = "root";
        group = "myapp";
      };
    };

    # Groups
    groups = {
      myapp = {
        gid = 991;
        system = true;
      };
    };

    # Users
    users = {
      myapp = {
        system = true;
        uid = 991;
        group = "myapp";
        home = "/var/lib/myapp";
        createHome = true;
        description = "MyApp service account";
      };
    };

    # Managed files in /etc
    files = {
      "/etc/myapp/config.json" = {
        text = builtins.toJSON {
          port = 8080;
          environment = "production";
          log_level = "info";
          database = {
            host = "localhost";
            port = 5432;
          };
        };
        mode = "0640";
        owner = "root";
        group = "myapp";
        restartUnits = [ "myapp.service" ];
      };

      "/etc/nginx/sites-enabled/myapp.conf" = {
        text = ''
          server {
              listen 80;
              server_name myapp.example.com;

              location / {
                  proxy_pass http://127.0.0.1:8080;
                  proxy_set_header Host $host;
                  proxy_set_header X-Real-IP $remote_addr;
              }
          }
        '';
        mode = "0644";
        owner = "root";
        group = "root";
        restartUnits = [ "nginx.service" ];
      };
    };

    # Systemd units
    systemd.units = {
      "myapp.service" = {
        text = ''
          [Unit]
          Description=My Application
          After=network-online.target
          Wants=network-online.target

          [Service]
          Type=simple
          User=myapp
          Group=myapp
          WorkingDirectory=/var/lib/myapp
          ExecStart=/nix/var/nix/profiles/nixfleet/system/bin/myapp --config /etc/myapp/config.json
          Restart=always
          RestartSec=5

          # Hardening
          NoNewPrivileges=true
          ProtectSystem=strict
          ProtectHome=true
          PrivateTmp=true
          ReadWritePaths=/var/lib/myapp

          [Install]
          WantedBy=multi-user.target
        '';
        enabled = true;
      };
    };

    # Health checks
    healthChecks = {
      myapp-http = {
        type = "http";
        url = "http://localhost:8080/health";
        expectedStatus = 200;
        timeout = 10;
        interval = 30;
      };
    };

    # Hooks
    hooks = {
      postActivate = ''
        echo "MyApp activation complete"
      '';
    };
  };
}
