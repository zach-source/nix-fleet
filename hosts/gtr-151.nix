# GTR-151 — AMD Ryzen AI MAX+ 395 (192.168.3.132)
# The "Qwen3.6" node — both family variants co-hosted, MTP self-speculation:
#   :8084  Qwen3.6-35B-A3B MoE  + MTP (~61 tok/s, pure 3.6)
#   :8085  Qwen3.6-27B    dense + MTP (~16 tok/s, pure 3.6)
# Build: /opt/llama-rocm-latest (commit 6a257d4) — fork retired, PR #19493
# natively handles qwen35 spec. 131GB unified VRAM, ROCm 7.13 (TheRock), gfx1151
{ pkgs, ... }:

{
  imports = [ ../modules/llm-inference.nix ];

  nixfleet = {
    host = {
      name = "gtr-151";
      base = "ubuntu";
      addr = "192.168.3.132";
    };

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
      # Runs on LATEST UPSTREAM llama.cpp built for gfx1151 at
      # /opt/llama-rocm-latest (commit 6a257d4). Replaces the old custom fork:
      # upstream PR #19493 natively handles qwen35 hybrid speculation, so the
      # 6 fork patches are obsolete (verified empirically — native spec gives
      # 100% draft acceptance). The build also adds --spec-type draft-mtp.
      #
      # Both models use MTP self-speculation (pure Qwen3.6, no Qwen3.5 draft).
      # Benchmarked head-to-head on this gfx1151 box (same coding prompts):
      #   MoE   — MTP ~61 tok/s vs classic 3.5 draft ~48  → MTP wins
      #   dense — MTP ~16 tok/s vs classic 3.5 draft ~22  → draft slightly
      #           faster, but MTP keeps it draft-free; dense is the quality
      #           tier so the gap is an acceptable trade for pure-3.6.
      # NOTE: --spec-draft-n-max must stay 2 for these MTP heads; n_max=4 hits
      # a ROCm launch failure that wedges the GPU (requires reboot).
      services.qwen36-spec = {
        description = "Qwen3.6-35B-A3B MoE + MTP self-speculation";
        # MTP GGUF (UD-Q6_K_XL, 30.4GB) — built-in multi-token-prediction head.
        model = "/srv/models/Qwen3.6-35B-A3B-MTP-UD-Q6_K_XL.gguf";
        binary = "/opt/llama-rocm-latest/llama-server";
        ldLibraryPath = "/opt/llama-rocm-latest:/opt/rocm-sdk/lib:/opt/rocm-sdk/lib/rocm_sysdeps/lib:/opt/rocm-sdk/lib/llvm/lib:/opt/rocm-sdk/lib/host-math/lib";
        port = 8084;
        ctxSize = 200000;
        batchSize = 512;
        ubatchSize = 512;
        # No fork workarounds: upstream handles qwen35 recurrent memory + the
        # prompt-cache restore bug. Defaults apply (ctx-checkpoints=32,
        # cache-reuse=256).
        mtp = {
          nMax = 2;
        };
        reasoning = {
          format = "deepseek";
          budget = 2048;
        };
      };

      # Qwen3.6-27B DENSE — the quality/coding counterpart to the 35B-A3B
      # MoE above. Co-hosted so gtr-151 is the single "Qwen3.6" node: MoE
      # on :8084, dense on :8085. Qwen claims the 27B dense beats the old
      # 397B-A17B flagship on coding (SWE-bench Verified 77.2 vs 76.2).
      # Same latest-upstream build; uses MTP self-speculation (pure 3.6, no
      # draft model) — the dense is the quality tier so its lower MTP
      # throughput is an acceptable trade for a draft-free 3.6-only setup.
      services.qwen36-27b = {
        description = "Qwen3.6-27B dense (quality/coding) + MTP self-speculation";
        # MTP GGUF (UD-Q6_K_XL, 24.2GB).
        model = "/srv/models/Qwen3.6-27B-MTP-UD-Q6_K_XL.gguf";
        binary = "/opt/llama-rocm-latest/llama-server";
        ldLibraryPath = "/opt/llama-rocm-latest:/opt/rocm-sdk/lib:/opt/rocm-sdk/lib/rocm_sysdeps/lib:/opt/rocm-sdk/lib/llvm/lib:/opt/rocm-sdk/lib/host-math/lib";
        port = 8085;
        ctxSize = 131072; # 131K — leaves headroom alongside the MoE's 200K
        mtp = {
          nMax = 2;
        };
        reasoning = {
          format = "deepseek";
          budget = 2048;
        };
        # Qwen3.6 coding-recommended sampling (clients may override per-request).
        extraFlags = [
          "--temp"
          "0.6"
          "--top-p"
          "0.95"
          "--top-k"
          "20"
          "--min-p"
          "0.0"
        ];
      };
    };
  };
}
