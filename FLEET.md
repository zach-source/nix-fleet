# NixFleet LLM Infrastructure

## Fleet Overview

4x AMD Ryzen AI MAX+ 395 nodes (131GB unified VRAM each) running local LLM inference via llama.cpp with ROCm/HIP on Radeon 8060S iGPUs (gfx1151).

**Total fleet capacity: ~250 tok/s aggregate across 4 primary models.**

## Node Layout

### gtr-150 (192.168.3.133) — Multi-Role Hub

| Port | Model | Role | TG |
|------|-------|------|----|
| 8080 | Gemma 4 26B-A4B Q4_K_M | General + multimodal | 53 tok/s |
| 8090 | Nomic Embed v2 MoE Q8_0 | RAG embeddings (768d) | — |
| 8091 | ShieldGemma 2B Q8_0 | Safety filter | — |
| 8092 | Qwen2.5-Coder 1.5B Q8_0 | Code completion FIM | 107 tok/s |
| 8093 | Qwen3.5-9B Q4_K_M | Agent orchestrator | 33 tok/s |
| 8094 | Whisper base (en) | Speech-to-text (CPU) | — |

### gtr-151 (192.168.3.132) — Quality King

| Port | Model | Role | TG |
|------|-------|------|----|
| 8084 | Qwen3.5-35B-A3B + 0.8B draft | Best quality (SWE-bench 60%) | 32-60 tok/s |

Custom llama.cpp fork: [github.com/zach-source/llama.cpp](https://github.com/zach-source/llama.cpp/tree/qwen35-speculation)

### gtr-152 (192.168.3.134) — Throughput King

| Port | Model | Role | TG |
|------|-------|------|----|
| 8082 | Qwen3-Coder-30B-A3B + 0.6B draft | Fast coding | 95-124 tok/s |

### gtr-153 (192.168.3.130) — SOTA + Code Sandbox

| Port | Model | Role | TG |
|------|-------|------|----|
| 8082 | MiniMax-M2.7 229B IQ4_XS | SOTA 229B model | 23 tok/s |
| 2358 | Judge0 | Code execution (47 langs) | — |

## AI Gateway

**LiteLLM Proxy** at `https://llm.stigen.home` (k0s cluster)

```
Base URL: https://llm.stigen.home/v1
API Key:  sk-nixfleet-2026
```

### Model Names

| Name | Backend | Use Case |
|------|---------|----------|
| `qwen3.5-35b` | gtr-151:8084 | Quality reasoning |
| `qwen3-coder` | gtr-152:8082 | Fast coding |
| `gemma4` | gtr-150:8080 | General |
| `minimax-m27` | gtr-153:8082 | SOTA |
| `qwen-coder-1.5b` | gtr-150:8092 | Code completion |
| `qwen3.5-9b` | gtr-150:8093 | Orchestration |
| `shieldgemma` | gtr-150:8091 | Safety |
| `nomic-embed` | gtr-150:8090 | Embeddings |
| `gpt-4` | → qwen3.5-35b | OpenAI alias |
| `gpt-4o` | → qwen3-coder | OpenAI alias |
| `gpt-4o-mini` | → qwen-coder-1.5b | OpenAI alias |

## Chat UIs

| UI | URL | Notes |
|----|-----|-------|
| Open WebUI | `https://chat.stigen.home` | RAG, no auth |
| LibreChat | `https://libre.stigen.home` | MCP agents, register |
| LobeChat | `https://lobe.stigen.home` | Access code: `nixfleet` |

## Code Execution

Judge0 on gtr-153 via Docker Compose.

```bash
curl -X POST 'http://192.168.3.130:2358/submissions?wait=true' \
  -H "Content-Type: application/json" \
  -H "X-Auth-Token: nixfleet-judge0-auth" \
  -d '{"language_id":71,"source_code":"print(42)","stdin":""}'
```

47 languages: Python, C, C++, Rust, Go, Java, JavaScript, Bash, and more.

## SSH Access

All nodes use the `~/.ssh/nixfleet` key (ed25519, no passphrase, no 1Password dependency).

```bash
ssh gtr-150  # via ~/.ssh/nixfleet-hosts.conf
ssh gtr-151
ssh gtr-152
ssh gtr-153
```

## Benchmarks

### SWE-bench Lite (5 instances, thinking budget 2048)

| Model | Submitted | Resolved | Time |
|-------|-----------|----------|------|
| Qwen3.5-35B+spec | 4/5 | 3/5 (60%) | 17 min |
| Qwen3-Coder-30B+spec | 5/5 | 2/5 (40%) | 16 min |
| Gemma 4 26B-A4B | 3/5 | 1/5 (20%) | 68 min |

### TerminalBench 2 (5 tasks)

| Model | Score | Notes |
|-------|-------|-------|
| Qwen3.5-35B | 1/3 (33%) | Only model to solve LLM batching scheduler |
| Qwen3-Coder-30B | 0/5 | Fast but couldn't solve frontier tasks |

### Token Generation (short context)

| Model | TG (tok/s) |
|-------|-----------|
| Qwen3-Coder-30B+spec (warm) | 124 |
| Qwen3.5-35B+spec (warm) | 60 |
| Gemma 4 26B-A4B | 53 |
| MYTHOS 26B-A4B | 49 |
| Qwen3.5-9B | 33 |
| MiniMax-M2.7 229B | 23 |
| Qwen2.5-Coder 1.5B | 107 |

## Key Settings

All models use:
- `--cache-type-k q4_0 --cache-type-v q4_0` (3.6x KV compression)
- `--flash-attn on`
- `--reasoning-budget 2048` (capped thinking)
- `--jinja` (chat template)

## Repositories

| Repo | Purpose |
|------|---------|
| `nixfleet` | Source code, Nix modules, host configs |
| `nix-fleet-hosts` | Flux-watched K8s manifests (LiteLLM, chat UIs) |
| `zach-source/llama.cpp` | Fork with Qwen3.5 hybrid speculation fixes |
