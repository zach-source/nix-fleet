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
