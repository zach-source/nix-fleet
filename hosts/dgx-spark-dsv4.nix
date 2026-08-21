# DeepSeek-V4-Flash across a stacked DGX Spark pair — shared definition.
#
# Both Sparks take a byte-identical env file except for NODE_RANK, so the
# settings live here once and each host file states its rank. Rank divergence is
# the classic way a TP job hangs in NCCL rendezvous with no useful error, so it
# is worth the indirection.
#
# This used to declare a native vLLM unit via nixfleet.modules.vllm, pointed at
# /opt/dsv4/bin/dsv4-vllm-entrypoint. That entrypoint does not exist and is not
# packaged by anyone, so the declaration shipped `enable = false` and the model
# was actually started by hand from a Docker Compose recipe. This file now
# declares the thing that really runs. See modules/dspark-dsv4.nix.
#
# Measured on this pair 2026-08-21: HumanEval+ pass@1 0.933 at
# DEFAULT_THINKING=low, 161.3 tok/s aggregate at 6-way concurrency, ~79 GiB GPU
# per node. See the DEFAULT_THINKING comment below before raising it — `max`
# scored 0.665 on the same suite, for 4.5x the tokens.
{
  nodeRank,
  # Rank 0's address on the ConnectX-7 fabric — NOT its management IP. NCCL and
  # the rendezvous both ride the 200GbE link. Matches nodeIndex 10 in
  # modules/dgx-spark-cluster.nix.
  masterAddr ? "192.168.100.10",
  workerAddr ? "192.168.100.11",
  # The CX7 interface NCCL should use. Left unset, NCCL picks by its own
  # heuristics and may choose the 10GbE management NIC — the job still runs,
  # just at a fraction of the bandwidth, which is miserable to diagnose.
  #
  # The LEFT port is the cabled one on this pair (verified on spark-5267), so
  # this must stay in step with modules.dgxSparkCluster.interfaces in both host
  # files. Pointing NCCL at a down interface is the silent-hang case.
  ncclInterface ? "enp1s0f0np0",
  # RDMA device for the same physical port. `ibdev2netdev` maps the two.
  ncclHca ? "rocep1s0f0",
}:

