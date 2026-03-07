# NixFleet TPM2 Auto-Unlock Module
# Configures initramfs hooks for automatic LUKS keystore unlock via TPM2
# on ZFS-on-LUKS Ubuntu hosts. Deploys two initramfs hooks:
#   1. cryptsetup-tpm2 — copies TPM2 token plugin + tss2 libs into initramfs
#   2. zfs-tpm2-patch  — patches ZFS script to try token-only before prompting
#
# After deploying, TPM2 enrollment must be done once manually per host:
#   sudo systemd-cryptenroll /dev/zvol/rpool/keystore --tpm2-device=auto --tpm2-pcrs=7
{
  config,
  pkgs,
  lib,
  ...
}:

let
  cfg = config.nixfleet.modules.tpm2Unlock;
in
{
  options.nixfleet.modules.tpm2Unlock = {
    enable = lib.mkEnableOption "TPM2 auto-unlock for ZFS-on-LUKS keystore";

    pcrList = lib.mkOption {
      type = lib.types.str;
      default = "7";
      description = ''
        PCR registers to bind TPM2 enrollment to.
        PCR 7 (Secure Boot state) is recommended — survives kernel/GRUB updates.
        Avoid PCR 0/2/4 which change on firmware or bootloader updates.
      '';
    };

    keystoreDevice = lib.mkOption {
      type = lib.types.str;
      default = "/dev/zvol/rpool/keystore";
      description = "Path to the LUKS-encrypted ZFS keystore device";
    };
  };

  config = lib.mkIf cfg.enable {
    nixfleet.files = {
      # ========================================================================
      # Initramfs hook: copy TPM2 token plugin + tss2 runtime deps
      # ========================================================================
      "/etc/initramfs-tools/hooks/cryptsetup-tpm2" = {
        mode = "0755";
        owner = "root";
        group = "root";
        text = ''
          #!/bin/sh
          PREREQ="cryptroot"
          prereqs() { echo "$PREREQ"; }
          case $1 in prereqs) prereqs; exit 0;; esac
          . /usr/share/initramfs-tools/hook-functions

          PLUGIN=/usr/lib/x86_64-linux-gnu/cryptsetup/libcryptsetup-token-systemd-tpm2.so

          if [ -f "$PLUGIN" ]; then
              # Ensure destination is a directory, then copy the plugin
              mkdir -p "''${DESTDIR}/usr/lib/x86_64-linux-gnu/cryptsetup"
              cp -p "$PLUGIN" "''${DESTDIR}/usr/lib/x86_64-linux-gnu/cryptsetup/"

              # Resolve linked deps via copy_exec
              copy_exec "$PLUGIN"

              # systemd's libsystemd-shared dlopen()'s tss2 libs at runtime —
              # ldd/copy_exec can't discover these, so add them manually
              for lib in \
                  /usr/lib/x86_64-linux-gnu/libtss2-esys.so.0 \
                  /usr/lib/x86_64-linux-gnu/libtss2-mu.so.0 \
                  /usr/lib/x86_64-linux-gnu/libtss2-rc.so.0 \
                  /usr/lib/x86_64-linux-gnu/libtss2-tctildr.so.0 \
                  /usr/lib/x86_64-linux-gnu/libtss2-tcti-device.so.0; do
                  if [ -f "$lib" ]; then
                      copy_exec "$lib"
                  fi
              done
          fi

          # Copy TPM udev rule for device node creation in initramfs
          if [ -f /lib/udev/rules.d/60-tpm-udev.rules ]; then
              mkdir -p "''${DESTDIR}/usr/lib/udev/rules.d"
              cp /lib/udev/rules.d/60-tpm-udev.rules "''${DESTDIR}/usr/lib/udev/rules.d/"
          fi
        '';
      };

      # ========================================================================
      # Initramfs hook: patch ZFS script to try TPM2 token before prompting
      # ========================================================================
      "/etc/initramfs-tools/hooks/zfs-tpm2-patch" = {
        mode = "0755";
        owner = "root";
        group = "root";
        text = ''
          #!/bin/sh
          set -e
          PREREQ=""
          prereqs() { echo "$PREREQ"; }
          case $1 in prereqs) prereqs; exit 0;; esac
          . /usr/share/initramfs-tools/hook-functions

          if [ -f "''${DESTDIR}/scripts/zfs" ]; then
              # Patch the ZFS initramfs script to try TPM2 token-only unlock
              # before falling back to the interactive cryptroot prompt.
              # The original line writes a crypttab entry and calls cryptroot;
              # the patched version tries cryptsetup --token-only first.
              sed -i 's|echo "keystore-''${pool} ''${ks} none luks,discard" >> "''${TABFILE}"|# Try TPM2 token first, fall back to interactive cryptroot\
          			if ! cryptsetup open --token-only "''${ks}" "keystore-''${pool}" 2>/dev/null; then\
          				echo "keystore-''${pool} ''${ks} none luks,discard" >> "''${TABFILE}"\
          			fi|' "''${DESTDIR}/scripts/zfs"
              echo "zfs-tpm2-patch: Patched /scripts/zfs to try TPM2 before cryptroot"
          fi
        '';
      };

      # ========================================================================
      # Add keystore-rpool to crypttab (for systemd post-boot handling)
      # ========================================================================
      "/etc/nixfleet/tpm2-unlock-crypttab-entry" = {
        mode = "0644";
        owner = "root";
        group = "root";
        text = ''
          # Managed by NixFleet tpm2-unlock module
          # This entry tells systemd-cryptsetup-generator about the keystore.
          # The actual boot-time unlock is handled by the initramfs TPM2 hooks.
          # keystore-rpool ${cfg.keystoreDevice} none luks,discard,tpm2-device=auto
        '';
      };
    };

    nixfleet.hooks = {
      postActivate = ''
        # Ensure keystore-rpool entry exists in /etc/crypttab
        if ! grep -q 'keystore-rpool' /etc/crypttab 2>/dev/null; then
          echo 'keystore-rpool ${cfg.keystoreDevice} none luks,discard,tpm2-device=auto' >> /etc/crypttab
          echo "tpm2-unlock: Added keystore-rpool to /etc/crypttab"
        fi

        # Ensure tpm2-tools is installed
        if ! dpkg -l tpm2-tools >/dev/null 2>&1; then
          apt-get install -y tpm2-tools
          echo "tpm2-unlock: Installed tpm2-tools"
        fi

        # Rebuild initramfs with TPM2 hooks
        echo "tpm2-unlock: Updating initramfs..."
        update-initramfs -u -k all

        echo "tpm2-unlock: Initramfs updated with TPM2 hooks"
        echo "tpm2-unlock: If TPM2 is not yet enrolled, run manually:"
        echo "  sudo systemd-cryptenroll ${cfg.keystoreDevice} --tpm2-device=auto --tpm2-pcrs=${cfg.pcrList}"
      '';
    };

    nixfleet.healthChecks = {
      tpm2-device = {
        type = "command";
        command = "test -c /dev/tpmrm0";
        timeout = 5;
      };
      tpm2-enrolled = {
        type = "command";
        command = "cryptsetup luksDump ${cfg.keystoreDevice} 2>/dev/null | grep -q 'systemd-tpm2'";
        timeout = 10;
      };
    };
  };
}
