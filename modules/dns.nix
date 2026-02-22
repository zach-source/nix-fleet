# NixFleet Unbound DNS Module
# Runs Unbound as a local DNS server with:
#   - Local A/PTR records for fleet hosts under a configurable domain
#   - DNS-over-TLS forwarding to upstream resolvers (Cloudflare by default)
#   - DNSSEC validation
#   - Aggressive caching
{
  config,
  pkgs,
  lib,
  ...
}:

let
  cfg = config.nixfleet.modules.dns;

  # Generate local-data and local-data-ptr lines from the localRecords attrset
  localRecordLines = lib.concatStringsSep "\n" (
    lib.mapAttrsToList (name: ip: ''
      local-data: "${name}.${cfg.domain}. IN A ${ip}"
      local-data-ptr: "${ip} ${name}.${cfg.domain}"'') cfg.localRecords
  );

  # Generate forward-addr lines from the forwarders list
  forwardAddrLines = lib.concatMapStringsSep "\n" (fwd: "    forward-addr: ${fwd}") cfg.forwarders;

  # Generate interface lines from listenAddresses
  interfaceLines = lib.concatMapStringsSep "\n" (addr: "    interface: ${addr}") cfg.listenAddresses;

  # Generate domain-insecure lines
  insecureLines = lib.concatMapStringsSep "\n" (
    d: "    domain-insecure: \"${d}\""
  ) cfg.insecureDomains;

  # Whitelist pattern for grep -v -E
  whitelistPattern = lib.concatStringsSep "|" cfg.adblock.whitelist;

  # Adblock update script
  adblockUpdateScript = ''
    #!/bin/bash
    set -euo pipefail

    CONF="/etc/unbound/adblock.conf"
    TMP=$(mktemp)

    trap 'rm -f "$TMP"' EXIT

    echo "Downloading blocklists..."
    ${lib.concatMapStringsSep "\n" (url: ''
      curl -fsSL "${url}" >> "$TMP" || echo "Warning: failed to download ${url}" >&2
    '') cfg.adblock.blocklists}

    echo "Processing domains..."
    # Extract domains from hosts-format lines, filter whitelist, deduplicate, convert to unbound format
    grep -Eh '^(0\.0\.0\.0|127\.0\.0\.1)\s' "$TMP" \
      | awk '{print $2}' \
      | grep -v '^\s*$' \
      | grep -v 'localhost' \
      | grep -v -E '(${whitelistPattern})' \
      | sort -u \
      | awk '{print "local-zone: \""$1".\" always_nxdomain"}' \
      > "$CONF"

    COUNT=$(wc -l < "$CONF")
    echo "Adblock: $COUNT domains blocked"

    # Reload unbound
    if systemctl is-active --quiet nixfleet-dns.service; then
      systemctl restart nixfleet-dns.service
      echo "Unbound reloaded"
    fi
  '';

  # Build unbound.conf from options
  unboundConf = ''
    server:
    ${interfaceLines}
        port: ${toString cfg.port}

        # Access control
        access-control: 127.0.0.0/8 allow
        access-control: ::1/128 allow
        access-control: ${cfg.accessControl} allow
        access-control: 0.0.0.0/0 refuse
        access-control: ::/0 refuse

        # Run as unbound user
        username: "unbound"
        directory: "/etc/unbound"
        pidfile: ""

        # Logging
        verbosity: 1
        use-syslog: no
        logfile: ""
        log-queries: no

        ${lib.optionalString cfg.enableDnssec ''
          # DNSSEC
          auto-trust-anchor-file: /var/lib/unbound/root.key
        ''}

        # Privacy
        qname-minimisation: yes
        hide-identity: yes
        hide-version: yes

        # Performance
        num-threads: 2
        so-reuseport: yes
        prefetch: yes
        prefetch-key: yes

        # Cache
        msg-cache-size: ${cfg.cacheSize}
        rrset-cache-size: ${cfg.rrsetCacheSize}
        cache-min-ttl: 300
        cache-max-ttl: 86400
        serve-expired: yes
        serve-expired-ttl: 86400

        # Local zone
        local-zone: "${cfg.domain}." static
        include: /etc/unbound/local-records.conf
    ${lib.optionalString cfg.adblock.enable ''
      include: /etc/unbound/adblock.conf
    ''}

    ${lib.optionalString (cfg.insecureDomains != [ ]) ''
      ${insecureLines}
    ''}

        # TLS for upstream
        tls-cert-bundle: /etc/ssl/certs/ca-certificates.crt

    ${lib.optionalString (cfg.forwarders != [ ]) ''
      forward-zone:
          name: "."
          ${lib.optionalString cfg.enableDoT "forward-tls-upstream: yes"}
      ${forwardAddrLines}
    ''}
    ${cfg.extraConfig}
  '';

  # Build local-records.conf
  localRecordsConf = ''
    ${localRecordLines}
    ${lib.concatStringsSep "\n" (map (r: "    ${r}") cfg.extraLocalRecords)}
  '';
