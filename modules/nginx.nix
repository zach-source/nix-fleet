# Nginx module
{
  config,
  pkgs,
  lib,
  ...
}:

{
  nixfleet.packages = [ pkgs.nginx ];

  nixfleet.users.nginx = {
    system = true;
    uid = 60;
    group = "nginx";
    home = "/var/lib/nginx";
  };

  nixfleet.groups.nginx = {
    gid = 60;
  };

  nixfleet.directories = {
    "/var/lib/nginx" = {
      mode = "0750";
      owner = "nginx";
      group = "nginx";
    };
    "/var/log/nginx" = {
      mode = "0750";
      owner = "nginx";
      group = "nginx";
    };
    "/etc/nginx/sites-enabled" = {
      mode = "0755";
      owner = "root";
      group = "root";
    };
  };

  nixfleet.files."/etc/nginx/nginx.conf" = {
    text = ''
      user nginx nginx;
      worker_processes auto;
      error_log /var/log/nginx/error.log;
      pid /run/nginx.pid;

      events {
          worker_connections 1024;
      }

      http {
          include       /etc/nginx/mime.types;
          default_type  application/octet-stream;

          log_format  main  '$remote_addr - $remote_user [$time_local] "$request" '
                            '$status $body_bytes_sent "$http_referer" '
                            '"$http_user_agent" "$http_x_forwarded_for"';

          access_log  /var/log/nginx/access.log  main;

          sendfile        on;
          keepalive_timeout  65;

          include /etc/nginx/sites-enabled/*.conf;
      }
    '';
    mode = "0644";
    owner = "root";
    group = "root";
    restartUnits = [ "nginx.service" ];
  };

  nixfleet.systemd.units."nginx.service" = {
    text = ''
      [Unit]
      Description=The NGINX HTTP and reverse proxy server
      After=network-online.target
      Wants=network-online.target

      [Service]
      Type=forking
      PIDFile=/run/nginx.pid
      ExecStartPre=/nix/var/nix/profiles/nixfleet/system/bin/nginx -t
      ExecStart=/nix/var/nix/profiles/nixfleet/system/bin/nginx
      ExecReload=/bin/kill -s HUP $MAINPID
      ExecStop=/bin/kill -s QUIT $MAINPID
      PrivateTmp=true

      [Install]
      WantedBy=multi-user.target
    '';
    enabled = true;
  };
}
