# DGX Spark 2 — rank 1 of the stacked pair.
#
# Differs from dgx-spark-1 in exactly three places: hostname, management
# address, and its position on the fabric (nodeIndex 11, vLLM node-rank 1).
# Everything about the model server itself comes from hosts/dgx-spark-dsv4.nix
# so the two ranks cannot drift apart.
#
# See hosts/dgx-spark-1.nix for why DGX hosts are managed but never OS-updated.
{ pkgs, ... }:

{
  imports = [
    ../modules/base-packages.nix
    ../modules/dgx-spark-cluster.nix
    ../modules/netplan.nix
    ../modules/storage-vlan.nix
    ../modules/dspark-dsv4.nix
    (import ./dgx-spark-dsv4.nix { nodeRank = 1; })
  ];

  nixfleet = {
    host = {
      name = "dgx-spark-2";
      base = "dgx";
      addr = "192.168.3.141";
    };

    # nodeIndex 11 = the playbook's second-node convention, giving
    # 192.168.100.11 and 192.168.101.11 on the two CX7 subnets.
    #
    # Left-port override matching dgx-spark-1 — the playbook's one hard rule is
    # that every node uses the SAME physical port. Verified on spark-7ee2
    # 2026-08-20: enp1s0f0np0 + enP2p1s0f0np0 (Up), right pair (Down).
    modules.dgxSparkCluster = {
      enable = true;
      nodeIndex = 11;
      interfaces = {
        enp1s0f0np0 = "192.168.100";
        enP2p1s0f0np0 = "192.168.101";
      };
    };

    # VLAN 8 storage network. Parent confirmed identical to spark-5267 —
    # enP7s7, maxmtu 9194, so 9000 fits.
    # OFF, and not out of caution — out of fact. The management address lives
    # on enP7s7, which is exactly the interface modules/storage-vlan.nix
    # declares as the VLAN 8 parent. With renderer: NetworkManager, an apply
    # tears down and rebuilds that connection, i.e. the one carrying SSH. There
    # is no BMC, no AMT and no reachable KVM on this fleet, and spark-7ee2 has
    # already needed a deadman rollback once for exactly this. The unit still
    # ships (disabled) so an operator physically present can run it by hand.
    modules.netplan.autoApply = false;

    modules.storageVlan = {
      enable = true;
      interface = "enP7s7";
      address = "192.168.8.141/24";
      # DGX OS netplan is an empty `renderer: NetworkManager` stub — nothing
      # else declares this interface, so the module must carry its addressing
      # or NetworkManager replaces a working profile with an address-less one.
      parentDhcp = true;
      peer = "192.168.8.133";
    };

    healthChecks = {
      gpu-present = {
        type = "command";
        command = "nvidia-smi -L | grep -q GPU";
      };

      # Rank 1 serves no HTTP and has no service unit — the head starts this
      # node's container over SSH. What is worth asserting from this side is
      # that the fabric path to rank 0 is actually up, since a silent NCCL hang
      # is the usual symptom of it not being.
      dsv4-master-reachable = {
        type = "command";
        command = "ping -c1 -W2 192.168.100.10";
      };
    };
  };
}
