# GTR-150 — AMD Ryzen AI MAX+ 395 (192.168.3.133)
# Multi-role node: Primary LLM + embeddings + safety + code completion + orchestrator + whisper
# 131GB unified VRAM, ROCm (stock lemonade build), gfx1151
{ pkgs, ... }:

{
  imports = [ ../modules/llm-inference.nix ];

  nixfleet = {
    host = {
      name = "gtr-150";
      base = "ubuntu";
      addr = "192.168.3.133";
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

      # Primary LLM
      services.gemma4 = {
        description = "Gemma 4 26B-A4B Q4_K_M";
        model = "/srv/models/mythos/gemma-4-26B-A4B-it-Q4_K_M.gguf";
        port = 8080;
        ctxSize = 262144;
        noMmap = true;
      };

      # Embeddings (768-dim, for RAG pipelines)
      services.embeddings = {
        description = "Nomic Embed Text v2 MoE - Embeddings";
        model = "/srv/models/support/nomic-embed-text-v2-moe-Q8_0.gguf";
        port = 8090;
        ctxSize = 8192;
        parallel = 4;
        embedding = true;
        rocmEnv = { }; # no ROCm env needed for small model
      };

      # Safety filter
      services.safety = {
        description = "ShieldGemma 2B - Safety Filter";
        model = "/srv/models/support/shieldgemma-2b-Q8_0.gguf";
        port = 8091;
        ctxSize = 8192;
        parallel = 2;
        rocmEnv = { };
      };

      # Code completion (FIM)
      services.codecomplete = {
        description = "Qwen2.5-Coder 1.5B - FIM Code Completion";
        model = "/srv/models/support/qwen2.5-coder-1.5b-instruct-q8_0.gguf";
        port = 8092;
        ctxSize = 32768;
        parallel = 4;
        rocmEnv = { };
      };

      # Agent orchestrator
      services.orchestrator = {
        description = "Qwen3.5-9B - Agent Orchestrator";
        model = "/srv/models/support/Qwen3.5-9B-Q4_K_M.gguf";
        port = 8093;
        ctxSize = 131072;
        parallel = 2;
        reasoning = {
          format = "deepseek";
          budget = 2048;
        };
      };
    };

    # Whisper is a separate binary, not llama-server
    units."whisper-server.service" = {
      text = ''
        [Unit]
        Description=Whisper.cpp Server (Speech-to-Text, CPU)
        After=network.target

        [Service]
        Type=simple
        User=deploy
        ExecStart=/opt/build/whisper.cpp/build/bin/whisper-server \
          --model /srv/models/support/ggml-base.en.bin \
          --host 0.0.0.0 --port 8094 \
          --threads 4
        Restart=on-failure
        RestartSec=5

        [Install]
        WantedBy=multi-user.target
      '';
      enabled = true;
    };
  };
}
