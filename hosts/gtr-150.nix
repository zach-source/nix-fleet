# GTR-150 — AMD Ryzen AI MAX+ 395 (192.168.3.133)
# The SUPPORT box (2026-10-05): embeddings, rerankers, the Qwen3Guard safety
# classifier and whisper — the small models every other tier leans on. Qwen3.8-27B
# ran here 2026-10-10..15 and was removed for lack of use. Its
# chat LLM (Gemma 4 26B-A4B) was retired so each gtr box carries one big
# model at most; the gateway's "gemma4" name is now an alias for gtr-151.
# 131GB unified VRAM, ROCm (stock lemonade build), gfx1151
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
      name = "gtr-150";
      base = "ubuntu";
      addr = "192.168.3.133";
    };

    # k0s worker, declaratively managed. system-reserved=78Gi -> ~44Gi k8s
    # allocatable: the support models (~28Gi GTT) plus Qwen3.8-27B-pi
    # (~38Gi, 2026-11-20). It was 56Gi from 2026-10-15 to 2026-11-20.
    k0s.worker = {
      enable = true;
      systemReservedMemory = "78Gi";
    };

    # iSCSI initiator so the Synology CSI driver can attach btrfs-backed LUNs.
    modules.iscsi.enable = true;

    # Blacklist Synology LUNs from dm-multipath auto-claim — see
    # modules/multipath.nix (2026-07-25 gastown-town readonly incident, this
    # node is where it happened).
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

      # Safety classifier — Qwen3Guard-Gen-4B, the gateway's ENFORCED pre-call
      # guardrail since 2026-10-05 (replaced ShieldGemma 2B, retired from this
      # box the same day; English-only, one call per policy). Qwen3Guard-Gen covers
      # 119 languages with 3-tier severity (safe / controversial / unsafe)
      # and a richer category taxonomy. The "Gen" variant generates the
      # verdict via its chat template (jinja on); not a reasoning model.
      services.guard = {
        description = "Qwen3Guard-Gen-4B - Safety Classifier (multilingual)";
        model = "/srv/models/support/Qwen3Guard-Gen-4B.Q8_0.gguf";
        port = 8098;
        ctxSize = 16384;
        parallel = 2;
        rocmEnv = { };
      };

      # Code completion (FIM)
      #
      # DISABLED 2026-08-15: unused. Over the preceding 30 days this served
      # SIX generations total, two of them longer than 20 tokens. gtr-150 is
      # the fleet's tightest node (~25G free of 122G across 10 services), so
      # the slot is worth more than the model. Config kept for a quick revert:
      # flip `enable = true` + `nixfleet apply -H gtr-150`.
      services.codecomplete = {
        enable = false;
        description = "Qwen2.5-Coder 1.5B - FIM Code Completion";
        model = "/srv/models/support/qwen2.5-coder-1.5b-instruct-q8_0.gguf";
        port = 8092;
        ctxSize = 32768;
        parallel = 4;
        rocmEnv = { };
        # Sampler nudge — see hosts/gtr-152.nix / docs/llm-proxy-usage.md.
        extraFlags = [
          "--min-p 0.01"
          "--top-p 0.98"
        ];
      };

      # Agent orchestrator
      #
      # DISABLED 2026-08-15: effectively unused. Its request count looks busy
      # (4,606 generations in 30 days) but 4,590 of those are ONE-token
      # LiteLLM health probes firing every ~35s — only 15 real generations in
      # a month. Count generations excluding `prompt eval time` lines before
      # trusting traffic numbers here. Revert: `enable = true` + apply.
      services.orchestrator = {
        enable = false;
        description = "Qwen3.5-9B - Agent Orchestrator";
        model = "/srv/models/support/Qwen3.5-9B-Q4_K_M.gguf";
        port = 8093;
        ctxSize = 131072;
        parallel = 2;
        reasoning = {
          format = "deepseek";
          budget = 2048;
        };
        # Sampler nudge — see hosts/gtr-152.nix / docs/llm-proxy-usage.md.
        extraFlags = [
          "--min-p 0.01"
          "--top-p 0.98"
        ];
      };

      # Reranker (cross-encoder, for RAG — pairs with the nomic-embed embeddings)
      # jina-reranker-v2: small (BERT, 1024-token max → ctx 4096 / 4 slots =
      # 1024 per pair), reliable. Note: the near-zero-score issue with Qwen3-
      # Reranker (llama.cpp#16407) was broken *community* GGUFs missing the
      # cls.output.weight classifier; the properly-converted GGUF (qwen3-reranker
      # below) works. Not a chat model: jinja off, --pooling rank.
      services.reranker = {
        description = "Jina Reranker v2 Base Multilingual (cross-encoder)";
        model = "/srv/models/support/jina-reranker-v2-base-multilingual-Q8_0.gguf";
        port = 8095;
        ctxSize = 4096;
        parallel = 4;
        jinja = false;
        extraFlags = [
          "--reranking"
          "--pooling rank"
        ];
        rocmEnv = { };
      };

      # Qwen3-Embedding-8B — SOTA embeddings (#1 MTEB multilingual, 70.6),
      # 4096-dim, 32K-capable. Added alongside nomic-embed (:8090) so existing
      # 768-dim RAG collections keep working until re-embedded. Decoder model,
      # last-token pooling. For non-causal pooling, ubatch must be >= the input
      # length, so batch/ubatch match the per-slot ctx (16384/4 = 4096).
      services."qwen3-embedding" = {
        description = "Qwen3-Embedding-8B (SOTA, 4096-dim)";
        model = "/srv/models/support/Qwen3-Embedding-8B-Q8_0.gguf";
        port = 8096;
        ctxSize = 16384;
        parallel = 4;
        batchSize = 4096;
        ubatchSize = 4096;
        embedding = true;
        jinja = false;
        extraFlags = [ "--pooling last" ];
      };

      # Qwen3-Reranker-8B — SOTA reranker. Voodisss GGUF (officially converted,
      # includes the cls.output.weight yes/no classifier). Needs all three:
      # --reranking --pooling rank AND --embedding (embedding=true).
      services."qwen3-reranker" = {
        description = "Qwen3-Reranker-8B (SOTA cross-encoder)";
        model = "/srv/models/support/Qwen3-Reranker-8B-Q8_0.gguf";
        port = 8097;
        ctxSize = 16384;
        parallel = 4;
        batchSize = 4096;
        ubatchSize = 4096;
        embedding = true;
        jinja = false;
        extraFlags = [
          "--reranking"
          "--pooling rank"
        ];
      };

      # Qwen3.8-27B with the harness-hub "pi-adapted" LoRA (2026-11-20).
      # It is an RL post-train for the pi coding agent, with thinking off.
      # Card scores: SWE-bench Pro hard 39.2 -> 64.7, TB2.1 39.5 -> 55.8.
      # The card gives these settings:
      #   - a Q8_0 base (lower quants move away from the trained behavior)
      #   - 131072 context (the eval setting)
      #   - thinking off, temp 0.8, top-p 1.0
      #   - clients use the chat-completions API, not Responses
      #
      # The port is 8099. The atelet DaemonSet holds hostPort 8085 on all
      # k0s nodes (see 62da41b, the old qwen38-27b slot).
      services.qwen38-pi = {
        description = "Qwen3.8-27B pi-adapted LoRA (coding agent), thinking off";
        model = "/srv/models/Qwen3.8-27B-MTP-Q8_0.gguf";
        port = 8099;
        ctxSize = 131072;
        parallel = 2;
        # The base GGUF has its own MTP head. The LoRA does not change the
        # nextn layers, so the base model makes the drafts. Acceptance can be
        # lower, but the adapted model checks each draft token.
        mtp = {
          nMax = 2;
        };
        reasoning = {
          format = "deepseek";
          budget = 2048;
        };
        # Canary: llama.cpp 919794500 + zach-source/llama-cpp-rdna-boosts
        # v18-919794500-r366 on /opt/rocm-10.1 (2026-11-27). build-rdna.sh in
        # /opt/build/rdna-boosts built it on this host
        # (copy in scripts/build-rdna.sh).
        # llama-bench Qwen3.8-27B Q8_0, master -> rdna (tok/s):
        #   pp512 349 -> 445, pp4096 197 -> 535, tg128 7.5 -> 8.0
        # Live with MTP: decode 15 -> 17.6, prefill 247 -> 335.
        binary = "/opt/llama-rocm-rdna/llama-server";
        ldLibraryPath = "/opt/llama-rocm-rdna:/opt/rocm-10.1/lib:/opt/rocm-10.1/lib/rocm_sysdeps/lib:/opt/rocm-10.1/lib/llvm/lib:/opt/rocm-10.1/lib/host-math/lib";
        extraFlags = [
          "--lora"
          "/srv/models/Qwen3.8-27B-pi-lora-F16.gguf"
          # --fit off: skip the memory-fit pass that re-reads the whole GGUF.
          "--fit"
          "off"
          # One KV pool for both slots, so one request can use all 131072.
          "--kv-unified"
          "--temp"
          "0.8"
          "--top-p"
          "1.0"
          # Single-quoted so systemd keeps the inner double quotes.
          "--chat-template-kwargs"
          "'{\"enable_thinking\":false}'"
        ];
      };
    };

    # Whisper is a separate binary, not llama-server.
    # Upgraded base.en → large-v3-turbo: ~809M params / 4 decoder layers,
    # multilingual, far lower WER than base while staying fast. CPU inference
    # (no GPU groups on this unit); bumped threads 4→8 to offset the larger
    # model. GPU offload (Vulkan/ROCm whisper.cpp) is a future option if
    # CPU latency becomes an issue.
    systemd.units."whisper-server.service" = {
      text = ''
        [Unit]
        Description=Whisper.cpp Server (large-v3-turbo, Speech-to-Text, CPU)
        After=network.target

        [Service]
        Type=simple
        User=deploy
        ExecStart=/opt/build/whisper.cpp/build/bin/whisper-server \
          --model /srv/models/support/ggml-large-v3-turbo.bin \
          --host 0.0.0.0 --port 8094 \
          --threads 8
        Restart=on-failure
        RestartSec=5

        [Install]
        WantedBy=multi-user.target
      '';
      enabled = true;
    };
  };
}
