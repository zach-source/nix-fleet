# Example DGX Spark — second node of a stacked pair.
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
    ../modules/vllm.nix
    (import ./dgx-spark-dsv4.nix { nodeRank = 1; })
  ];

  nixfleet = {
    host = {
      name = "dgx-spark-2";
      base = "dgx";
      addr = "10.0.3.11";
    };

    # nodeIndex 11 = the playbook's second-node convention, giving
    # 192.168.100.11 and 192.168.101.11 on the two CX7 subnets.
    modules.dgxSparkCluster = {
      enable = true;
      nodeIndex = 11;
    };

    healthChecks = {
      gpu-present = {
        type = "command";
        command = "nvidia-smi -L | grep -q GPU";
      };

      # Rank 1 serves no HTTP — modules/vllm.nix health-checks the unit here
      # instead of a port. What is worth asserting from this side is that the
      # fabric path to rank 0 is actually up, since a silent NCCL hang is the
      # usual symptom of it not being.
      dsv4-master-reachable = {
        type = "command";
        command = "ping -c1 -W2 192.168.100.10";
      };
    };
  };
}
