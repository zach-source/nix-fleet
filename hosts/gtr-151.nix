# GTR-151 — AMD Ryzen AI MAX+ 395 (192.168.3.132)
# Quality king: Qwen3.5-35B-A3B with speculative decoding (custom fork)
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
      services.qwen35-spec = {
        description = "Qwen3.5-35B-A3B + speculation (custom fork)";
        model = "/srv/models/Qwen3.5-35B-A3B-Q4_K_M.gguf";
        binary = "/opt/llama-rocm-qwen35/bin/llama-server";
        ldLibraryPath = "/opt/llama-rocm-qwen35/lib:/opt/rocm-sdk/lib:/opt/rocm-sdk/lib/rocm_sysdeps/lib:/opt/rocm-sdk/lib/llvm/lib:/opt/rocm-sdk/lib/host-math/lib";
        port = 8084;
        ctxSize = 200000;
        batchSize = 512;
        ubatchSize = 512;
        ctxCheckpoints = 0; # incompatible with hybrid memory
        cacheReuse = null; # IMROPE doesn't support seq_add
        draft = {
          model = "/srv/models/Qwen3.5-0.8B-Q4_K_M.gguf";
          max = 4;
          min = 1;
          pMin = 0.6;
        };
        reasoning = {
          format = "deepseek";
          budget = 2048;
        };
        extraFlags = [ ];
      };
    };
  };
}
