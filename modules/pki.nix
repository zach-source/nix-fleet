# PKI module - TLS certificate management for fleet hosts
{
  config,
  pkgs,
  lib,
  ...
}:

let
  cfg = config.nixfleet.pki;

  # Certificate submodule type
  certType = lib.types.submodule (
    { name, ... }:
    {
      options = {
        enable = lib.mkEnableOption "this certificate" // {
          default = true;
        };

        source = lib.mkOption {
          type = lib.types.nullOr lib.types.path;
          default = null;
          description = "Path to the certificate file (source)";
        };

        keySource = lib.mkOption {
          type = lib.types.nullOr lib.types.path;
          default = null;
          description = "Path to the private key file (source)";
        };

        installPath = lib.mkOption {
          type = lib.types.path;
          default = cfg.certDir;
          description = "Directory to install the certificate";
        };

        certFile = lib.mkOption {
          type = lib.types.str;
          default = "${name}.crt";
          description = "Filename for the certificate";
        };

        keyFile = lib.mkOption {
          type = lib.types.str;
          default = "${name}.key";
          description = "Filename for the private key";
        };

        owner = lib.mkOption {
          type = lib.types.str;
          default = "root";
          description = "Owner of the certificate files";
        };

        group = lib.mkOption {
          type = lib.types.str;
          default = "root";
          description = "Group of the certificate files";
        };

        certMode = lib.mkOption {
          type = lib.types.str;
          default = "0644";
          description = "Permissions for the certificate file";
        };

        keyMode = lib.mkOption {
          type = lib.types.str;
          default = "0600";
          description = "Permissions for the private key file";
        };

        reloadServices = lib.mkOption {
          type = lib.types.listOf lib.types.str;
          default = [ ];
          description = "Services to reload when this certificate changes";
        };
      };
    }
  );

  # Generate mTLS configuration snippet for various services
  mtlsConfigFor =
    certName: certCfg: service:
    let
      certPath = "${certCfg.installPath}/${certCfg.certFile}";
      keyPath = "${certCfg.installPath}/${certCfg.keyFile}";
      caPath = "${cfg.certDir}/ca.crt";
    in
    {
      nginx = ''
        # NixFleet mTLS configuration for ${certName}
        ssl_certificate ${certPath};
        ssl_certificate_key ${keyPath};
        ssl_client_certificate ${caPath};
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
                  certificate_chain.filename = certPath;
                  private_key.filename = keyPath;
                }
              ];
              validation_context.trusted_ca.filename = caPath;
            };
            require_client_certificate = true;
          };
        };
      };

      generic = {
        name = certName;
        ca_cert = caPath;
        cert = certPath;
        key = keyPath;
      };
    }
    .${service} or (throw "Unknown mTLS service type: ${service}");

  # Generate systemd path unit that watches for cert changes
  certWatcherUnit = certName: certCfg: serviceName: {
    "nixfleet-cert-watcher-${certName}-${serviceName}" = {
      Unit = {
        Description = "Watch for NixFleet certificate ${certName} changes to reload ${serviceName}";
        After = [ "local-fs.target" ];
      };
      Path = {
        PathChanged = [
          "${certCfg.installPath}/${certCfg.certFile}"
          "${certCfg.installPath}/${certCfg.keyFile}"
        ];
        Unit = "nixfleet-cert-reload-${certName}-${serviceName}.service";
      };
      Install = {
        WantedBy = [ "multi-user.target" ];
      };
    };
  };

  # Generate systemd service that reloads the target service
  certReloadUnit = certName: serviceName: {
    "nixfleet-cert-reload-${certName}-${serviceName}" = {
      Unit = {
        Description = "Reload ${serviceName} after ${certName} certificate change";
      };
      Service = {
        Type = "oneshot";
        ExecStart = "${pkgs.systemd}/bin/systemctl reload-or-restart ${serviceName}.service";
      };
    };
  };

  # Get all enabled certificates
  enabledCerts = lib.filterAttrs (n: v: v.enable) cfg.certificates;

  # Generate files for all certificates
  certFiles = lib.mapAttrs' (
    name: certCfg:
    lib.nameValuePair "${certCfg.installPath}/${certCfg.certFile}" {
      source = certCfg.source;
      mode = certCfg.certMode;
      owner = certCfg.owner;
      group = certCfg.group;
    }
  ) (lib.filterAttrs (n: v: v.source != null) enabledCerts);

  keyFiles = lib.mapAttrs' (
    name: certCfg:
    lib.nameValuePair "${certCfg.installPath}/${certCfg.keyFile}" {
      source = certCfg.keySource;
      mode = certCfg.keyMode;
      owner = certCfg.owner;
      group = certCfg.group;
    }
  ) (lib.filterAttrs (n: v: v.keySource != null) enabledCerts);

  # Generate directories for all install paths
  certDirs = lib.unique (lib.mapAttrsToList (name: certCfg: certCfg.installPath) enabledCerts);
