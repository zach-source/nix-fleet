#!/usr/bin/env python3
"""
NixFleet Pixiecore API Backend

Serves boot configurations to pixiecore based on MAC address.
Supports four modes per target:
  - install:          Ubuntu autoinstall with NixFleet preinstalled
  - recovery:         Ubuntu live boot with ZFS/LUKS recovery tools
  - dgx-spark:        DGX OS autoinstall from an ISO (needs 'iso_url')
  - dgx-spark-fastos: DGX Spark FastOS recovery image (needs 'fastos_url')

CAVEAT on the DGX Spark modes: the cmdlines and boot-file layout below follow
NVIDIA's PXE guide and are believed correct, but the transport is NOT verified.
NVIDIA documents netbooting a Spark with a signed arm64 GRUB
(grubnetaa64.efi.signed, served via DHCP next-server/filename), whereas
pixiecore drives its own DHCP-proxy and iPXE chain, which is x86-centric.
Whether pixiecore can netboot an aarch64 Spark has not been tested here. If it
can't, these builders are still the right cmdlines — point a plain
GRUB/TFTP/DHCP setup at them instead, per the guide.

Also note Secure Boot must be disabled, or grubnetaa64.efi.signed enrolled in
UEFI, before a Spark will PXE boot at all.

Ref: https://docs.nvidia.com/dgx/dgx-spark/pxe.html

Pixiecore calls GET /v1/boot/<mac> and expects JSON:
  { "kernel": "file:///path/vmlinuz",
    "initrd": ["file:///path/initrd"],
    "cmdline": "..." }

Targets are managed via /srv/installer/pxe-targets.json

Usage:
  python3 pxe-api.py [--port 8891] [--boot-dir /srv/installer/boot]
"""

import json
import os
import sys
from http.server import HTTPServer, BaseHTTPRequestHandler
from pathlib import Path

# Configuration
BOOT_DIR = os.environ.get("BOOT_DIR", "/srv/installer/boot")
TARGETS_FILE = os.environ.get("TARGETS_FILE", "/srv/installer/pxe-targets.json")
INSTALLER_URL = os.environ.get("INSTALLER_URL", "http://192.168.3.131:8889")
API_PORT = int(os.environ.get("API_PORT", "8891"))


def load_targets():
    """Load PXE targets from JSON config file."""
    try:
        with open(TARGETS_FILE) as f:
            return json.load(f)
    except FileNotFoundError:
        return {}
    except json.JSONDecodeError as e:
        print(f"[pxe-api] ERROR: Invalid JSON in {TARGETS_FILE}: {e}", file=sys.stderr)
        return {}


def normalize_mac(mac):
    """Normalize MAC address to lowercase colon-separated format."""
    mac = mac.lower().replace("-", ":").strip("/")
    # Handle pixiecore's format: it sends MACs without leading slashes
    return mac


def build_install_config(host):
    """Build boot config for install mode."""
    return {
        "kernel": f"file://{BOOT_DIR}/vmlinuz",
        "initrd": [f"file://{BOOT_DIR}/initrd"],
        "cmdline": (
            f"root=/dev/ram0 ramdisk_size=1500000 "
            f"ip=dhcp "
            f"url={INSTALLER_URL}/boot/ubuntu-25.04.iso "
            f"autoinstall "
            f"ds=nocloud-net\\;s={INSTALLER_URL}/{host}/ "
            f"cloud-config-url=/dev/null "
            "---"
        ),
    }


def build_recovery_config():
    """Build boot config for recovery mode (live Ubuntu, no autoinstall)."""
    return {
        "kernel": f"file://{BOOT_DIR}/vmlinuz",
        "initrd": [f"file://{BOOT_DIR}/initrd"],
        "cmdline": ("ip=dhcp " "---"),
    }


def build_dgx_spark_config(target):
    """Build boot config for a DGX Spark installing DGX OS from an ISO.

    Kernel and initrd come out of the DGX OS ISO's own casper/ directory, so
    they live under a separate subdirectory from the Ubuntu installer's:

        sudo mount -o loop base_os_7.0.0.iso /mnt
        cp /mnt/casper/{vmlinuz,initrd} <BOOT_DIR>/dgx-spark/

    Two of these arguments are DGX-specific and not obvious:
    nvme-core.multipath=n, and nouveau.modeset=0 to keep the open-source
    driver off the GPU during install. Keep this aligned with the ISO's own
    /boot/grub/grub.cfg if you move to a different DGX OS release.

    Ref: https://docs.nvidia.com/dgx/dgx-spark/pxe.html
    """
    iso_url = target.get("iso_url")
    if not iso_url:
        raise ValueError("dgx-spark mode requires 'iso_url' in the target")
    return {
        "kernel": f"file://{BOOT_DIR}/dgx-spark/vmlinuz",
        "initrd": [f"file://{BOOT_DIR}/dgx-spark/initrd"],
        "cmdline": (
            "fsck.mode=skip "
            "autoinstall "
            "ip=dhcp "
            f"url={iso_url} "
            "nvme-core.multipath=n "
            "nouveau.modeset=0"
        ),
    }


