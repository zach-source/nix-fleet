# GLM-5.3-Flash EXL3 across the stacked DGX Spark pair — shared definition.
#
# Companion to hosts/dgx-spark-dsv4.nix and structured identically: both Sparks
# take a byte-identical env file, and each host file states only its rank. The
# two models are mutually exclusive (see the Conflicts= note in
# modules/dspark-glm53.nix) — ~80 GiB of 121 GiB per node each, so only one can
# be resident. Both stay on disk; switching is a systemctl start.
#
# Unlike the dsv4 recipe, this one has no NODE_RANK key: the head's start.sh
# owns the whole cluster bring-up, including standing up rank 1 over SSH. The
# module's nodeRank therefore only decides which node gets a service unit.
{
  nodeRank,
  # Rank 0 and rank 1 on the ConnectX-7 fabric — NOT the management IPs. Ray,
  # NCCL and the weight rsync all ride the 200GbE link. Matches nodeIndex 10/11
  # in modules/dgx-spark-cluster.nix, same as the dsv4 stack.
  headAddr ? "192.168.100.10",
  workerAddr ? "192.168.100.11",
  # The CX7 interface and its RDMA device. The recipe's .env.example ships the
  # RIGHT port for the head (enp1s0f1np1 / rocep1s0f1) and the LEFT for the
  # worker; this pair is cabled LEFT on BOTH nodes, so both are overridden.
  #
  # Verified on spark-5267 2026-09-02 — ibdev2netdev reports rocep1s0f0 =>
  # enp1s0f0np0 (Up) with the right-hand pair (Down). Pointing NCCL at a down
  # interface is the silent-rendezvous-hang case, and it is the single most
  # expensive mistake available here, so it is spelled out rather than defaulted.
  cx7Interface ? "enp1s0f0np0",
  cx7Hca ? "rocep1s0f0",
}:

