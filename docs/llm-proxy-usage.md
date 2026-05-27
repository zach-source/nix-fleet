# Using the NixFleet LLM Proxy (LiteLLM)

The fleet runs a [LiteLLM](https://docs.litellm.ai/) proxy that presents every
self-hosted model behind one OpenAI-compatible API. Point any OpenAI SDK / client
at it and select a model by name.

> **Source of truth:** the model list lives in
> `flux/apps/overlays/nixfleet/litellm/config.yaml` (in the `nix-fleet-hosts`
> repo). This doc was last validated **2026-05-27** against that config.

## 1. Endpoints & auth

| Calling from | Base URL | Headers required |
|---|---|---|
| **In-cluster** (k0s pods) | `http://litellm.litellm.svc.cluster.local:4000/v1` | `Authorization: Bearer sk-nixfleet-2026` |
| **External** (laptop, bare-metal, anywhere) | `https://llm.nixfleet.private.stigen.ai/v1` | `Authorization: Bearer sk-nixfleet-2026` **plus** Cloudflare Access |

External traffic goes through a Cloudflare Tunnel guarded by CF Access
(`*.nixfleet.private.stigen.ai`, allows `@stigen.ai` / `@stigen.io`). A browser
gets an SSO login; programmatic clients must send a CF Access **service token**:

```bash
ID=$(op read "op://Personal Agents/op-connect-cf-access/client_id")
SEC=$(op read "op://Personal Agents/op-connect-cf-access/client_secret")
# add to every request:
#   -H "CF-Access-Client-Id: $ID" -H "CF-Access-Client-Secret: $SEC"
```

The master key `sk-nixfleet-2026` is the LiteLLM virtual key (defined in
`general_settings.master_key`).

## 2. Model reference

| Model name | Type | What it is | Host | Notes |
|---|---|---|---|---|
| **`qwen3-coder`** | chat | Qwen3-Coder-30B, ~95–124 tok/s | gtr-152 | **Recommended default** — fast & reliable |
| `qwopus` | chat | Qwopus3.5-9B-Coder | gtr-153 | small/fast coder |
| `gemma4` | chat (**vision**) | Gemma 4 26B-A4B, multimodal | gtr-150 | accepts image input |
| `qwen3.5-9b` | chat | Qwen3.5-9B | gtr-150 | general |
| `qwen-coder-1.5b` | chat | Qwen2.5-Coder-1.5B | gtr-150 | tiny/fast (titles, classification) |
| `qwen3.6-35b` | chat | Qwen3.6-35B-A3B MoE, quality king | gtr-151 | ⚠️ wedge-prone / node may be down |
| `qwen3.6-27b` | chat | Qwen3.6-27B dense + MTP | gtr-151 | ⚠️ same caveat |
| `qwen3.5-35b` | chat | alias → `qwen3.6-35b` | gtr-151 | back-compat |
| `nomic-embed` | embedding | nomic-embed-text-v2, **768-dim** | gtr-150 | default embedder |
| `qwen3-embedding` | embedding | Qwen3-Embedding-8B, **4096-dim**, #1 MTEB | gtr-150 | SOTA; not vector-compatible with 768-dim |
| `qwen3-reranker` | rerank | Qwen3-Reranker-8B, **normalized 0–1** | gtr-150 | SOTA reranker |
| `jina-reranker-v2` | rerank | Jina v2 cross-encoder, **raw logits** | gtr-150 | rank order valid; scores not 0–1 |
| `shieldgemma` | safety | ShieldGemma-2B | gtr-150 | also auto-runs as a pre-call guardrail on all chat |
| `gpt-4` / `gpt-4o` / `gpt-4o-mini` | chat | aliases → `qwen3.6-35b` / `qwen3-coder` / `qwen-coder-1.5b` | — | drop-in for OpenAI-named code |
| `text-embedding-3-small` | embedding | alias → `nomic-embed` (768-dim) | — | drop-in |

List the live names anytime:

```bash
curl -s http://litellm.litellm.svc.cluster.local:4000/v1/models \
  -H "Authorization: Bearer sk-nixfleet-2026" | jq -r '.data[].id'
```

## 3. Chat completions — `/v1/chat/completions`

**curl (in-cluster):**

```bash
curl -s http://litellm.litellm.svc.cluster.local:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-nixfleet-2026" -H "Content-Type: application/json" \
  -d '{"model":"qwen3-coder","messages":[{"role":"user","content":"Write a bash one-liner to count files"}]}'
```

**Python (OpenAI SDK)** — change `model` to use any chat model:

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://litellm.litellm.svc.cluster.local:4000/v1",
    api_key="sk-nixfleet-2026",
)
r = client.chat.completions.create(
    model="qwen3-coder",                 # or qwopus, gemma4, qwen3.6-35b, …
    messages=[{"role": "user", "content": "hello"}],
    temperature=0.2,
)
print(r.choices[0].message.content)
```

**External** (same SDK, add the CF Access headers):

```python
client = OpenAI(
    base_url="https://llm.nixfleet.private.stigen.ai/v1",
    api_key="sk-nixfleet-2026",
    default_headers={"CF-Access-Client-Id": ID, "CF-Access-Client-Secret": SEC},
)
```

**Vision (`gemma4` only):**

```python
client.chat.completions.create(model="gemma4", messages=[{"role": "user", "content": [
    {"type": "text", "text": "What's in this image?"},
    {"type": "image_url", "image_url": {"url": "data:image/png;base64,<...>"}},
]}])
```

## 4. Embeddings — `/v1/embeddings`

```python
e = client.embeddings.create(model="nomic-embed", input=["first doc", "second doc"])
print(len(e.data[0].embedding))   # 768 (nomic-embed) | 4096 (qwen3-embedding)
```

```bash
curl -s http://litellm.litellm.svc.cluster.local:4000/v1/embeddings \
  -H "Authorization: Bearer sk-nixfleet-2026" -H "Content-Type: application/json" \
  -d '{"model":"qwen3-embedding","input":"text to embed"}'
```

> ⚠️ Don't mix embedding dimensions in one vector store — `nomic-embed` (768) and
> `qwen3-embedding` (4096) are not interchangeable. Re-embed a collection if you
> switch models.

## 5. Reranking — `/v1/rerank` (Cohere-style)

```bash
curl -s http://litellm.litellm.svc.cluster.local:4000/v1/rerank \
  -H "Authorization: Bearer sk-nixfleet-2026" -H "Content-Type: application/json" \
  -d '{"model":"qwen3-reranker","query":"capital of France",
       "documents":["Paris is the capital of France.","Berlin is in Germany."],
       "top_n":2}'
# -> {"results":[{"index":0,"relevance_score":0.98},{"index":1,"relevance_score":0.03}]}
```

Use `qwen3-reranker` for normalized 0–1 scores. `jina-reranker-v2` returns raw
cross-encoder logits — rank order is valid, but magnitudes are not 0–1.

## 6. Gotchas

- **Safety guardrail runs automatically** (`shieldgemma`, pre-call) on all chat
  requests — flagged prompts are blocked before reaching the model.
- **`qwen3.6-35b` / `qwen3.6-27b` (gtr-151)** are highest-quality but least
  reliable (MoE wedges, node may be offline). Default to `qwen3-coder` for
  anything automated; reach for gtr-151 models only when you need max quality.
- `drop_params: true` is set — unsupported params are silently dropped rather
  than erroring. `num_retries: 2`, request timeout 300s.
- Embeddings route through the `hosted_vllm/` provider (not `openai/`) so
  `encoding_format` is handled correctly for the llama.cpp backends; you don't
  need to do anything special as a client.
