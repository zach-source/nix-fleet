# GTR-150 — AMD Ryzen AI MAX+ 395 (192.168.3.133)
# The SUPPORT box (2026-10-05): embeddings, rerankers, the Qwen3Guard safety
# classifier and whisper — the small models every other tier leans on —
# plus Qwen3.8-27B, the thinking tier, since gtr-153 was drained (2026-10-10). Its
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
    # allocatable. Was 56Gi from 2026-10-05 while this box ran only the
    # support models (~28Gi GTT); Qwen3.8-27B (~42Gi) moved here 2026-10-10,
    # so inference is ~70Gi again.
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

      # Qwen3.8-27B DENSE — the THINKING tier. Moved here from gtr-153 on
      # 2026-10-10 (that node is being removed); the only box with room for
      # it unchanged (~42GiB GTT at 512K). Needs /opt/llama-rocm-latest,
      # /opt/rocm-sdk AND /opt/build/llama.cpp/build-qwen36-spec/bin (the
      # binary's RPATH — it loads its libllama/ggml .so files from there),
      # all copied from gtr-153 out of band. The stock /opt/llama-rocm build
      # the support models use is too old for qwen35 MTP.
      #
      # History: succeeds the Qwen3.6-27B that held this slot
      # (:8085) from 2026-07-17 until it was disabled 2026-07-27 for memory.
      # Same GGUF architecture string (`qwen35`) as the 3.6 it replaces, so the
      # existing /opt/llama-rocm-latest build loads it unchanged; the old 3.6
      # GGUF stays on disk for a one-line revert.
      #
      # Deliberately Q5_K_XL (20.2GB), not the Q6_K_XL (25.9GB) the 3.6 used:
      # this node co-hosts hauhaucs-uncensored + k0s pods and was swap-thrashing
      # at the 26GB size. ctxSize also stays at the slot's proven 131072 even
      # though 3.8 is natively 262144 — raise it only after watching `free -g`
      # here, since the hybrid Gated-DeltaNet arch keeps KV cheap (only 16 of 64
      # layers are full attention) and the headroom may well be there.
      services.qwen38-27b = {
        description = "Qwen3.8-27B dense (quality/coding/agentic) + MTP self-speculation";
        model = "/srv/models/Qwen3.8-27B-UD-Q5_K_XL.gguf";
        binary = "/opt/llama-rocm-latest/llama-server";
        ldLibraryPath = "/opt/llama-rocm-latest:/opt/rocm-sdk/lib:/opt/rocm-sdk/lib/rocm_sysdeps/lib:/opt/rocm-sdk/lib/llvm/lib:/opt/rocm-sdk/lib/host-math/lib";
        # 8099, not the 8085 it used on gtr-153: ate-system's atelet DaemonSet
        # (2026-10-06) claims hostPort 8085 on every k0s node, so the CNI
        # forwards <node>:8085 to atelet and llama-server never sees a request.
        # That silently broke this model on gtr-153 from 2026-10-06.
        port = 8099;
        # 512K context on a natively-262K model: static YaRN, factor 2 — the
        # factor Qwen's cards give for 524288. Same window on every gtr box
        # (2026-10-05). Static YaRN applies at every length, so very short
        # prompts may lose a little quality; drop the rope/yarn/override-kv
        # flags and set ctxSize = 262144 to undo.
        #
        # 4 slots sharing ONE unified KV pool (--kv-unified): any request can
        # still use the full 524288 window (the context_length override is the
        # per-request cap), concurrent requests split the pool, and memory is
        # the same as one pool of this size. Unified KV also turns on
        # --cache-idle-slots, which llama-server otherwise disables: an idle
        # slot's processed prompt is saved to the --cache-ram host cache
        # (8 GiB default) and restored when that conversation returns, instead
        # of re-prefilling it. Without it, a short request waited ~2 min behind
        # an agent re-prefilling a 111k-token conversation (2026-10-05).
        ctxSize = 524288;
        parallel = 4;
        # CORRECTION 2026-08-15: this GGUF already carries its own MTP head —
        # `strings` on the file shows qwen35.nextn_predict_layers and
        # blk.64.nextn.* tensors. An earlier revision also passed
        # `--spec-draft-model /srv/models/mtp-Qwen3.8-27B-Q8_0.gguf`, which
        # overrode that built-in head with ggml-org's Q8_0 one against these
        # Q5_K_XL weights. Measured draft acceptance was 57% on a code-gen
        # benchmark, against 81% for gtr-151's single-repo merged-MTP build on
        # the same workload. The external draft file is now dropped; it stays
        # on disk but is unused.
        mtp = {
          nMax = 2;
        };
        reasoning = {
          format = "deepseek";
          budget = 2048;
        };
        extraFlags = [
          # --fit off: skip the auto memory-fit step, which re-reads the whole
          # GGUF to measure (~8min cold load observed on gtr-153's ZFS /srv).
          "--fit"
          "off"
          # Qwen3.8 model card, thinking mode (3.6 used temp 0.6; 3.8 asks 1.0).
          "--temp"
          "1.0"
          "--top-p"
          "0.95"
          "--top-k"
          "20"
          "--min-p"
          "0.0"
          "--rope-scaling"
          "yarn"
          "--rope-scale"
          "2"
          "--yarn-orig-ctx"
          "262144"
          # llama-server caps every slot at the GGUF's declared training
          # context ("exceeds the training context of the model - capping"),
          # whatever the rope flags say, so declare the extended window too.
          "--kv-unified"
          "--override-kv"
          "qwen35.context_length=int:524288"
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
