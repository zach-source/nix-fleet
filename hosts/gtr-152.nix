# GTR-152 — AMD Ryzen AI MAX+ 395 (192.168.3.134)
# Throughput king: Qwen3-Coder-30B-A3B with speculative decoding
# 131GB unified VRAM, ROCm (stock lemonade build), gfx1151
{ pkgs, ... }:

{
  imports = [
    ../modules/llm-inference.nix
    ../modules/iscsi.nix
  ];

  nixfleet = {
    host = {
      name = "gtr-152";
      base = "ubuntu";
      addr = "192.168.3.134";
    };

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
        # Widen the default sampling window so client-side diversity params
        # (temperature, seeds, prompt perturbations) have room to actually
        # change the trajectory. llama.cpp defaults are min-p=0.05 / top-p=0.95;
        # we loosen both. For very peaked-logit prompts the model may still
        # converge — true output diversity often needs structural prompt
        # variation client-side. See docs/llm-proxy-usage.md.
        extraFlags = [
          "--min-p 0.01"
          "--top-p 0.98"
        ];
      };
    };

    # Two extra models on the VULKAN backend (separate /usr/local/bin/llama-server
    # build + radeon Vulkan ICD), previously hand-installed out-of-band at
    # /etc/systemd/system. Declared here verbatim so NixFleet manages them; kept
    # off the llmInference module because that module is ROCm-specific (binary,
    # LD_LIBRARY_PATH, HIP env all differ). opus is ordered after gemma so the two
    # don't initialise the iGPU simultaneously.
    systemd.units = {
      "llama-server.service" = {
        enabled = true;
        text = ''
          [Unit]
          Description=llama.cpp Server (Gemma 4 31B - Vulkan GPU)
          After=network.target

          [Service]
          Type=simple
          User=deploy
          Group=deploy
          SupplementaryGroups=render video
          Environment=LD_LIBRARY_PATH=/usr/local/lib
          Environment=VK_ICD_FILENAMES=/usr/share/vulkan/icd.d/radeon_icd.x86_64.json
          ExecStart=/usr/local/bin/llama-server --model /srv/models/gemma-4-31B-it-Q8_0.gguf --host 0.0.0.0 --port 8080 --ctx-size 32768 --threads 16 --gpu-layers 999 --parallel 2
          Restart=on-failure
          RestartSec=10
          LimitNOFILE=65536

          [Install]
          WantedBy=multi-user.target
        '';
      };

      "llama-server-opus-distilled.service" = {
        enabled = true;
        text = ''
          [Unit]
          Description=llama.cpp Server (Qwen 27B Opus Distilled - Vulkan GPU)
          After=network.target llama-server.service

          [Service]
          Type=simple
          User=deploy
          SupplementaryGroups=render video
          Environment=LD_LIBRARY_PATH=/usr/local/lib
          Environment=VK_ICD_FILENAMES=/usr/share/vulkan/icd.d/radeon_icd.x86_64.json
          ExecStart=/usr/local/bin/llama-server --model /srv/models/Qwen3.5-27B-Opus-Distilled-Q8_0.gguf --host 0.0.0.0 --port 8081 --ctx-size 32768 --threads 8 --gpu-layers 999 --parallel 2
          Restart=on-failure
          RestartSec=10
          LimitNOFILE=65536

          [Install]
          WantedBy=multi-user.target
        '';
      };
    };
  };
}