in
{
  options.nixfleet.modules.dns = {
    enable = lib.mkEnableOption "NixFleet Unbound DNS server";

    listenAddresses = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "0.0.0.0" ];
      description = "Interface addresses for Unbound to bind to. Use specific IPs to avoid conflict with systemd-resolved.";
      example = [
        "192.168.3.131"
        "127.0.0.1"
      ];
    };

    port = lib.mkOption {
      type = lib.types.port;
      default = 53;
      description = "DNS port";
    };

    domain = lib.mkOption {
      type = lib.types.str;
      default = "stigen.lan";
      description = "Local domain for fleet DNS records";
    };

    accessControl = lib.mkOption {
      type = lib.types.str;
      default = "192.168.0.0/16";
      description = "CIDR range allowed to query this DNS server";
    };

    forwarders = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [
        "1.1.1.1@853#cloudflare-dns.com"
        "1.0.0.1@853#one.one.one.one"
      ];
      description = "Upstream DNS forwarders (format: addr@port#tls-auth-name for DoT)";
    };

    enableDoT = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Use DNS-over-TLS for upstream queries";
    };

    enableDnssec = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Enable DNSSEC validation";
    };

    localRecords = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = { };
      description = "Hostname to IP mapping for local A/PTR records under the configured domain";
      example = lib.literalExpression ''
        {
          gtr = "192.168.3.31";
          gti = "192.168.3.131";
        }
      '';
    };

    extraLocalRecords = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Additional raw local-data lines for advanced entries (CNAME, SRV, etc.)";
      example = [
        "local-data: \"_http._tcp.stigen.lan. IN SRV 0 0 80 gti.stigen.lan.\""
      ];
    };

    cacheSize = lib.mkOption {
      type = lib.types.str;
      default = "50m";
      description = "Message cache size";
    };

    rrsetCacheSize = lib.mkOption {
      type = lib.types.str;
      default = "100m";
      description = "RRset cache size (should be roughly 2x msg-cache-size)";
    };

    insecureDomains = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Domains to skip DNSSEC validation for (e.g. local forward zones)";
      example = [
        "local"
        "3.168.192.in-addr.arpa"
      ];
    };

    extraConfig = lib.mkOption {
      type = lib.types.lines;
      default = "";
      description = "Extra lines appended to unbound.conf";
    };

    adblock = {
      enable = lib.mkEnableOption "DNS-based ad blocking via blocklists";

      blocklists = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [
          "https://raw.githubusercontent.com/StevenBlack/hosts/master/hosts"
        ];
        description = "URLs of hosts-format blocklists to download";
      };

      whitelist = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [
          "amazon"
          "amzn"
          "assoc-amazon"
          "google"
          "googlesyndication"
          "googleadservices"
          "googletagmanager"
          "doubleclick"
          "2mdn"
        ];
        description = "Domain patterns to exclude from blocking (grep -E patterns)";
      };

      updateSchedule = lib.mkOption {
        type = lib.types.str;
        default = "*-*-* 04:00:00";
        description = "OnCalendar schedule for blocklist updates";
      };
    };
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
      # ============================================================================
      # Required packages
      # ============================================================================
      nixfleet.packages = [ pkgs.unbound-full ];

      # ============================================================================
      # Directories
      # ============================================================================
      nixfleet.directories = {
        "/etc/unbound" = {
          mode = "0755";
          owner = "root";
          group = "root";
        };
        "/var/lib/unbound" = {
          mode = "0755";
          owner = "unbound";
          group = "unbound";
        };
      };

      # ============================================================================
      # Configuration files
      # ============================================================================
      nixfleet.files = {
        "/etc/unbound/unbound.conf" = {
          mode = "0644";
          owner = "root";
          group = "root";
          text = unboundConf;
          restartUnits = [ "nixfleet-dns.service" ];
        };

        "/etc/unbound/local-records.conf" = {
          mode = "0644";
          owner = "root";
          group = "root";
          text = localRecordsConf;
          restartUnits = [ "nixfleet-dns.service" ];
        };

        # Diagnostic script
        "/usr/local/bin/dns-check" = {
          mode = "0755";
          owner = "root";
          group = "root";
          text = ''
            #!/bin/bash
            set -euo pipefail

            echo "=== NixFleet DNS Server Status ==="
            echo ""

            # Service status
            echo "--- Service ---"
            systemctl is-active nixfleet-dns.service && echo "Status: running" || echo "Status: NOT running"
            echo ""

            # Local domain resolution
            echo "--- Local Records (${cfg.domain}) ---"
            ${lib.concatStringsSep "\n" (
              lib.mapAttrsToList (
                name: ip:
                ''echo -n "${name}.${cfg.domain}: " && dig +short @127.0.0.1 -p ${toString cfg.port} ${name}.${cfg.domain} A || echo "FAILED"''
              ) cfg.localRecords
            )}
            echo ""

            # Reverse lookups
            echo "--- Reverse Lookups ---"
            ${lib.concatStringsSep "\n" (
              lib.mapAttrsToList (
                name: ip:
                let
                  octets = lib.splitString "." ip;
                  reverseAddr = lib.concatStringsSep "." (lib.reverseList octets) + ".in-addr.arpa";
                in
                ''echo -n "${ip} -> " && dig +short @127.0.0.1 -p ${toString cfg.port} -x ${ip} || echo "FAILED"''
              ) cfg.localRecords
            )}
            echo ""

            # Upstream resolution
            echo "--- Upstream Resolution ---"
            echo -n "google.com: " && dig +short @127.0.0.1 -p ${toString cfg.port} google.com A | head -1 || echo "FAILED"
            echo ""

            # DNSSEC test
            echo "--- DNSSEC ---"
            RESULT=$(dig @127.0.0.1 -p ${toString cfg.port} cloudflare.com +dnssec +short 2>/dev/null) && echo "DNSSEC query OK" || echo "DNSSEC query FAILED"
            dig @127.0.0.1 -p ${toString cfg.port} cloudflare.com +dnssec 2>/dev/null | grep -q "ad" && echo "AD flag: present (validated)" || echo "AD flag: absent"
            echo ""

            # Cache stats
            echo "--- Cache Statistics ---"
            unbound-control stats_noreset 2>/dev/null | grep -E "^(total|num\.cachehits|num\.cachemiss|num\.queries)" || echo "(unbound-control not configured)"
            echo ""

            echo "=== Done ==="
          '';
        };
      };

      # ============================================================================
      # Users — ensure unbound system user exists
      # ============================================================================
      nixfleet.users.unbound = {
        uid = 999;
        group = "unbound";
        home = "/var/lib/unbound";
        shell = "/usr/sbin/nologin";
        system = true;
      };

      nixfleet.groups.unbound = {
        gid = 999;
        system = true;
      };

      # ============================================================================
      # Systemd service
      # ============================================================================
      nixfleet.systemd.units."nixfleet-dns.service" = {
        text = ''
          [Unit]
          Description=NixFleet Unbound DNS Server
          After=network-online.target
          Wants=network-online.target
          Before=nss-lookup.target
          Wants=nss-lookup.target

          [Service]
          Type=simple
          ExecStartPre=/bin/bash -c 'id unbound &>/dev/null || useradd -r -s /usr/sbin/nologin -d /var/lib/unbound unbound'
          ${lib.optionalString cfg.enableDnssec ''ExecStartPre=/usr/sbin/unbound-anchor -a /var/lib/unbound/root.key || true''}
          ExecStart=/usr/sbin/unbound -d -c /etc/unbound/unbound.conf
          Restart=on-failure
          RestartSec=10

          # Security hardening
          ProtectSystem=full
          ProtectHome=yes
          PrivateDevices=yes
          NoNewPrivileges=false
          AmbientCapabilities=CAP_NET_BIND_SERVICE

          [Install]
          WantedBy=multi-user.target
        '';
        enabled = true;
      };

      # ============================================================================
      # Health checks
      # ============================================================================
      nixfleet.healthChecks = {
        dns-service = {
          type = "command";
          command = "systemctl is-active nixfleet-dns";
          timeout = 5;
        };
        dns-resolution = {
          type = "command";
          command = "dig +short +timeout=3 @127.0.0.1 -p ${toString cfg.port} ${cfg.domain} SOA >/dev/null 2>&1 || dig +short +timeout=3 @127.0.0.1 -p ${toString cfg.port} ${cfg.domain} >/dev/null 2>&1";
          timeout = 10;
        };
      };
    })

    (lib.mkIf (cfg.enable && cfg.adblock.enable) {
      # ============================================================================
      # Adblock — blocklist-based DNS ad blocking
      # ============================================================================
      nixfleet.files = {
        "/etc/unbound/adblock.conf" = {
          mode = "0644";
          owner = "root";
          group = "root";
          text = "";
          restartUnits = [ "nixfleet-dns.service" ];
        };

        "/usr/local/bin/adblock-update" = {
          mode = "0755";
          owner = "root";
          group = "root";
          text = adblockUpdateScript;
        };
      };

      nixfleet.systemd.units = {
        "nixfleet-adblock-update.service" = {
          text = ''
            [Unit]
            Description=NixFleet Adblock Blocklist Update
            After=network-online.target
            Wants=network-online.target

            [Service]
            Type=oneshot
            ExecStart=/usr/local/bin/adblock-update
          '';
          enabled = false;
        };

        "nixfleet-adblock-update.timer" = {
          text = ''
            [Unit]
            Description=NixFleet Adblock Blocklist Update Timer

            [Timer]
            OnCalendar=${cfg.adblock.updateSchedule}
            RandomizedDelaySec=1800
            Persistent=true

            [Install]
            WantedBy=timers.target
          '';
          enabled = true;
        };
      };
    })
  ];
}
