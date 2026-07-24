# GTR-151 — AMD Ryzen AI MAX+ 395 (192.168.3.132)
# The "Qwen3.6" node — both family variants co-hosted:
#   :8084  Qwen3.6-35B-A3B MoE  + classic Qwen3.5-0.8B draft (~48 tok/s)
#                                 (MTP crashes the recurrent MoE — see below)
#   :8085  Qwen3.6-27B    dense + MTP self-speculation (~16 tok/s, pure 3.6)
# Build: /opt/llama-rocm-latest (commit 6a257d4) — fork retired, PR #19493
# natively handles qwen35 spec. 131GB unified VRAM, ROCm 7.13 (TheRock), gfx1151
{ pkgs, ... }:

{
  imports = [
    ../modules/llm-inference.nix
    ../modules/iscsi.nix
    ../modules/k0s.nix
  ];

  nixfleet = {
    host = {
      name = "gtr-151";
      base = "ubuntu";
      addr = "192.168.3.132";
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
      # Runs on LATEST UPSTREAM llama.cpp built for gfx1151 at
      # /opt/llama-rocm-latest (commit 6a257d4). Replaces the old custom fork:
      # upstream PR #19493 natively handles qwen35 hybrid speculation, so the
      # 6 fork patches are obsolete (verified empirically — native spec gives
      # 100% draft acceptance). The build also adds --spec-type draft-mtp.
      #
      # Speculation, after benchmarking AND stability-testing on gfx1151:
      #   MoE (:8084)   — CLASSIC Qwen3.5-0.8B draft (~48 tok/s). MTP on the
      #                   recurrent MoE is UNSTABLE here: it crashes with
      #                   "ROCm error: unspecified launch failure" during
      #                   warmup (same GPU-fault family as the n_max=4 wedge),
      #                   so despite MTP being marginally faster (~61) it's not
      #                   worth the crash/wedge risk. Classic draft is rock-solid.
      #   dense (:8085) — MTP self-speculation (~16 tok/s, pure 3.6) — stable on
      #                   the dense (non-recurrent) arch.
      services.qwen36-spec = {
        description = "Qwen3.6-35B-A3B MoE + classic draft (MTP unstable on MoE)";
        # Non-MTP GGUF (UD-Q6_K_XL, 29.7GB).
        model = "/srv/models/Qwen3.6-35B-A3B-UD-Q6_K_XL.gguf";
        binary = "/opt/llama-rocm-latest/llama-server";
        ldLibraryPath = "/opt/llama-rocm-latest:/opt/rocm-sdk/lib:/opt/rocm-sdk/lib/rocm_sysdeps/lib:/opt/rocm-sdk/lib/llvm/lib:/opt/rocm-sdk/lib/host-math/lib";
        port = 8084;
        ctxSize = 200000;
        batchSize = 512;
        ubatchSize = 512;
        newCli = true; # new build: --draft-max renamed --spec-draft-n-max
        # No fork workarounds: upstream handles qwen35 recurrent memory + the
        # prompt-cache restore bug. Defaults apply (ctx-checkpoints=32,
        # cache-reuse=256).
        draft = {
          model = "/srv/models/Qwen3.5-0.8B-Q4_K_M.gguf";
          # n-max 4->6 + pMin 0.6->0.5: benchmarked +8.5% (62.8 -> 68.1 tok/s) on
          # gfx1151 via deeper draft (accept 138 -> 217). 2026-07-04.
          max = 6;
          min = 1;
          pMin = 0.5;
        };
        reasoning = {
          format = "deepseek";
          budget = 2048;
        };
        # Sampler nudge — see hosts/gtr-152.nix / docs/llm-proxy-usage.md.
        # (qwen36-27b below intentionally KEEPS Qwen's official coding sampling
        # — temp=0.6 top-k=20 — so don't blanket-nudge it.)
        extraFlags = [
          "--min-p 0.01"
          "--top-p 0.98"
        ];
      };

      # Qwen3.6-27B DENSE — MOVED to gtr-153 (2026-07-17) to relieve GPU
      # contention here: gtr-151 was pegged at 100% GPU with three big models
      # (35B-A3B MoE + 27B + ornith) sharing one gfx1151, which spiked ornith's
      # latency. The 27B now runs on gtr-153's otherwise-idle GPU
      # (hosts/gtr-153.nix), and ornith's freed headroom goes to a larger
      # context (65K -> 200K) below. See docs/llm-proxy-usage.md.

      # Ornith-1.0-35B-MoE (deepreinforce-ai) — SOTA open coding-agent model,
      # post-trained on Qwen3.6-35B-A3B, so it runs on the same latest-upstream
      # build + shares the Qwen3.5-0.8B draft (100% accept on predictable ctx).
      # Tuned 2026-07-04: classic draft n-max 8 / p-min 0.5 → ~78 tok/s on
      # predictable context, ~42 on realistic code (localmaxxing Q4-no-draft ~58).
      services.ornith = {
        description = "Ornith-1.0-35B-MoE coding agent (Qwen3.6-35B post-train) + classic draft";
        # 1M-YaRN GGUF (satgeze): YaRN factor-4 metadata (native 262K -> 1M
        # ceiling, applied automatically, no rope flags) + a grafted MTP head.
        # We run CLASSIC draft here (proven ~78 tok/s) and leave the MTP head
        # unused — the gtr-152 instance is the MTP trial. One of 3 load-balanced
        # Orniths (gtr-151/152/153); see nix-fleet-hosts litellm/config.yaml.
        model = "/srv/models/ornith-1.0-35b-1M-MTP-Q6_K.gguf";
        binary = "/opt/llama-rocm-latest/llama-server";
        ldLibraryPath = "/opt/llama-rocm-latest:/opt/rocm-sdk/lib:/opt/rocm-sdk/lib/rocm_sysdeps/lib:/opt/rocm-sdk/lib/llvm/lib:/opt/rocm-sdk/lib/host-math/lib";
        port = 8086;
        # ctx-size is the TOTAL KV budget, split across --parallel slots: 524288
        # / 2 = 256K per concurrent request. parallel=2 lets each backend serve 2
        # requests at once (the pool bottlenecked at 1 slot/backend in load
        # testing). TIGHTEST node (co-hosts the 200K 35B-A3B MoE :8084): 512K KV
        # is ~1.33x the old 384K; if this OOMs at boot, drop ctxSize to 393216
        # (192K/req) or 262144. q4_0 KV + flash-attn keep it compact.
        ctxSize = 524288;
        parallel = 2;
        batchSize = 512;
        ubatchSize = 512;
        newCli = true;
        draft = {
          model = "/srv/models/Qwen3.5-0.8B-Q4_K_M.gguf";
          max = 8;
          min = 1;
          pMin = 0.5;
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
  };
}
