# GTR-152 — AMD Ryzen AI MAX+ 395 (192.168.3.134)
# Throughput king: Qwen3-Coder-30B-A3B with speculative decoding
# 131GB unified VRAM, ROCm (stock lemonade build), gfx1151
{ pkgs, ... }:

{
  imports = [ ../modules/llm-inference.nix ];

  nixfleet = {
    host = {
      name = "gtr-152";
      base = "ubuntu";
      addr = "192.168.3.134";
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
      services.qwen3-coder = {
        description = "Qwen3-Coder-30B-A3B + speculative draft";
        model = "/srv/models/Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf";
        port = 8082;
        ctxSize = 262144;
        mlock = true;
        draft = {
          model = "/srv/models/Qwen3-0.6B-Q4_K_M.gguf";
          max = 8;
          min = 2;
          pMin = 0.6;
        };
        reasoning = {
          format = "deepseek";
          budget = 2048;
        };
      };
    };
  };
}
