# GTR-152 — AMD Ryzen AI MAX+ 395 (192.168.3.134)
# Throughput king: Qwen3-Coder-30B-A3B with speculative decoding
# 131GB unified VRAM, ROCm (stock lemonade build), gfx1151
{ pkgs, ... }:

{
  imports = [
    ../modules/llm-inference.nix
    ../modules/iscsi.nix
    ../modules/multipath.nix
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

    # Blacklist Synology LUNs from dm-multipath auto-claim — see
    # modules/multipath.nix (2026-07-25 gastown-town readonly incident).
    modules.multipath.enable = true;

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
        # 524288 total KV / --parallel 2 = 256K per concurrent request (2 slots).
        # Ornith is the only ROCm model here (Qwen3-Coder evicted; abliterated
        # :8083 co-tenants), so the ~1.33x KV bump fits comfortably.
        ctxSize = 524288;
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
        ];
      };

      # 2026-07-26: replaces the old qwen3.6-35b-abliterated (huihui) imperative
      # unit at the same port. New uncensored fine-tune: HauhauCS-Aggressive
      # (wang-yang's MTP-grafted Q6_K_P — the upstream HauhauCS repo only ships
      # classic quants; wang-yang transplanted a real nextn/MTP head from a
      # stock Qwen3.6-35B-A3B GGUF, byte-exact, no requantization). Upstream's
      # own Apple M3 Metal benchmark found n_max=1-2 the best operating point
      # (~+27% over no-spec); n_max=2 chosen to match gtr-153's sibling deploy
      # and gtr-153's qwen36-27b precedent. UNVERIFIED on our gfx1151/ROCm
      # hardware — watch startup logs (qwen35moe + MTP has crashed here before,
      # see :8084 on gtr-151) and benchmark once live.
      services.hauhaucs-uncensored = {
        description = "Qwen3.6-35B-A3B Uncensored (HauhauCS-Aggressive) + MTP";
        model = "/srv/models/Qwen3.6-35B-A3B-Uncensored-HauhauCS-Aggressive-Q6_K_P-MTP.gguf";
        binary = "/opt/llama-rocm-latest/llama-server";
        ldLibraryPath = "/opt/llama-rocm-latest:/opt/rocm-sdk/lib:/opt/rocm-sdk/lib/rocm_sysdeps/lib:/opt/rocm-sdk/lib/llvm/lib:/opt/rocm-sdk/lib/host-math/lib";
        port = 8083;
        ctxSize = 131072;
        newCli = true;
        mtp = {
          nMax = 2;
        };
        reasoning = {
          format = "deepseek";
          budget = 2048;
        };
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
  };
}
