# PKI module - TLS certificate management for fleet hosts
{
  config,
  pkgs,
  lib,
  ...
}:

let
  cfg = config.nixfleet.pki;

  # Generate mTLS configuration snippet for various services
  mtlsConfigFor =
    service:
    {
      nginx = ''
        # NixFleet mTLS configuration for nginx
        ssl_certificate ${cfg.certDir}/host.crt;
        ssl_certificate_key ${cfg.certDir}/host.key;
        ssl_client_certificate ${cfg.certDir}/ca.crt;
        ssl_verify_client on;
        ssl_protocols TLSv1.2 TLSv1.3;
        ssl_prefer_server_ciphers on;
      '';

      envoy = builtins.toJSON {
        transport_socket = {
          name = "envoy.transport_sockets.tls";
          typed_config = {
            "@type" = "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext";
            common_tls_context = {
              tls_certificates = [
                {
                  certificate_chain.filename = "${cfg.certDir}/host.crt";
                  private_key.filename = "${cfg.certDir}/host.key";
                }
              ];
              validation_context.trusted_ca.filename = "${cfg.certDir}/ca.crt";
            };
            require_client_certificate = true;
          };
        };
      };

      generic = {
        ca_cert = "${cfg.certDir}/ca.crt";
        cert = "${cfg.certDir}/host.crt";
        key = "${cfg.certDir}/host.key";
      };
    }
    .${service} or (throw "Unknown mTLS service type: ${service}");

  # Generate a systemd path unit that watches for cert changes
  certWatcherUnit = serviceName: {
    "nixfleet-cert-watcher-${serviceName}" = {
      Unit = {
        Description = "Watch for NixFleet certificate changes to reload ${serviceName}";
        After = [ "local-fs.target" ];
      };
      Path = {
        PathChanged = [
          "${cfg.certDir}/host.crt"
          "${cfg.certDir}/host.key"
        ];
        Unit = "nixfleet-cert-reload-${serviceName}.service";
      };
      Install = {
        WantedBy = [ "multi-user.target" ];
      };
    };
  };

  # Generate a systemd service that reloads the target service
  certReloadUnit = serviceName: {
    "nixfleet-cert-reload-${serviceName}" = {
      Unit = {
        Description = "Reload ${serviceName} after certificate change";
      };
      Service = {
        Type = "oneshot";
        ExecStart = "${pkgs.systemd}/bin/systemctl reload-or-restart ${serviceName}.service";
      };
    };
  };
