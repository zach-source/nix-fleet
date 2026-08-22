# BIOS/UEFI Configuration from Linux (uefisettings)

Reading — and potentially writing — AMI Aptio BIOS settings on the Beelink fleet
from a running Linux host, with no BMC and no reboot.

Investigated 2026-08-20. **Reads are verified on real hardware. Writes are NOT —
see [The write path](#the-write-path-untested) before touching anything.**

## Why this exists

The fleet has no out-of-band management. Confirmed empirically:

| Mechanism | Result |
|-----------|--------|
| IPMI / BMC | SMBIOS Type 38 returns **0 blocks** on gti *and* gtr-150. No `/dev/ipmi*`, no ipmi modules. Nothing for `ipmitool` to talk to. |
| Intel AMT (gti only) | Ports 16992–16995 all **closed**, `0` AMT/vPro references in dmidecode. `/dev/mei0` + `mei state: ENABLED` is just the Management Engine, present on every Intel chip — not provisioned AMT. |
| jetkvm | `jetkvm-gtr` (.120) and `jetkvm-gti` (.135) are DNS-only, unreachable. |

AMT could never have covered the AMD gtr nodes anyway. gtr-152 was down twelve
days in Aug 2026 and needed physical hands.

`uefisettings` doesn't give you remote power. It gives you the **BIOS settings
that decide how a box behaves when power returns** — which is the other half of
the same problem.

## Verdict

Works on the four AMD gtr nodes. Does not work on gti.

| | gti (Intel GTi15) | gtr-150/151/152/153 (AMD GTR Pro) |
|---|---|---|
| BIOS vendor | AMI Aptio | AMI Aptio |
| BIOS version | `GTi15T203` / rev 5.32 | `GTRP108` / rev 5.36 |
| Board | AZW GTi15 | AZW GTR Pro |
| `identify` backend | `Backend::Hii` | `Backend::Hii` |
| `hii show-ifr` | **614 lines** — SDEV entries, firmware hashes, no setup menu | **15,025 lines** — the real setup menu |
| Answers resolved | 75 (55 errored) | **754** (505 errored) |
| Operationally useful | effectively none | AMD CBS subset |

gti reports `Backend::Hii` and exits 0, but its setup FormSet is never published
to the runtime HII database. Every `get` for a real setting returns nothing.

**Key insight:** efivarfs presence does *not* predict HII exposure — it inverted
here. gti is the host that **has** `Setup-ec87d643…` in efivarfs; the gtr nodes
**don't**, yet the gtr nodes are the ones where this works. Test, don't reason.

## Install

`uefisettings` is in nixpkgs (`0-unstable-2025-07-29`) and prebuilt in the binary
cache. No Rust toolchain on hosts, no `cargo install`:

```bash
nix build --no-link --print-out-paths nixpkgs#uefisettings
```

`meta.platforms` includes `aarch64-linux`, so it is also available for the DGX
Sparks — untested there, and unlikely to be useful given NVIDIA's firmware.

## Reproducing

All read-only, all safe:

```bash
U=$(nix build --no-link --print-out-paths nixpkgs#uefisettings)/bin/uefisettings

sudo $U identify                      # backend + board identity
sudo $U hii show-ifr > /tmp/ifr.txt   # full forms dump
sudo $U get "Ac Loss Control"         # single question
```

Useful greps against the dump:

```bash
# what's actually controllable
grep -iE "Q: .*(iommu|svm|power loss|wake|above 4g|pxe|watchdog)" /tmp/ifr.txt

# resolved vs errored
grep -c -- "-Answer:" /tmp/ifr.txt
grep -c "VStoreError" /tmp/ifr.txt

# which varstores are failing, ranked
grep -o "efivars/[A-Za-z_0-9]*-" /tmp/ifr.txt | sort | uniq -c | sort -rn
```

## Why 40% of settings fail

505 of 1259 questions on a gtr node return:

```
-Answer: <VStoreError: failed to open sysfs efivars
         '/sys/firmware/efi/efivars/Setup-ec87d643-eba4-4bb5-a1e5-3f3e36b20da9'>
```

The HII forms *reference* a varstore that the firmware never exposes at runtime.
AMI declares these boot-services-only (no `EFI_VARIABLE_RUNTIME_ACCESS`), so they
disappear from efivarfs before the OS starts. The forms, help text and value
enumerations still parse fine — only the current value is unreadable.

Failures ranked by varstore on gtr-150:

```
139  PmfSetupVar-      42  UsbSupport-       16  FixedBootPriorities-
132  Setup-            38  IT8613_SMF-       10  ROM_CMN-
                       35  NicCfgData-        7  NetworkStackVar-
```

Settings that are therefore **not reachable**, despite appearing in the dump:

- `Wake On LAN` — `NicCfgData-e2c85968…` absent
- `Network Stack`, `Wake system from S5` — `Setup-ec87d643…` absent
- `Above 4G Decoding`, `Re-Size BAR Support` — unresolvable

WoL being unreachable is worth noting: the fleet's `Wake-on: g` is the NIC
default asserted by the OS, and this tool cannot see or change the firmware-level
setting behind it.

## Reachable settings

754 questions resolve on a gtr node, nearly all inside the **AMD CBS** FormSet
(`B04535E3-3004-4946-9EB7-149428983053`) backed by varstore `AmdSetup`. Mostly
memory timings and curve-optimizer knobs. The operationally interesting ones:

```
Ac Loss Control : "Always Off"   [Always Off | Always On | Last State]
IOMMU           : "Auto"         [Disabled | Enabled | Auto]
SVM Enable      : "Auto"         [Enabled | Disabled | Auto]
```

### Ac Loss Control is the prize

**Every gtr node is currently `Always Off`.** An AC event leaves them dark until
someone drives over.

Setting it to `Always On` is what makes a smart PDU an actual power *lever*
rather than just a remote off switch. `Always On` is the right value for servers;
`Last State` would leave a deliberately-powered-down box off, which is not what
you want from a recovery mechanism.

This would not have saved gtr-152 — that box was hung, not unpowered — but it
closes the other half of the problem.

## The write path (UNTESTED)

Writability posture on `AmdSetup` (verified read-only):

```
attributes : NON_VOLATILE | BOOTSERVICE_ACCESS | RUNTIME_ACCESS   (0x7)
efivarfs   : rw,nosuid,nodev,noexec,relatime
inode flag : ----i----------------   (standard efivarfs; tools clear it)
```

`RUNTIME_ACCESS` on a `rw` mount means the OS **can** write it. Whether AMI
accepts the result, and whether the setting survives a power cycle, is unverified.

```bash
sudo $U set "Ac Loss Control" "Always On"   # NOT YET RUN ON ANY HOST
```

### Do not do this remotely

A bad AMI NVRAM write can leave a box unbootable. Recovery is a CMOS jumper or a
battery pull — physical access. This fleet has no BMC, no AMT and no reachable
jetkvm, so a brick costs a trip, and gtr-152 established what that costs.

**Also skip the `setup_var` raw-offset technique entirely.** Poking NVRAM at
computed offsets without HII validation is the standard way to brick an AMI
board, and there is no recovery path here to absorb the mistake.

### Protocol when someone is at the machines

1. Record current state first: `sudo $U get "Ac Loss Control" -j > before.json`
2. Write on **one** node. Never gti — it is the k0s control plane and the tunnel
   SNAT host; every remote path into the fleet dies with it.
3. Read back. Confirm the value changed.
4. Reboot. Confirm it survived and the box still boots.
5. Pull the physical power. Confirm it comes back on its own.
6. Only then roll to the remaining three.

## Not yet done

- `uefisettings` is not declared in any host config. It belongs in
  `modules/gtr.nix`, not `modules/base-packages.nix` — it is a no-op on gti and
  the DGX Sparks, and shipping a useless binary fleet-wide is bloat.
- No health check asserts `Ac Loss Control`. Adding one turns BIOS state into
  something recorded and diffable, the same gap as gti's kernel pin — currently
  imperative (GRUB `saved_entry` + apt hold) and written down nowhere.
- The write itself.

## References

- <https://github.com/linuxboot/uefisettings>
- Fleet WoL / power facts: `FLEET.md`, and the `fleet-wol-unifi` note