def build_dgx_spark_fastos_config(target):
    """Build boot config for DGX Spark FastOS recovery.

    Unlike the ISO path this pulls a tarball over HTTP (fastos_usbimg_url),
    extracted from NVIDIA's dgx-spark-recovery-image archive:

        tar xpfv usb.customer.tar.gz
        cp usbimg.customer/usb/{vmlinuz,initrd} <BOOT_DIR>/dgx-spark-fastos/

    static_ip is optional and takes the form '<ip>:<gateway>'; without it the
    initramfs relies on ip=dhcp alone. Note the serial console runs at 921600,
    not a typo, and sbsa_gwdt.action=1 sets the ARM SBSA watchdog behaviour.
    """
    img_url = target.get("fastos_url")
    if not img_url:
        raise ValueError("dgx-spark-fastos mode requires 'fastos_url' in the target")
    cmdline = (
        "nouveau.modeset=0 "
        "console=tty0 "
        "console=ttyS0,921600 "
        "sbsa_gwdt.action=1 "
        "noui "
        "pxeinstall=true "
        f"fastos_usbimg_url={img_url} "
        "ip=dhcp"
    )
    static_ip = target.get("static_ip")
    if static_ip:
        cmdline += f" static_ip={static_ip}"
    # usb.skipfw bypasses the firmware stage; usb.shell drops to a shell
    # instead of installing. Both are opt-in escape hatches for a bad install.
    for flag in ("usb.skipfw", "usb.shell"):
        if target.get(flag.replace(".", "_")):
            cmdline += f" {flag}"
    return {
        "kernel": f"file://{BOOT_DIR}/dgx-spark-fastos/vmlinuz",
        "initrd": [f"file://{BOOT_DIR}/dgx-spark-fastos/initrd"],
        "cmdline": cmdline,
    }


class PXEAPIHandler(BaseHTTPRequestHandler):
    """Handle pixiecore API requests."""

    def log_message(self, format, *args):
        """Override to add prefix."""
        if args:
            msg = format % args
        else:
            msg = format
        print(f"[pxe-api] {msg}")

    def do_GET(self):
        # Pixiecore calls /v1/boot/<mac-address>
        if not self.path.startswith("/v1/boot/"):
            self.send_response(404)
            self.end_headers()
            return

        mac = normalize_mac(self.path[len("/v1/boot/") :])
        targets = load_targets()

        if mac not in targets:
            # No config for this MAC — pixiecore will ignore it
            self.send_response(404)
            self.end_headers()
            self.log_message(f"SKIP {mac} (not in targets)")
            return

        target = targets[mac]
        mode = target.get("mode", "install")

        try:
            if mode == "install":
                host = target.get("host", "unknown")
                config = build_install_config(host)
                self.log_message(f"INSTALL {mac} -> host={host}")
            elif mode == "recovery":
                config = build_recovery_config()
                self.log_message(f"RECOVERY {mac}")
            elif mode == "dgx-spark":
                config = build_dgx_spark_config(target)
                self.log_message(f"DGX-SPARK {mac} -> {target.get('iso_url')}")
            elif mode == "dgx-spark-fastos":
                config = build_dgx_spark_fastos_config(target)
                self.log_message(
                    f"DGX-SPARK-FASTOS {mac} -> {target.get('fastos_url')}"
                )
            else:
                self.send_response(400)
                self.end_headers()
                self.log_message(f"ERROR {mac}: unknown mode '{mode}'")
                return
        except ValueError as e:
            self.send_response(400)
            self.end_headers()
            self.log_message(f"ERROR {mac}: {e}")
            return

        # Verify boot files exist. Derive the paths from the config rather than
        # assuming BOOT_DIR/vmlinuz — the DGX Spark modes boot a kernel from
        # their own subdirectory, and checking the wrong file would report
        # healthy right up until the machine failed to netboot.
        missing = [
            p[len("file://") :]
            for p in [config["kernel"], *config["initrd"]]
            if p.startswith("file://") and not os.path.isfile(p[len("file://") :])
        ]
        if missing:
            self.send_response(500)
            self.end_headers()
            self.log_message(f"ERROR {mac}: boot files missing: {', '.join(missing)}")
            return

        response = json.dumps(config)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(response.encode())


def main():
    global BOOT_DIR, TARGETS_FILE, INSTALLER_URL

    import argparse

    parser = argparse.ArgumentParser(description="NixFleet Pixiecore API Backend")
    parser.add_argument("--port", type=int, default=API_PORT, help="API port")
    parser.add_argument("--boot-dir", default=BOOT_DIR, help="Boot files directory")
    parser.add_argument(
        "--targets-file", default=TARGETS_FILE, help="PXE targets JSON file"
    )
    parser.add_argument(
        "--installer-url", default=INSTALLER_URL, help="Installer HTTP server URL"
    )
    args = parser.parse_args()

    BOOT_DIR = args.boot_dir
    TARGETS_FILE = args.targets_file
    INSTALLER_URL = args.installer_url

    # Validate boot directory
    if not os.path.isdir(BOOT_DIR):
        print(f"[pxe-api] WARNING: Boot directory {BOOT_DIR} does not exist")
        print(f"[pxe-api] Run 'installer-setup' to download Ubuntu boot files")

    # Initialize empty targets file if needed
    targets_path = Path(TARGETS_FILE)
    if not targets_path.exists():
        targets_path.parent.mkdir(parents=True, exist_ok=True)
        targets_path.write_text("{}\n")
        print(f"[pxe-api] Created empty targets file: {TARGETS_FILE}")

    server = HTTPServer(("0.0.0.0", args.port), PXEAPIHandler)
    print(f"[pxe-api] NixFleet PXE API server listening on :{args.port}")
    print(f"[pxe-api]   Boot dir:     {BOOT_DIR}")
    print(f"[pxe-api]   Targets file: {TARGETS_FILE}")
    print(f"[pxe-api]   Installer:    {INSTALLER_URL}")
    print(f"[pxe-api]")
    print(f"[pxe-api] Pixiecore should be started with:")
    print(f"[pxe-api]   pixiecore api http://localhost:{args.port} --dhcp-no-bind")
    print()

    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\n[pxe-api] Shutting down")
        server.shutdown()


if __name__ == "__main__":
    main()
