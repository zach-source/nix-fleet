#!/usr/bin/env python3
"""
NixFleet Pixiecore API Backend

Serves boot configurations to pixiecore based on MAC address.
Supports two modes per target:
  - install:  Ubuntu autoinstall with NixFleet preinstalled
  - recovery: Ubuntu live boot with ZFS/LUKS recovery tools

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
            "{{.Initrd}} "
            f"autoinstall "
            f"ds=nocloud-net\\;s={INSTALLER_URL}/{host}/ "
            "ip=dhcp "
            "---"
        ),
    }


def build_recovery_config():
    """Build boot config for recovery mode (live Ubuntu, no autoinstall)."""
    return {
        "kernel": f"file://{BOOT_DIR}/vmlinuz",
        "initrd": [f"file://{BOOT_DIR}/initrd"],
        "cmdline": ("{{.Initrd}} " "ip=dhcp " "---"),
    }


class PXEAPIHandler(BaseHTTPRequestHandler):
    """Handle pixiecore API requests."""

    def log_message(self, format, *args):
        """Override to add prefix."""
        print(f"[pxe-api] {args[0]}")

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

        if mode == "install":
            host = target.get("host", "unknown")
            config = build_install_config(host)
            self.log_message(f"INSTALL {mac} -> host={host}")
        elif mode == "recovery":
            config = build_recovery_config()
            self.log_message(f"RECOVERY {mac}")
        else:
            self.send_response(400)
            self.end_headers()
            self.log_message(f"ERROR {mac}: unknown mode '{mode}'")
            return

        # Verify boot files exist
        kernel_path = f"{BOOT_DIR}/vmlinuz"
        initrd_path = f"{BOOT_DIR}/initrd"
        if not os.path.isfile(kernel_path) or not os.path.isfile(initrd_path):
            self.send_response(500)
            self.end_headers()
            self.log_message(f"ERROR: Boot files missing. Run 'installer-setup' first.")
            return

        response = json.dumps(config)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(response.encode())


def main():
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

    global BOOT_DIR, TARGETS_FILE, INSTALLER_URL
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
