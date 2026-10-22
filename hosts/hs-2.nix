# HS-2 — HPE DL380 Gen9 (192.168.3.145)
# Hyperscale k0s worker for KubeVirt + LINSTOR/DRBD (Piraeus) storage.
# 2x Xeon E5-2650 v3 (20c/40t), 294 GiB RAM visible, no GPU, no TPM
# NIC HP 533FLR-T / BCM57810 10G (eno49 = EC:B1:D7:AF:3B:40, port 2 :44)
# HBA LSI SAS2308 (IT mode)
# OS on LUKS+LVM RAID1 on sdc+sdd (2x 300G)
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
      name = "hs-2";
      base = "ubuntu";
      addr = "192.168.3.145";
    };

    # k0s worker. system-reserved=48Gi: 16Gi for the host (OS, DRBD/LINSTOR
    # satellite) plus the 32Gi ZFS ARC cap. The rest is VM and pod memory.
    k0s.worker = {
      enable = true;
      systemReservedMemory = "48Gi";
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

    # ZFS pools for LINSTOR (ZFS_THIN pool on zpool linstor) and k0s scratch.
    modules.hyperscale = {
      enable = true;
      # 6 mirrors of 600G 10k SAS; each pair mixes an EG0600 with an ST600.
      linstorMirrors = [
        [
          "/dev/disk/by-id/wwn-0x5000c50083a914d7"
          "/dev/disk/by-id/wwn-0x5000c500769d39b7"
        ]
        [
          "/dev/disk/by-id/wwn-0x5000c50083a94067"
          "/dev/disk/by-id/wwn-0x5000c500769d68eb"
        ]
        [
          "/dev/disk/by-id/wwn-0x5000c50083a91957"
          "/dev/disk/by-id/wwn-0x5000c50076997e37"
        ]
        [
          "/dev/disk/by-id/wwn-0x5000c50083a9362b"
          "/dev/disk/by-id/wwn-0x5000c500769daf9b"
        ]
        [
          "/dev/disk/by-id/wwn-0x5000c500769d61af"
          "/dev/disk/by-id/wwn-0x5000c5007698f913"
        ]
        [
          "/dev/disk/by-id/wwn-0x5000c5007698f69b"
          "/dev/disk/by-id/wwn-0x5000c500769908df"
        ]
      ];
      # 2x 1.2T 10k SAS (HUC101812).
      scratchMirror = [
        "/dev/disk/by-id/wwn-0x5000cca0b01c3020"
        "/dev/disk/by-id/wwn-0x5000cca0b01c2540"
      ];
    };
  };
}
