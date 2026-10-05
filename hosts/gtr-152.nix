# GTR-152 — AMD Ryzen AI MAX+ 395 (192.168.3.134)
# The CODING tier (2026-10-05): one model, Ornith-1.5-35B-A3B with MTP
# self-speculation and thinking on. One model per box so the rest of the
# unified memory stays free for builds and hosting.
# Build: /opt/llama-rocm-latest, ROCm 7.13 (TheRock), gfx1151.
{ pkgs, ... }:

{
  imports = [
    ../modules/base-packages.nix
    ../modules/llm-inference.nix
    ../modules/iscsi.nix
    ../modules/multipath.nix
    ../modules/k0s.nix
    ../modules/kubevirt.nix
    ../modules/sysctl.nix
  ];

  nixfleet = {
    host = {
      name = "gtr-152";
      base = "ubuntu";
      addr = "192.168.3.134";
    };

    # k0s worker, declaratively managed. system-reserved=56Gi -> ~66Gi k8s
    # allocatable for builds and hosting. Was 78Gi (~44Gi allocatable) while
    # this box ran several models; one model per box since 2026-10-05 needs
    # ~30-45Gi, and 56Gi keeps ~10-15Gi of host headroom on top of it.
    k0s.worker = {
      enable = true;
      systemReservedMemory = "56Gi";
    };

    # iSCSI initiator so the Synology CSI driver can attach btrfs-backed LUNs.
    modules.iscsi.enable = true;

    # Blacklist Synology LUNs from dm-multipath auto-claim — see
    # modules/multipath.nix (2026-07-25 gastown-town readonly incident).
    modules.multipath.enable = true;

    # KubeVirt: bind k0s's real kubelet pods dir onto the hardcoded
    # /var/lib/kubelet/pods, or virt-launcher's container-disk init container
    # can't find its binary and every VM crash-loops. See modules/kubevirt.nix.
    modules.kubevirt.enable = true;

    # inotify headroom for k0s. Ubuntu defaults (max_user_instances=128) are
    # exhausted by kubelet + containerd + CSI, and virt-handler then panics at
    # startup with "Failed to create an inotify watcher: too many open files".
    # Same values gti already runs.
    modules.sysctl = {
      enable = true;
      settings = {
        "fs.inotify.max_user_watches" = 524288;
        "fs.inotify.max_user_instances" = 8192;
      };
    };

    modules.llmInference = {
      enable = true;
      # Ornith-1.5-35B-A3B (2026-10-05), replacing Ornith-1.0 at the same port.
      # Same Qwen3.6-35B-A3B architecture, so the same build loads it. The
      # official Q6_K GGUF carries its own MTP head (nextn_predict_layers = 1,
      # checked in the file header), so the MTP self-spec settings carry over
      # from 1.0, which ran nMax=3 stably here at 54-58 tok/s. Card claims
      # TB2.1 (terminus-2) 67.8 vs 1.0's 64.2. The 1.0 GGUF stays on disk for
      # a one-line revert.
      services.ornith = {
        description = "Ornith-1.5-35B-A3B coding agent + MTP self-speculation";
        model = "/srv/models/Ornith-1.5-35B-A3B-Q6_K.gguf";
        binary = "/opt/llama-rocm-latest/llama-server";
        ldLibraryPath = "/opt/llama-rocm-latest:/opt/rocm-sdk/lib:/opt/rocm-sdk/lib/rocm_sysdeps/lib:/opt/rocm-sdk/lib/llvm/lib:/opt/rocm-sdk/lib/host-math/lib";
        port = 8086;
        # 512K context on a natively-262K model: static YaRN, factor 2 — the
        # factor Qwen's cards give for 524288. Same window on every gtr box
        # (2026-10-05). Static YaRN applies at every length, so very short
        # prompts may lose a little quality; drop the rope/yarn/override-kv
        # flags and set ctxSize = 262144 to undo.
        #
        # ctxSize is the TOTAL KV budget split across --parallel slots, so two
        # slots of 524288 each. With one slot, a short request waited ~2 min
        # behind an agent re-prefilling a 111k-token conversation (measured
        # 2026-10-05). The MoE's KV is cheap (10 of 40 layers are full
        # attention, q4_0), so the second slot costs ~6 GB.
        ctxSize = 1048576;
        parallel = 2;
        newCli = true;
        mtp = {
          nMax = 3;
        };
        reasoning = {
          format = "deepseek";
          budget = 2048;
        };
        # Ornith/Qwen coding-recommended sampling (clients may override).
        extraFlags = [
          "--temp"
          "0.6"
          "--top-p"
          "0.95"
          "--top-k"
          "20"
          "--rope-scaling"
          "yarn"
          "--rope-scale"
          "2"
          "--yarn-orig-ctx"
          "262144"
          # llama-server caps every slot at the GGUF's declared training
          # context ("exceeds the training context of the model - capping"),
          # whatever the rope flags say, so declare the extended window too.
          "--override-kv"
          "qwen35moe.context_length=int:524288"
        ];
      };
    };

    # The two Vulkan-backend models that used to live here (Gemma-4-31B :8080,
    # Qwen3.5-27B-Opus-Distilled :8081) were EVICTED 2026-07-23 to free GPU for
    # Ornith #2 above — neither was referenced by the LiteLLM proxy or any agent.
  };
}
