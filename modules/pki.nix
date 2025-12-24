# PKI module - TLS certificate management for fleet hosts
{
  config,
  pkgs,
  lib,
  ...
}:

let
  cfg = config.nixfleet.pki;
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
    ];

    # Environment variables for easy access
    nixfleet.environment = lib.mkIf (cfg.caCert != null) {
      NIXFLEET_CA_CERT = "${cfg.certDir}/ca.crt";
      NIXFLEET_HOST_CERT = lib.mkIf (cfg.hostCert != null) "${cfg.certDir}/host.crt";
      NIXFLEET_HOST_KEY = lib.mkIf (cfg.hostKey != null) "${cfg.certDir}/host.key";
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
  };
}
