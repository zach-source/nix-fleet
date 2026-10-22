# HS-1 — HPE DL380 Gen9 (192.168.3.144)
# Hyperscale k0s worker for KubeVirt + LINSTOR/DRBD (Piraeus) storage.
# 2x Xeon E5-2676 v3 (24c/48t), 256 GB RAM, no GPU, no TPM
# Boot NIC HP 546FLR-SFP+ 10G (MAC E0:07:1B:67:50:B0 port 1, :B1 port 2),
# HBAs H240ar + H240 in HBA mode
{ ... }:

{
  imports = [
    ../modules/base-packages.nix
    ../modules/iscsi.nix
    ../modules/multipath.nix
    ../modules/k0s.nix
    ../modules/kubevirt.nix
    ../modules/sysctl.nix
    ../modules/hyperscale.nix
  ];

  nixfleet = {
    host = {
      name = "hs-1";
      base = "ubuntu";
      addr = "192.168.3.144";
    };

    # k0s worker. system-reserved=16Gi: no host-side model services here, so
    # the host only needs room for the OS, ZFS ARC and the DRBD/LINSTOR
    # satellite; the rest is VM and pod memory.
    k0s.worker = {
      enable = true;
      systemReservedMemory = "16Gi";
    };

    # iSCSI initiator + multipath blacklist so Synology CSI PVCs still attach,
    # same as the gtr nodes.
    modules.iscsi.enable = true;
    modules.multipath.enable = true;

    # KubeVirt kubelet pods bind — see modules/kubevirt.nix.
    modules.kubevirt.enable = true;

    # inotify headroom for k0s/virt-handler — same values as gtr-150.
    modules.sysctl = {
      enable = true;
      settings = {
        "fs.inotify.max_user_watches" = 524288;
        "fs.inotify.max_user_instances" = 8192;
      };
    };

    # LINSTOR thin-LVM prerequisites. dataDisks stays empty until the disk
    # layout is known: after first boot, fill it with the /dev/disk/by-id paths
    # of the empty data disks (the VG service refuses disks that carry data).
    modules.hyperscale = {
      enable = true;
      dataDisks = [ ];
    };
  };
}
