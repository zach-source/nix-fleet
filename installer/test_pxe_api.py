#!/usr/bin/env python3
"""Self-check for pxe-api boot config builders.

Run: python3 installer/test_pxe_api.py

Covers the DGX Spark modes, whose kernel arguments come straight from NVIDIA's
PXE guide and are easy to typo into something that boots but misbehaves.
"""

import importlib.util
import sys
from pathlib import Path

spec = importlib.util.spec_from_file_location(
    "pxe_api", Path(__file__).parent / "pxe-api.py"
)
pxe_api = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pxe_api)


def test_dgx_spark_cmdline():
    cfg = pxe_api.build_dgx_spark_config({"iso_url": "http://srv/base_os_7.0.0.iso"})
    cmd = cfg["cmdline"]
    # The two DGX-specific arguments that a generic Ubuntu cmdline would omit.
    assert "nvme-core.multipath=n" in cmd, cmd
    assert "nouveau.modeset=0" in cmd, cmd
    assert "autoinstall" in cmd and "ip=dhcp" in cmd, cmd
    assert "url=http://srv/base_os_7.0.0.iso" in cmd, cmd
    # Must not inherit the Ubuntu path's ramdisk/nocloud args.
    assert "ds=nocloud-net" not in cmd, cmd
    # Boots from its own subdir, not the shared Ubuntu one.
    assert cfg["kernel"].endswith("/dgx-spark/vmlinuz"), cfg["kernel"]


def test_dgx_spark_requires_iso_url():
    for bad in ({}, {"iso_url": ""}):
        try:
            pxe_api.build_dgx_spark_config(bad)
        except ValueError:
            continue
        raise AssertionError(f"expected ValueError for {bad!r}")


def test_fastos_cmdline():
    cfg = pxe_api.build_dgx_spark_fastos_config(
        {"fastos_url": "http://srv/usb.customer.tar.gz"}
    )
    cmd = cfg["cmdline"]
    assert "pxeinstall=true" in cmd, cmd
    assert "fastos_usbimg_url=http://srv/usb.customer.tar.gz" in cmd, cmd
    # 921600 is genuinely the documented baud, and the ARM watchdog arg is
    # required — both look like typos to a reviewer, so pin them.
    assert "console=ttyS0,921600" in cmd, cmd
    assert "sbsa_gwdt.action=1" in cmd, cmd
    assert "noui" in cmd, cmd
    # static_ip is opt-in; absent unless asked for.
    assert "static_ip=" not in cmd, cmd


def test_fastos_optional_flags():
    cfg = pxe_api.build_dgx_spark_fastos_config(
        {
            "fastos_url": "http://srv/x.tar.gz",
            "static_ip": "10.0.3.10:10.0.3.1",
            "usb_skipfw": True,
        }
    )
    cmd = cfg["cmdline"]
    assert "static_ip=10.0.3.10:10.0.3.1" in cmd, cmd
    assert "usb.skipfw" in cmd, cmd
    # usb.shell not requested, so it must not appear.
    assert "usb.shell" not in cmd, cmd


def test_fastos_requires_url():
    try:
        pxe_api.build_dgx_spark_fastos_config({})
    except ValueError:
        return
    raise AssertionError("expected ValueError when fastos_url missing")


def test_existing_modes_unchanged():
    """The Ubuntu paths must keep booting from the shared BOOT_DIR."""
    inst = pxe_api.build_install_config("gtr-150")
    assert inst["kernel"].endswith("/vmlinuz"), inst["kernel"]
    assert "/dgx-spark/" not in inst["kernel"], inst["kernel"]
    assert "ds=nocloud-net" in inst["cmdline"]
    rec = pxe_api.build_recovery_config()
    assert "ip=dhcp" in rec["cmdline"]


if __name__ == "__main__":
    tests = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for t in tests:
        t()
        print(f"  ok  {t.__name__}")
    print(f"\n{len(tests)} passed")
    sys.exit(0)