{
  nixfleet.modules.dsparkGlm53 = {
    enable = true;
    inherit nodeRank;

    settings = {
      # --- topology -------------------------------------------------------
      HEAD_IP = headAddr;
      WORKER_IP = workerAddr;
      HEAD_CX7_IF = cx7Interface;
      WORKER_CX7_IF = cx7Interface;
      HEAD_CX7_IB = cx7Hca;
      WORKER_CX7_IB = cx7Hca;
      TP = "2";
      NNODES = "2";
      # 29521, well clear of the dsv4 stack's 25000. They never run at the same
      # time, but a stale listener from a half-dead rank is a real failure mode
      # and disjoint ports make it obvious which stack owns it.
      MASTER_PORT = "29521";

      # --- model ----------------------------------------------------------
      # The Mia-AiLab id is a byte-identical public mirror of brandonmusic's
      # EXL3/TR3 4bpw quant; MODEL_FALLBACK is the original, which start.sh
      # tries if the mirror 404s. Revision-pinned so a mirror re-push cannot
      # change the weights under a deploy.
      MODEL = "Mia-AiLab/GLM-5.3-Flash-EXL3-TR3-4bpw";
      MODEL_FALLBACK = "brandonmusic/GLM-5.3-Flash-tr3-4bpw";
      MODEL_REVISION = "25a44fdbf16862a46b7cc9921142c6c81350af2f";
      SERVED_MODEL_NAME = "GLM-5.3-Flash-EXL3";
      QUANTIZATION = "exl3";

      # --- serving --------------------------------------------------------
      # 8888, matching the dsv4 stack deliberately: the two can never both be
      # up, so a single endpoint always points at whichever model is running.
      # The served model name is what distinguishes them on the wire.
      PORT = "8888";
      # 768k, not the recipe's 1M, and this is measured rather than cautious.
      # At 1000000 vLLM sized the KV pool at 14.52 GiB against 13.27 GiB
      # available and refused to start. The binding constraint is GB10's unified
      # memory: weights, page cache and KV all come out of the same 121 GiB, and
      # this pair carries a 164 GiB checkpoint where upstream's numbers assume
      # their own geometry.
      #
      # 768k needs ~11.2 GiB, which leaves real headroom instead of landing 1.25
      # GiB short. Raise it once a run has shown how much KV is actually free —
      # `/metrics` reports the pool — but do not put 1M back without checking.
      MAX_MODEL_LEN = "786432";
      MAX_NUM_SEQS = "4";
      # 7168 is the maintainer default for MAX_NUM_SEQS=4. It is not arbitrary:
      # this model caches in 3584-token pages whose KDA state is checkpointed
      # only when a scheduler step lands exactly on a page boundary, so a value
      # that is not a multiple of 3584 silently reads 0% prefix-cache hits.
      MAX_NUM_BATCHED_TOKENS = "7168";
      GPU_MEM_UTIL = "0.87";
      KV_CACHE_DTYPE = "fp8";

      # --- speculative decoding -------------------------------------------
      # DFlash2 k=7, not the model's built-in MTP. This is the single biggest
      # throughput lever on this hardware: ~63 tok/s structured against ~24.6
      # for MTP k=2 on the same pair. MTP_TOKENS stays declared as the fallback
      # if SPEC_METHOD is ever flipped to mtp.
      SPEC_METHOD = "dflash";
      DFLASH_MODEL = "incoai/GLM-5.3-Flash-DFlash2";
      DFLASH_TOKENS = "7";
      DFLASH_DRAFT_TP = "2";
      MTP_TOKENS = "2";

      # --- kernels ---------------------------------------------------------
      # EXL3 fused MoE + the E2 fat-expert prefill kernels, both on. E2 is worth
      # ~20% on fully uncached long-context prefill and was promoted to the
      # production path by upstream after a controlled five-sample-per-rung
      # comparison. Do NOT add --moe-backend marlin here — that is the NVFP4
      # recipe's flag and it does not belong on an EXL3 serve.
      EXL3_FUSED_MOE = "1";
      EXL3_FAT_KERNEL = "1";
      ENFORCE_EAGER = "0";
      CG_ESTIMATE = "1";

      # Use the digest-pinned GHCR image; do not rebuild it locally.
      #
      # start.sh stamps the image with a hash of the recipe tree and rebuilds
      # from the Dockerfile whenever the stamp does not match — which it never
      # does for a pulled image, so the default is to rebuild on every cold
      # start. That throws away the whole point of pinning by digest (the
      # locally built image is not the artifact we verified) and costs a CUDA
      # build on an aarch64 Spark. The image is public and anonymously
      # pullable, so there is no reason to build it.
      SKIP_BUILD = "1";

      # --- multimodal -------------------------------------------------------
      # GLM-5.3-Flash is natively multimodal; this is the first vision-capable
      # model on the fleet. Profiling the MM encoder at boot is skipped because
      # it costs minutes and estimates a shape the recipe already pins.
      LANGUAGE_MODEL_ONLY = "0";
      SKIP_MM_PROFILING = "1";
      # The embedded single quotes are load-bearing, not decoration. start.sh
      # reads this file with `set -a; source`, so bash strips the inner double
      # quotes from a bare value and vLLM receives {image:4,video:1}, which is
      # not JSON:
      #   vllm serve: error: argument --limit-mm-per-prompt:
      #   Value {image:4,video:1} cannot be converted to <function loads>
      # Any value here containing quotes or spaces needs the same treatment.
      LIMIT_MM = "'{\"image\":4,\"video\":1}'";

      # --- recipe behaviour flags ------------------------------------------
      GLM53_BOOT_SHAPE_WARMUP = "1";
      GLM53_SUPPRESS_STOPS_IN_REASONING = "1";
      GLM53_MIXED_PREFILL_CHUNK = "skip";
      GLM53_INDEXER_WORKSPACE = "stock";
      GLM53_SPINWAIT_MS = "stock";
      ABLIT = "0";

      # --- fabric -----------------------------------------------------------
      USE_HOST_NCCL = "0";
      NCCL_SO_NAME = "libnccl.so.2.30.7";
      NCCL_IB_GID_INDEX = "3";
      NCCL_DEBUG = "WARN";
    };
  };
}
