# ZFS TPM2 Auto-Decryption for Ubuntu Hosts

Enable automatic ZFS encrypted pool unlocking at boot using TPM2, eliminating manual passphrase entry while maintaining security.

## Current Architecture

**Ubuntu ZFS-on-root with encrypted keystore:**

```
┌─────────────────────────────────────────────────────────────────┐
│ zd0 (LUKS zvol)                                                 │
│   └── keystore-rpool (ext4, 4MB)                                │
│         └── /run/keystore/rpool/system.key (32-byte raw key)    │
│               └── rpool (ZFS aes-256-gcm encrypted)             │
└─────────────────────────────────────────────────────────────────┘
```

**Key insight:** The ZFS key is stored in a LUKS-encrypted zvol. We just need to add TPM2 to this LUKS volume using `systemd-cryptenroll`.

## Boot Sequence

### Current (with passphrase)
```
1. BIOS/UEFI starts
2. GRUB loads kernel from bpool (unencrypted)
3. initramfs prompts for LUKS passphrase
4. LUKS unlocks zd0 → mounts keystore-rpool
5. ZFS reads key from /run/keystore/rpool/system.key
6. rpool mounts, boot continues
```

### Target (with TPM2)
```
1. BIOS/UEFI starts, TPM2 measures boot components
2. GRUB loads kernel from bpool (unencrypted)
3. initramfs asks TPM2 to unseal LUKS key (validates PCRs)
4. LUKS unlocks zd0 → mounts keystore-rpool
5. ZFS reads key from /run/keystore/rpool/system.key
6. rpool mounts, boot continues (NO PROMPT!)
```

## TPM Enrollment

### Prerequisites

Ensure you have:
1. Current LUKS passphrase stored securely (1Password, etc.)
2. USB recovery drive with Ubuntu live environment
3. Backup of `/etc/crypttab`

### Enrollment Steps

```bash
# 1. Verify TPM2 is available
sudo systemd-cryptenroll --tpm2-device=list

# 2. Find the LUKS device (it's the zvol backing the keystore)
LUKS_DEV=$(sudo cryptsetup status keystore-rpool | grep device | awk '{print $2}')
echo "LUKS device: $LUKS_DEV"

# 3. Enroll TPM2 with strict PCR binding (0,2,4,7)
sudo systemd-cryptenroll "$LUKS_DEV" \
  --tpm2-device=auto \
  --tpm2-pcrs=0+2+4+7

# 4. Update initramfs to use TPM2
sudo update-initramfs -u -k all

# 5. Test (reboot and verify no passphrase prompt)
sudo reboot
```

## Recovery

**If TPM fails to unseal:**
- Boot will fall back to passphrase prompt automatically
- No lockout - just inconvenience

**From live USB:**
```bash
sudo cryptsetup open /dev/zd0 keystore-rpool
sudo mount /dev/mapper/keystore-rpool /mnt
# Then continue boot manually
```

## After BIOS/GRUB Updates

PCR values change after firmware or bootloader updates. Re-enroll:

```bash
LUKS_DEV=$(sudo cryptsetup status keystore-rpool | grep device | awk '{print $2}')
sudo systemd-cryptenroll "$LUKS_DEV" --wipe-slot=tpm2
sudo systemd-cryptenroll "$LUKS_DEV" --tpm2-device=auto --tpm2-pcrs=0+2+4+7
sudo update-initramfs -u -k all
```

## Security Model

| PCR | Measures | Breaks on |
|-----|----------|-----------|
| 0 | BIOS/UEFI firmware | Firmware update |
| 2 | Option ROM code | GPU/NIC firmware |
| 4 | Boot manager (GRUB) | GRUB update |
| 7 | Secure Boot state | Secure Boot changes |

**Fallback**: If TPM unsealing fails, system prompts for passphrase.

**Store recovery passphrase securely** - needed if:
- TPM fails or is cleared
- Motherboard replaced
- Major boot chain changes

## Optional: NixFleet Helper Module

Deploy enrollment script via NixFleet:

```nix
# modules/tpm-enroll.nix
{
  files."/usr/local/bin/enroll-tpm-keystore" = {
    mode = "0755";
    owner = "root";
    text = ''
      #!/bin/bash
      set -euo pipefail

      echo "=== TPM2 Enrollment for ZFS Keystore ==="

      LUKS_DEV=$(cryptsetup status keystore-rpool | grep device | awk '{print $2}')

      if [ -z "$LUKS_DEV" ]; then
        echo "ERROR: keystore-rpool not found"
        exit 1
      fi

      echo "LUKS device: $LUKS_DEV"

      if cryptsetup luksDump "$LUKS_DEV" | grep -q "systemd-tpm2"; then
        echo "TPM2 already enrolled!"
        cryptsetup luksDump "$LUKS_DEV" | grep -A5 "Tokens:"
        exit 0
      fi

      echo "Enrolling TPM2 with PCRs 0+2+4+7..."
      systemd-cryptenroll "$LUKS_DEV" \
        --tpm2-device=auto \
        --tpm2-pcrs=0+2+4+7

      echo "Updating initramfs..."
      update-initramfs -u -k all

      echo ""
      echo "=== SUCCESS ==="
      echo "Reboot to test automatic unlock."
      echo "Keep your passphrase safe for recovery!"
    '';
  };
}
```
