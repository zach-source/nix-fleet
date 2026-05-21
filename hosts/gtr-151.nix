# GTR-151 — AMD Ryzen AI MAX+ 395 (192.168.3.132)
# The "Qwen3.6" node — both family variants co-hosted:
#   :8084  Qwen3.6-35B-A3B MoE  + speculation (custom fork)
#   :8085  Qwen3.6-27B    dense + speculation (custom fork)
# 131GB unified VRAM, ROCm 7.13 (TheRock SDK), gfx1151
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
      # Uses custom fork at /opt/llama-rocm-qwen35 (PR #20075 + our fixes)
      # Fork repo: https://github.com/zach-source/llama.cpp/tree/qwen35-speculation
      # Qwen3.6 uses qwen35moe architecture — same hybrid backbone as Qwen3.5, so fork applies.
      services.qwen36-spec = {
        description = "Qwen3.6-35B-A3B + speculation (custom fork)";
        model = "/srv/models/Qwen3.6-35B-A3B-UD-Q4_K_M.gguf";
        binary = "/opt/llama-rocm-qwen35/bin/llama-server";
        ldLibraryPath = "/opt/llama-rocm-qwen35/lib:/opt/rocm-sdk/lib:/opt/rocm-sdk/lib/rocm_sysdeps/lib:/opt/rocm-sdk/lib/llvm/lib:/opt/rocm-sdk/lib/host-math/lib";
        port = 8084;
        ctxSize = 200000;
        batchSize = 512;
        ubatchSize = 512;
        ctxCheckpoints = 0; # incompatible with hybrid memory
        cacheReuse = null; # IMROPE doesn't support seq_add
        draft = {
          # Qwen3.5-0.8B outperforms 2B draft on varied prompts:
          # 0.8B: 38.8 tok/s mean, 61% accept
          # 2B:   35.3 tok/s mean, 65% accept (slower draft pass negates higher accept)
          model = "/srv/models/Qwen3.5-0.8B-Q4_K_M.gguf";
          max = 4;
          min = 1;
          pMin = 0.6;
        };
        reasoning = {
          format = "deepseek";
          budget = 2048;
        };
        # --cache-ram 0 disables prompt cache restore — workaround for
        # upstream bug in llama-memory-recurrent.cpp:1086 where state_read_meta
        # asserts cells[head].pos == ubatch.pos[0] but recurrent find_slot sets
        # cell.pos = ubatch.pos[cell_count-1]. Crashes server with concurrent
        # requests that hit the prompt cache. See git log of fork for fix.
        extraFlags = [
          "--cache-ram"
          "0"
        ];
      };

      # Qwen3.6-27B DENSE — the quality/coding counterpart to the 35B-A3B
      # MoE above. Co-hosted so gtr-151 is the single "Qwen3.6" node: MoE
      # on :8084, dense on :8085. Qwen claims the 27B dense beats the old
      # 397B-A17B flagship on coding (SWE-bench Verified 77.2 vs 76.2).
      #
      # Runs on the CUSTOM FORK (/opt/llama-rocm-qwen35), same as the MoE.
      # The dense is qwen35 arch (hybrid DeltaNet+attention), so stock
      # llama.cpp refuses speculation ("context does not support partial
      # sequence removal"). The fork's qwen35 speculation fixes apply to the
      # dense too, giving draft-model speedup on this otherwise bandwidth-
      # bound model (~8 tok/s raw: reads all 25.6GB/token over ~256GB/s).
      # Mirrors the MoE's hybrid-memory workarounds.
      services.qwen36-27b = {
        description = "Qwen3.6-27B dense (quality/coding) + speculation (fork)";
        model = "/srv/models/Qwen3.6-27B-UD-Q6_K_XL.gguf";
        binary = "/opt/llama-rocm-qwen35/bin/llama-server";
        ldLibraryPath = "/opt/llama-rocm-qwen35/lib:/opt/rocm-sdk/lib:/opt/rocm-sdk/lib/rocm_sysdeps/lib:/opt/rocm-sdk/lib/llvm/lib:/opt/rocm-sdk/lib/host-math/lib";
        port = 8085;
        ctxSize = 131072; # 131K — leaves headroom alongside the MoE's 200K
        ctxCheckpoints = 0; # incompatible with hybrid memory (qwen35)
        cacheReuse = null; # IMROPE doesn't support seq_add
        draft = {
          # Qwen3.5-0.8B — vocab-compatible (Qwen3.6 shares the vocab), same
          # draft the MoE uses. High acceptance on structured/coding output.
          model = "/srv/models/Qwen3.5-0.8B-Q4_K_M.gguf";
          max = 4;
          min = 1;
          pMin = 0.6;
        };
        reasoning = {
          format = "deepseek";
          budget = 2048;
        };
        # --cache-ram 0 dodges the recurrent prompt-cache restore bug (see
        # MoE note). Plus Qwen3.6 coding-recommended sampling.
        extraFlags = [
          "--cache-ram"
          "0"
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
