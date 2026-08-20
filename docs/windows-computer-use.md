# Windows Computer-Use for Open WebUI — Design / Options

**Status:** planning (2026-07-12). No Windows target committed yet. This doc lays
out the tool choices, where the Windows machine could live, how it wires into
Open WebUI, and the security model — so we can pick a path.

## Goal

Let an OWUI model (via litellm) drive a **Windows machine** — click, type,
screenshot, run PowerShell, control apps — the same way the just-deployed
Playwright tool drives a browser. Same wiring pattern: a tool server the model
calls; OWUI reaches it by URL.

## The automation tool (runs ON the Windows box)

| Tool | Mechanism | Notes |
|------|-----------|-------|
| **Windows-MCP** (`CursorTouch/Windows-MCP`) | UI Automation tree + Win32 `SendInput` + screenshots | **Recommended.** Works with any LLM, no vision model required; ~0.2–0.5s/action; one-command `uvx` install. Broadest general coverage (click/type/scroll/hotkeys/PowerShell/registry). |
| `sbroenne/mcp-windows` | UI Automation, targets elements **by name** | Most *reliable* app control (DPI/theme/resolution-independent, no coordinates). Great when you know the app; narrower than Windows-MCP. |
| `claude-did-this/MCPControl` | OS-level automation | Similar scope to Windows-MCP. |
| `AB498/computer-control-mcp` | PyAutoGUI + RapidOCR (onnx) | Zero external deps, Anthropic-computer-use-like (coordinates + OCR). Good if you want a pure vision/OCR loop. |

All are **stdio MCP servers** → they need a bridge to be reachable over the
network (see wiring). Recommendation: **Windows-MCP** as the default; add
`mcp-windows` later if we need rock-solid by-name control of specific apps.

## Where the Windows machine lives (pick one)

| Option | Effort | Notes |
|--------|--------|-------|
| **A. Existing Windows box on the LAN** | Low | Fastest PoC. Install Windows-MCP + mcpo on it, expose to OWUI. Downside: the model gets full control of a *real* machine — only do this on a throwaway/non-sensitive box. |
| **B. Dedicated Windows VM on a hypervisor** (Proxmox/ESXi/Hyper-V) | Medium | **Recommended for anything ongoing.** Snapshot + revert, network-segment it, treat as disposable. Standard Windows tooling; no k8s complexity. Needs a Windows license + ISO. |
| **C. KubeVirt Windows VM in k0s** | High | Keeps it "in cluster," but heavy: KVM on the nodes, virtio drivers, Windows licensing, lots of RAM, PVC-backed disk (note: iSCSI/NAS is currently down). Only worth it if you specifically want VMs managed by k8s. |

GPU is **not** needed for UI automation, so any of these can be a small VM
(2 vCPU / 4–8 GB). For B/C you'll need a Windows license + ISO and (for C) the
virtio-win drivers.

## Wiring into Open WebUI (mirrors the Playwright setup)

Windows-MCP is stdio, so bridge it with **mcpo** (MCP→OpenAPI) or a
streamable-HTTP transport, then point OWUI at it:

```
Windows box/VM
  ├─ Windows-MCP (stdio MCP)              ← drives the desktop
  └─ mcpo  --api-key <KEY> -- windows-mcp ← exposes OpenAPI on :8000
        │  (bind to LAN IP or a tunnel)
        ▼
Open WebUI  TOOL_SERVER_CONNECTIONS
  [{ "type":"openapi", "url":"https://<win-host>:8000",
     "auth_type":"bearer", "key":"<KEY>", "config":{"enable":true} }]
```

Because the tool server lives **outside** the cluster (unlike open-terminal /
playwright which are in-cluster pods), secure the hop:
- **Bearer key** on mcpo (`--api-key`), and **TLS** — either a Cloudflare
  Tunnel (like `op.nixfleet.private`) or a direct TLS cert. Don't expose the
  raw mcpo port un-authenticated.
- Network path: OWUI runs in-cluster on gti; it must be able to reach the
  Windows host (LAN reachable, or via the tunnel). Same reachability model as
  the gtr llama-server backends.

If a tool exposes native **streamable-HTTP MCP**, OWUI can connect directly
(`type:"mcp"`) and skip mcpo — check the chosen tool's transports first
(Windows-MCP is stdio today, so mcpo is the safe assumption).

## Security model (do NOT skip)

Computer-use = the model can do **anything a logged-in user can** on that box.
- Use a **dedicated, disposable** Windows instance (Option B/C, or a throwaway
  box for A). **Snapshot before, revert after.**
- **No sensitive creds / SSO / domain join / saved logins** on it. Network-
  segment it (no lateral access to the fleet or NAS).
- Bearer key + TLS on the tool endpoint; never expose it unauthenticated.
- Consider **human-in-the-loop approval** for destructive actions (OWUI tool
  approval), and scope what the automation account can do (non-admin where
  possible — though many automation tasks want admin, which is exactly why the
  box should be disposable).
- Log actions (screenshots/commands) for audit.

## Recommended path

1. **PoC:** if there's a spare Windows box, install Windows-MCP + mcpo (bearer
   key), expose to OWUI as an OpenAPI tool server. Validate the loop end-to-end.
2. **Ongoing:** stand up a **dedicated snapshotted Windows VM** (Option B on a
   hypervisor is the least friction; C/KubeVirt only if you want it k8s-managed
   — and revisit once the NAS/iSCSI is healthy for VM disk).
3. Wire it exactly like the Playwright tool (this repo's `open-webui`
   deployment `TOOL_SERVER_CONNECTIONS`), just pointing at the external,
   TLS+bearer-protected mcpo endpoint.

## Open decisions for you

- Is there an existing Windows box to use (PoC), or do we provision a VM (B vs C)?
- Windows license/ISO availability?
- Reachability: LAN-direct or via a Cloudflare tunnel (like the other private hosts)?

## References

- Windows-MCP — https://github.com/CursorTouch/Windows-MCP
- mcp-windows (by-name UI Automation) — https://github.com/sbroenne/mcp-windows
- MCPControl — https://github.com/claude-did-this/MCPControl
- computer-control-mcp (PyAutoGUI+OCR) — https://github.com/AB498/computer-control-mcp
- mcpo (MCP→OpenAPI) — https://github.com/open-webui/mcpo
