# GTR-152 — AMD Ryzen AI MAX+ 395 (192.168.3.134)
# Throughput king: Qwen3-Coder-30B-A3B with speculative decoding
# 131GB unified VRAM, ROCm (stock lemonade build), gfx1151
{ pkgs, ... }:

{
  imports = [
    ../modules/llm-inference.nix
    ../modules/iscsi.nix
    ../modules/k0s.nix
  ];

  nixfleet = {
    host = {
      name = "gtr-152";
      base = "ubuntu";
      addr = "192.168.3.134";
    };

    # k0s worker, declaratively managed. system-reserved=78Gi -> ~44Gi k8s
    # allocatable (was an out-of-band 98Gi/24Gi); 78Gi stays for inference.
    k0s.worker.enable = true;

    # iSCSI initiator so the Synology CSI driver can attach btrfs-backed LUNs.
    modules.iscsi.enable = true;

    packages = with pkgs; [
      git
      htop
      curl
      jq
      tmux
      vim
    ];

    modules.llmInference = {
      enable = true;
      # Ornith #2 of 3 (load-balanced pool gtr-151/152/153). This is the MTP
      # TRIAL instance: it uses the 1M-YaRN GGUF's grafted MTP head for self-
      # speculation (--spec-type draft-mtp) instead of a classic draft model.
      # MTP historically CRASHED the recurrent 35B-A3B MoE on gfx1151 warmup
      # ("ROCm unspecified launch failure"), which is why gtr-151/153 stay on
      # classic draft — so watch this one's startup logs on first deploy. If it
      # wedges, remove the `mtp` block and add the classic draft (Qwen3.5-0.8B)
      # used on gtr-151. Replaced Qwen3-Coder-30B (weaker coder) to free the GPU.
      # Needs the latest-upstream build + pinned rocm-sdk (staged onto this box
      # alongside gtr-151/153); the stock lemonade build can't run qwen35moe.
      services.ornith = {
        description = "Ornith-1.0-35B-MoE coding agent + MTP self-speculation (trial)";
        model = "/srv/models/ornith-1.0-35b-1M-MTP-Q6_K.gguf";
        binary = "/opt/llama-rocm-latest/llama-server";
        ldLibraryPath = "/opt/llama-rocm-latest:/opt/rocm-sdk/lib:/opt/rocm-sdk/lib/rocm_sysdeps/lib:/opt/rocm-sdk/lib/llvm/lib:/opt/rocm-sdk/lib/host-math/lib";
        port = 8086;
        # 384K — Ornith is the only big model here now (Qwen3-Coder evicted), so
        # ~54G leaves plenty of the 122G node free.
        ctxSize = 393216;
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
        ];
      };
    };

    # The two Vulkan-backend models that used to live here (Gemma-4-31B :8080,
    # Qwen3.5-27B-Opus-Distilled :8081) were EVICTED 2026-07-23 to free GPU for
    # Ornith #2 above — neither was referenced by the LiteLLM proxy or any agent.
    # The abliterated Qwen3.6-35B (:8083, an imperative unit, the fleet's
    # `qwen36-35b-abliterated` tier) STAYS; Ornith co-tenants with it (~83G/122G).
  };
}