in
{
  options.nixfleet.pki = {
    enable = lib.mkEnableOption "NixFleet PKI certificate management";

    certDir = lib.mkOption {
      type = lib.types.path;
      default = "/etc/nixfleet/pki";
      description = "Default directory for PKI certificates and keys";
    };

    caCert = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = "Path to the fleet CA certificate";
    };

    # Named certificates with custom install paths
    certificates = lib.mkOption {
      type = lib.types.attrsOf certType;
      default = { };
      description = "Named certificates with custom installation configuration";
      example = lib.literalExpression ''
        {
          web = {
            source = ./certs/web.crt;
            keySource = ./certs/web.key;
            installPath = "/etc/nginx/ssl";
            owner = "nginx";
            group = "nginx";
            reloadServices = [ "nginx" ];
          };
          api = {
            source = ./certs/api.crt;
            keySource = ./certs/api.key;
            installPath = "/opt/myapp/certs";
            owner = "myapp";
            reloadServices = [ "myapp" ];
          };
        }
      '';
    };

    # Legacy single-cert options (for backwards compatibility)
    hostCert = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = "Path to this host's default certificate (legacy)";
    };

    hostKey = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = "Path to this host's default private key (legacy)";
    };

    trustSystemwide = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Add fleet CA to system-wide trust store";
    };

    reloadServices = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Services to reload when default certificate changes";
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
        description = "Services to automatically reload when default certificates change";
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
    # Create all certificate directories
    nixfleet.directories = lib.mkMerge (
      [
        {
          "${cfg.certDir}" = {
            mode = "0755";
            owner = "root";
            group = "root";
          };
        }
      ]
      ++ map (dir: {
        "${dir}" = {
          mode = "0755";
          owner = "root";
          group = "root";
        };
      }) certDirs
    );

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

      # Legacy host certificate (readable)
      (lib.mkIf (cfg.hostCert != null) {
        "${cfg.certDir}/host.crt" = {
          source = cfg.hostCert;
          mode = "0644";
          owner = "root";
          group = "root";
        };
      })

      # Legacy host private key (restricted)
      (lib.mkIf (cfg.hostKey != null) {
        "${cfg.certDir}/host.key" = {
          source = cfg.hostKey;
          mode = "0600";
          owner = "root";
          group = "root";
        };
      })

      # Named certificates
      certFiles
      keyFiles

      # mTLS configuration snippets (using first enabled cert or legacy)
      (lib.mkIf (cfg.mtls.enable && builtins.elem "nginx" cfg.mtls.generateConfigs) {
        "${cfg.certDir}/mtls-nginx.conf" =
          let
            firstCert = lib.head (lib.attrNames enabledCerts);
            certCfg =
              if enabledCerts != { } then
                enabledCerts.${firstCert}
              else
                {
                  installPath = cfg.certDir;
                  certFile = "host.crt";
                  keyFile = "host.key";
                };
          in
          {
            text = mtlsConfigFor (if enabledCerts != { } then firstCert else "host") certCfg "nginx";
            mode = "0644";
            owner = "root";
            group = "root";
          };
      })

      (lib.mkIf (cfg.mtls.enable && builtins.elem "generic" cfg.mtls.generateConfigs) {
        "${cfg.certDir}/mtls.json" = {
          text = builtins.toJSON (
            lib.mapAttrs (name: certCfg: mtlsConfigFor name certCfg "generic") enabledCerts
          );
          mode = "0644";
          owner = "root";
          group = "root";
        };
      })
    ];

    # Environment variables for easy access
    nixfleet.environment = lib.mkMerge [
      (lib.mkIf (cfg.caCert != null) {
        NIXFLEET_CA_CERT = "${cfg.certDir}/ca.crt";
      })
      (lib.mkIf (cfg.hostCert != null) {
        NIXFLEET_HOST_CERT = "${cfg.certDir}/host.crt";
      })
      (lib.mkIf (cfg.hostKey != null) {
        NIXFLEET_HOST_KEY = "${cfg.certDir}/host.key";
      })
      (lib.mkIf cfg.mtls.enable {
        NIXFLEET_MTLS_CONFIG = "${cfg.certDir}/mtls.json";
      })
      # Environment vars for each named certificate
      (lib.mapAttrs' (
        name: certCfg:
        lib.nameValuePair "NIXFLEET_CERT_${lib.toUpper name}" "${certCfg.installPath}/${certCfg.certFile}"
      ) enabledCerts)
      (lib.mapAttrs' (
        name: certCfg:
        lib.nameValuePair "NIXFLEET_KEY_${lib.toUpper name}" "${certCfg.installPath}/${certCfg.keyFile}"
      ) enabledCerts)
    ];

    # Add health check for certificate expiry
    nixfleet.healthChecks = lib.mkMerge [
      # Legacy host cert check
      (lib.mkIf (cfg.hostCert != null) {
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
      })

      # Health checks for each named certificate
      (lib.mapAttrs' (
        name: certCfg:
        lib.nameValuePair "cert-expiry-${name}" {
          command = ''
            CERT="${certCfg.installPath}/${certCfg.certFile}"
            if [ ! -f "$CERT" ]; then
              echo "Certificate not found: $CERT"
              exit 2
            fi

            EXPIRY=$(${pkgs.openssl}/bin/openssl x509 -in "$CERT" -noout -enddate | cut -d= -f2)
            EXPIRY_EPOCH=$(date -d "$EXPIRY" +%s 2>/dev/null || date -j -f "%b %d %T %Y %Z" "$EXPIRY" +%s)
            NOW_EPOCH=$(date +%s)
            DAYS_LEFT=$(( (EXPIRY_EPOCH - NOW_EPOCH) / 86400 ))

            if [ $DAYS_LEFT -lt 0 ]; then
              echo "${name} certificate EXPIRED $((DAYS_LEFT * -1)) days ago"
              exit 2
            elif [ $DAYS_LEFT -lt 7 ]; then
              echo "${name} certificate expires in $DAYS_LEFT days (CRITICAL)"
              exit 2
            elif [ $DAYS_LEFT -lt 30 ]; then
              echo "${name} certificate expires in $DAYS_LEFT days (WARNING)"
              exit 1
            else
              echo "${name} certificate valid for $DAYS_LEFT days"
              exit 0
            fi
          '';
          interval = "1h";
          description = "Check ${name} TLS certificate expiration";
        }
      ) (lib.filterAttrs (n: v: v.source != null) enabledCerts))
    ];

    # Certificate rotation - systemd path units for legacy cert
    nixfleet.systemd.units = lib.mkIf cfg.rotation.enable (
      lib.mkMerge (
        # Legacy rotation watchers
        (map (serviceName: {
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
        }) cfg.rotation.watchServices)
        ++ (map (serviceName: {
          "nixfleet-cert-reload-${serviceName}" = {
            Unit = {
              Description = "Reload ${serviceName} after certificate change";
            };
            Service = {
              Type = "oneshot";
              ExecStart = "${pkgs.systemd}/bin/systemctl reload-or-restart ${serviceName}.service";
            };
          };
        }) cfg.rotation.watchServices)
        # Per-certificate rotation watchers
        ++ (lib.flatten (
          lib.mapAttrsToList (
            certName: certCfg:
            map (serviceName: certWatcherUnit certName certCfg serviceName) certCfg.reloadServices
            ++ map (serviceName: certReloadUnit certName serviceName) certCfg.reloadServices
          ) enabledCerts
        ))
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