in
{
  options.nixfleet.pki = {
    enable = lib.mkEnableOption "NixFleet PKI certificate management";

    certDir = lib.mkOption {
      type = lib.types.path;
      default = "/etc/nixfleet/pki";
      description = "Directory for PKI certificates and keys";
    };

    caCert = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = "Path to the fleet CA certificate";
    };

    hostCert = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = "Path to this host's certificate";
    };

    hostKey = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = "Path to this host's private key";
    };

    trustSystemwide = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Add fleet CA to system-wide trust store";
    };

    reloadServices = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Services to reload when certificates change";
    };

    # mTLS configuration helpers
    mtls = {
      enable = lib.mkEnableOption "mTLS configuration helpers";

      generateConfigs = lib.mkOption {
        type = lib.types.listOf (
          lib.types.enum [
            "nginx"
            "envoy"
            "generic"
          ]
        );
        default = [ ];
        description = "Generate mTLS configuration snippets for these services";
      };
    };

    # Certificate rotation settings
    rotation = {
      enable = lib.mkEnableOption "automatic certificate rotation handling";

      watchServices = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = "Services to automatically reload when certificates change";
        example = [
          "nginx"
          "myapp"
        ];
      };

      preReloadScript = lib.mkOption {
        type = lib.types.nullOr lib.types.lines;
        default = null;
        description = "Script to run before reloading services";
      };

      postReloadScript = lib.mkOption {
        type = lib.types.nullOr lib.types.lines;
        default = null;
        description = "Script to run after reloading services";
      };
    };
  };

  config = lib.mkIf cfg.enable {
    # Create PKI directory
    nixfleet.directories = {
      "${cfg.certDir}" = {
        mode = "0755";
        owner = "root";
        group = "root";
      };
    };

    # Files to deploy
    nixfleet.files = lib.mkMerge [
      # CA certificate (always readable)
      (lib.mkIf (cfg.caCert != null) {
        "${cfg.certDir}/ca.crt" = {
          source = cfg.caCert;
          mode = "0644";
          owner = "root";
          group = "root";
        };
      })

      # Host certificate (readable)
      (lib.mkIf (cfg.hostCert != null) {
        "${cfg.certDir}/host.crt" = {
          source = cfg.hostCert;
          mode = "0644";
          owner = "root";
          group = "root";
        };
      })

      # Host private key (restricted)
      (lib.mkIf (cfg.hostKey != null) {
        "${cfg.certDir}/host.key" = {
          source = cfg.hostKey;
          mode = "0600";
          owner = "root";
          group = "root";
        };
      })

      # mTLS configuration snippets
      (lib.mkIf (cfg.mtls.enable && builtins.elem "nginx" cfg.mtls.generateConfigs) {
        "${cfg.certDir}/mtls-nginx.conf" = {
          text = mtlsConfigFor "nginx";
          mode = "0644";
          owner = "root";
          group = "root";
        };
      })

      (lib.mkIf (cfg.mtls.enable && builtins.elem "envoy" cfg.mtls.generateConfigs) {
        "${cfg.certDir}/mtls-envoy.json" = {
          text = mtlsConfigFor "envoy";
          mode = "0644";
          owner = "root";
          group = "root";
        };
      })

      (lib.mkIf (cfg.mtls.enable && builtins.elem "generic" cfg.mtls.generateConfigs) {
        "${cfg.certDir}/mtls.json" = {
          text = builtins.toJSON (mtlsConfigFor "generic");
          mode = "0644";
          owner = "root";
          group = "root";
        };
      })
    ];

    # Environment variables for easy access
    nixfleet.environment = lib.mkIf (cfg.caCert != null) {
      NIXFLEET_CA_CERT = "${cfg.certDir}/ca.crt";
      NIXFLEET_HOST_CERT = lib.mkIf (cfg.hostCert != null) "${cfg.certDir}/host.crt";
      NIXFLEET_HOST_KEY = lib.mkIf (cfg.hostKey != null) "${cfg.certDir}/host.key";
      NIXFLEET_MTLS_CONFIG = lib.mkIf cfg.mtls.enable "${cfg.certDir}/mtls.json";
    };

    # Add health check for certificate expiry
    nixfleet.healthChecks = lib.mkIf (cfg.hostCert != null) {
      cert-expiry = {
        command = ''
          CERT="${cfg.certDir}/host.crt"
          if [ ! -f "$CERT" ]; then
            echo "Certificate not found: $CERT"
            exit 2
          fi

          EXPIRY=$(${pkgs.openssl}/bin/openssl x509 -in "$CERT" -noout -enddate | cut -d= -f2)
          EXPIRY_EPOCH=$(date -d "$EXPIRY" +%s 2>/dev/null || date -j -f "%b %d %T %Y %Z" "$EXPIRY" +%s)
          NOW_EPOCH=$(date +%s)
          DAYS_LEFT=$(( (EXPIRY_EPOCH - NOW_EPOCH) / 86400 ))

          if [ $DAYS_LEFT -lt 0 ]; then
            echo "Certificate EXPIRED $((DAYS_LEFT * -1)) days ago"
            exit 2
          elif [ $DAYS_LEFT -lt 7 ]; then
            echo "Certificate expires in $DAYS_LEFT days (CRITICAL)"
            exit 2
          elif [ $DAYS_LEFT -lt 30 ]; then
            echo "Certificate expires in $DAYS_LEFT days (WARNING)"
            exit 1
          else
            echo "Certificate valid for $DAYS_LEFT days"
            exit 0
          fi
        '';
        interval = "1h";
        description = "Check TLS certificate expiration";
      };
    };

    # Certificate rotation - systemd path units to watch for cert changes
    nixfleet.systemd.units = lib.mkIf cfg.rotation.enable (
      lib.mkMerge (
        map (serviceName: certWatcherUnit serviceName) cfg.rotation.watchServices
        ++ map (serviceName: certReloadUnit serviceName) cfg.rotation.watchServices
      )
    );

    # Hooks for certificate rotation
    nixfleet.hooks = lib.mkIf (cfg.rotation.enable && cfg.rotation.preReloadScript != null) {
      pre-cert-reload = {
        script = cfg.rotation.preReloadScript;
        description = "Script to run before reloading services after cert rotation";
      };
    };
  };
}