{
  nixfleet.modules.dsparkDsv4 = {
    enable = true;
    inherit nodeRank;

    settings = {
      # --- topology -------------------------------------------------------
      # WORKER_HOST is what the head SSHes to. It is the fabric address, so the
      # peer SSH rides the 200GbE link too.
      WORKER_HOST = workerAddr;
      WORKER_DIR = "/opt/dspark";
      WORKER_SCRIPT_DIR = "/opt/dspark";
      MASTER_ADDR = masterAddr;
      MASTER_PORT = "25000";
      NODE_RANK = toString nodeRank;
      VLLM_HOST_IP = masterAddr;
      WORKER_VLLM_HOST_IP = workerAddr;
      # The head's start script sets HEADLESS=1 for the worker itself; this
      # stays empty so rank 0 doesn't headless itself out of serving HTTP.
      HEADLESS = "";

      # --- fabric ---------------------------------------------------------
      # The recipe's template ships the RIGHT port (enp1s0f1np1) for the socket
      # interfaces. This pair is cabled on the LEFT, and the template's default
      # would have quietly put the torch/gloo control traffic on a down NIC.
      NCCL_IB_HCA = ncclHca;
      NCCL_SOCKET_IFNAME = ncclInterface;
      TP_SOCKET_IFNAME = ncclInterface;
      GLOO_SOCKET_IFNAME = ncclInterface;
      NCCL_NET = "IB";
      NCCL_IB_DISABLE = "0";
      NCCL_CROSS_NIC = "1";
      NCCL_CUMEM_ENABLE = "0";
      NCCL_IGNORE_CPU_AFFINITY = "1";
      NCCL_NVLS_ENABLE = "0";
      NCCL_DEBUG = "WARN";

      # --- model ----------------------------------------------------------
      # Absolute, not the recipe template's literal ${HOME}. Compose expands
      # that from whatever environment invoked it, so a hand-run as root would
      # mount /root/.cache and the container would find no weights at all.
      HF_CACHE = "/home/deploy/.cache/huggingface";
      WORKER_HF_CACHE = "/home/deploy/.cache/huggingface";
      # Offline: the 156GB of weights are already resident on both nodes and a
      # deploy must never be able to trigger a re-download.
      HF_HUB_OFFLINE = "1";
      TRANSFORMERS_OFFLINE = "1";
      HF_HUB_DISABLE_XET = "1";
      ABLITERATED = "0";
      DSPARK_MODEL_OFFICIAL = "deepseek-ai/DeepSeek-V4-Flash-0731";
      # Unused while ABLITERATED=0, but the recipe's scripts dereference it
      # unconditionally when picking which model name to serve.
      DSPARK_MODEL_ABLITERATED = "drowzeys/keys-DeepSeekV4-Flash-GA-0731-Dspark-Abliterated-32-32";
      DSPARK_REVISION = "9e165c30e2704aec5d9d593cce3eebd58bbef1cb";
      # Empty = let the container find encoding_dsv4.py in the model snapshot,
      # which is what it does. Declared rather than omitted so the file matches
      # the recipe's template surface exactly.
      DSPARK_ENCODING_FILE = "";
      SERVED_MODEL_NAME = "deepseek-v4-flash-0731";

      # --- serving --------------------------------------------------------
      VLLM_HOST = "0.0.0.0";
      # 8888, not 8000. Worth stating plainly because the inventory and every
      # other fleet LLM use 8000, and a health check aimed at 8000 here reads
      # "connection refused" while the model is perfectly healthy.
      VLLM_PORT = "8888";
      MAX_MODEL_LEN = "1048576";
      # At 1M context the KV cache, not compute, is the binding constraint.
      MAX_NUM_SEQS = "6";
      MAX_NUM_BATCHED_TOKENS = "8192";
      LONG_PREFILL_TOKEN_THRESHOLD = "1024";
      GPU_MEMORY_UTILIZATION_TEXT = "0.835";
      GPU_MEMORY_UTILIZATION_VISION = "0.80";
      MTP_NUM_TOKENS = "5";

      # Default reasoning effort for requests that don't ask for one.
      #
      # `low`, not `max`, and the gap is not subtle. Measured on this pair
      # 2026-08-21, HumanEval+ pass@1, greedy, 16k token cap:
      #
      #   thinking=max  0.665   53/164 truncated (32%)   mean 6293 tok
      #   thinking=low  0.933    4/164 truncated (2.4%)  mean 1389 tok
      #
      # At `max` the model falls into non-terminating reasoning loops: it pins
      # the token cap, the reasoning block never closes, so the parser routes
      # everything to `reasoning` and `content` comes back EMPTY. Those aren't
      # wrong answers, they're non-answers — of the 111 requests that did
      # return, 111/111 passed base HumanEval. So `max` costs 27 points of
      # pass@1 and 4.5x the tokens to produce a strictly worse service.
      #
      # Callers who want deeper reasoning can still pass reasoning_effort
      # per-request via chat_template_kwargs; this only sets the default.
      DEFAULT_THINKING = "low";

      # --- vLLM / kernel tuning, from the recipe's template ----------------
      DSPARK_MAX_INFLIGHT_PREFILLS = "2";
      DSPARK_ISSUE43_SCHED_DIAG = "0";
      VLLM_PREFIX_CACHE_RETENTION_INTERVAL = "4096";
      DSPARK_ENABLE_ASSISTANT_FINAL_HOTFIX = "0";
      DSPARK_ENABLE_ISSUE31_GPU_HOTFIX = "0";
      VLLM_USE_FLASHINFER_SAMPLER = "1";
      VLLM_USE_BREAKABLE_CUDAGRAPH = "0";
      VLLM_USE_B12X_MOE = "1";
      VLLM_B12X_W4A16_FORCE_BLOCKS_PER_SM = "0";
      VLLM_B12X_W4A16_FORCE_BLOCKS_MAX_M = "16";
      VLLM_B12X_W4A16_FORCE_TILE_CONFIG = "";
      VLLM_ALLOW_LONG_MAX_MODEL_LEN = "1";
      VLLM_SPARSE_INDEXER_MAX_LOGITS_MB = "256";
      VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS = "0";
      VLLM_EXECUTE_MODEL_TIMEOUT_SECONDS = "1800";
      CUTE_DSL_ARCH = "sm_121a";
      TORCH_CUDA_ARCH_LIST = "12.1a";
      FLASHINFER_CUDA_ARCH_LIST = "12.1a";
      FLASHINFER_DISABLE_VERSION_CHECK = "1";
      TILELANG_CLEANUP_TEMP_FILES = "1";
      TILELANG_CACHE_DIR = "/cache/huggingface/tilelang-cache";
      DG_JIT_USE_NVRTC = "0";
      DG_JIT_NVCC_COMPILER = "/usr/local/cuda/bin/nvcc";
      PYTORCH_CUDA_ALLOC_CONF = "expandable_segments:True";
      ENABLE_VL_SIDECAR = "0";
      PREPARE_VL_SIDECAR_MODEL = "0";
    };
  };
}
