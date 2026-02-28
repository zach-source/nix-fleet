# Agent Secrets Setup Guide

Step-by-step instructions for creating all secrets needed by the NixFleet agent fleet. Each agent gets a dedicated 1Password item in the **"Personal Agents"** vault, synced to Kubernetes via the 1Password Connect operator.

## Agent Secret Matrix

| Secret | Lena | Marcus | Sophie | Ada | Kai | Nora | Rex |
|--------|:----:|:------:|:------:|:---:|:---:|:----:|:---:|
| `OPENAI_API_KEY` | x | x | x | x | x | x | x |
| `GITHUB_TOKEN` | x | x | x | | | | x |
| `TELEGRAM_BOT_TOKEN` | x | | x | x | x | x | |
| `TELEGRAM_OWNER_ID` | x | | x | x | x | x | |
| `SLACK_BOT_TOKEN` | x | x | x | | x | x | x |
| `SLACK_APP_TOKEN` | x | x | x | | x | x | x |
| `OPENCLAW_GATEWAY_TOKEN` | x | x | x | x | x | x | x |
| Google OAuth creds | | | | x | | | |

---

## 1. OpenAI API Key

All agents need this. You can reuse the same key across agents or create one per agent for usage tracking.

### Steps

1. Go to https://platform.openai.com/api-keys
2. Click **"Create new secret key"**
3. Name it (e.g. `nixfleet-agents` or per-agent like `nixfleet-lena`)
4. Copy the key — it won't be shown again

### Pro Subscription (GPT-5 / Codex)

The agents are configured to use `openai/gpt-5.1` with `openai/gpt-5-mini` fallback. ZAI agents (coder, architect, sre) use `zai/glm-5` primary. Code-focused agents (sage, code-review) use `openai/gpt-5.3-codex` primary. Make sure your accounts have access to these models.

### 1Password Field

- **Field name**: `OPENAI_API_KEY`
- **Value**: `sk-...`

---

## 2. GitHub Fine-Grained PAT

Needed by: **Lena** (code-review), **Marcus** (pm), **Sophie** (marketing), **Rex** (security).

### Steps

1. Go to https://github.com/settings/tokens?type=beta
2. Click **"Generate new token"**
3. Configure:
   - **Token name**: `nixfleet-<agent>` (e.g. `nixfleet-lena`)
   - **Expiration**: 90 days (set a calendar reminder to rotate)
   - **Repository access**: Select the repos each agent should access
   - **Permissions**:

| Agent | Permissions Needed |
|-------|--------------------|
| Lena (code-review) | Contents: Read, Pull requests: Read & Write, Issues: Read |
| Marcus (pm) | Issues: Read & Write, Pull requests: Read, Projects: Read & Write |
| Sophie (marketing) | Contents: Read, Releases: Read |
| Rex (security) | Contents: Read, Vulnerability alerts: Read, Security advisories: Read, Dependabot alerts: Read |

4. Click **"Generate token"**
5. Copy the token

### Shared vs Separate Tokens

You can use one token with combined permissions, or create separate tokens per agent for least-privilege isolation. Separate tokens are recommended for production.

### 1Password Field

- **Field name**: `GITHUB_TOKEN`
- **Value**: `github_pat_...`

---

## 3. Telegram Bot Tokens

Needed by: **Lena**, **Sophie**, **Ada**, **Kai**, **Nora**. Each agent gets its own Telegram bot.

### Create a Bot

Repeat for each agent that uses Telegram:

1. Open Telegram and message [@BotFather](https://t.me/BotFather)
2. Send `/newbot`
3. Enter a display name (e.g. `Lena Code Review`)
4. Enter a username — must end in `bot` (e.g. `nixfleet_lena_bot`)
5. BotFather replies with the **bot token** — copy it

### Get Your Telegram Owner ID

Each agent's `openclaw.json` uses `TELEGRAM_OWNER_ID` in the allowlist so only you can message the bot.

1. Message [@userinfobot](https://t.me/userinfobot) on Telegram
2. It replies with your user ID (a number like `123456789`)
3. The value to store is `tg:123456789` (with the `tg:` prefix)

### Recommended Bot Names

| Agent | Bot Display Name | Bot Username (example) |
|-------|-----------------|------------------------|
| Lena | Lena Code Review | `nixfleet_lena_bot` |
| Sophie | Sophie Marketing | `nixfleet_sophie_bot` |
| Ada | Ada Assistant | `nixfleet_ada_bot` |
| Kai | Kai DevOps | `nixfleet_kai_bot` |
| Nora | Nora Research | `nixfleet_nora_bot` |

### 1Password Fields

- **Field name**: `TELEGRAM_BOT_TOKEN`
- **Value**: `123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11`
- **Field name**: `TELEGRAM_OWNER_ID`
- **Value**: `tg:123456789`

---

## 4. Slack App (Socket Mode)

Needed by: **Lena**, **Marcus**, **Sophie**, **Kai**, **Nora**, **Rex**. You can create one Slack app shared across agents (each gets its own tokens), or one app per agent.

### Create a Slack App

1. Go to https://api.slack.com/apps
2. Click **"Create New App"** > **"From scratch"**
3. Name it (e.g. `NixFleet Agents` or per-agent like `Lena`)
4. Select your workspace
5. Click **"Create App"**

### Enable Socket Mode

1. In the app settings, go to **"Socket Mode"** (left sidebar)
2. Toggle **"Enable Socket Mode"** on
3. Create an app-level token:
   - Name: `socket-token`
   - Scope: `connections:write`
4. Click **"Generate"** — copy this token (starts with `xapp-`)
5. This is your `SLACK_APP_TOKEN`

### Configure Bot Token Scopes

1. Go to **"OAuth & Permissions"** (left sidebar)
2. Under **"Bot Token Scopes"**, add:

| Scope | Purpose |
|-------|---------|
| `chat:write` | Send messages |
| `channels:read` | List channels |
| `channels:history` | Read channel messages |
| `groups:read` | List private channels |
| `groups:history` | Read private channel messages |
| `im:read` | List DMs |
| `im:history` | Read DMs |
| `im:write` | Open DMs |
| `users:read` | Look up user info |

3. Click **"Install to Workspace"** (or "Reinstall" if already installed)
4. Authorize the app
5. Copy the **"Bot User OAuth Token"** (starts with `xoxb-`)
6. This is your `SLACK_BOT_TOKEN`

### Enable Events (optional but recommended)

1. Go to **"Event Subscriptions"** (left sidebar)
2. Toggle on
3. Subscribe to bot events: `message.channels`, `message.groups`, `message.im`, `app_mention`

### Per-Agent or Shared App

**Shared app** (simpler): One Slack app, install once, use the same `xoxb-` and `xapp-` tokens for all agents. All agents appear as the same bot in Slack.

**Per-agent apps** (recommended): Create separate Slack apps per agent. Each agent has its own bot identity in Slack, so you can tell who's responding. More setup but better UX.

### 1Password Fields

- **Field name**: `SLACK_BOT_TOKEN`
- **Value**: `xoxb-...`
- **Field name**: `SLACK_APP_TOKEN`
- **Value**: `xapp-...`

---

## 5. Google OAuth (Ada only)

Ada uses the `gog` skill for Gmail and Google Calendar.

### Create Google Cloud Project

1. Go to https://console.cloud.google.com/
2. Create a new project (e.g. `nixfleet-ada`)
3. Enable these APIs:
   - **Gmail API**: https://console.cloud.google.com/apis/library/gmail.googleapis.com
   - **Google Calendar API**: https://console.cloud.google.com/apis/library/calendar-json.googleapis.com

### Create OAuth Credentials

1. Go to **"APIs & Services"** > **"Credentials"**
2. Click **"Create Credentials"** > **"OAuth client ID"**
3. If prompted, configure the **OAuth consent screen** first:
   - User type: **External** (or Internal if using Google Workspace)
   - App name: `NixFleet Ada`
   - Add your email as a test user
4. Back in Credentials, choose:
   - Application type: **Desktop app** (or Web application)
   - Name: `nixfleet-ada`
5. Click **"Create"**
6. Download the JSON file — it contains `client_id` and `client_secret`

### Authorize via gog CLI

The `gog` skill handles OAuth token exchange. On first run, Ada will prompt you (via Telegram) with an authorization URL:

1. Open the URL in your browser
2. Sign in with the Google account to manage
3. Grant permissions for Gmail and Calendar
4. The refresh token is stored in Ada's workspace (emptyDir volume)

**Note**: If the pod restarts, you may need to re-authorize. For persistence, consider backing up the OAuth refresh token to 1Password and mounting it.

### 1Password Fields

Store these if you want to pre-configure OAuth (otherwise `gog` handles it interactively):

- **Field name**: `GOOGLE_CLIENT_ID`
- **Value**: `123456-abc.apps.googleusercontent.com`
- **Field name**: `GOOGLE_CLIENT_SECRET`
- **Value**: `GOCSPX-...`

---

## 6. OpenClaw Gateway Token

Every agent needs a gateway token for the OpenClaw API. Generate a random token for each agent.

### Generate

```bash
# Generate one per agent
for agent in lena marcus sophie ada kai nora rex; do
  echo "$agent: $(openssl rand -hex 32)"
done
```

### 1Password Field

- **Field name**: `OPENCLAW_GATEWAY_TOKEN`
- **Value**: (64-character hex string)

---

## 7. Create 1Password Items

Now create the items in the **"Personal Agents"** vault. If the vault doesn't exist yet, create it first.

### Create the Vault

1. Open 1Password
2. Go to **Vaults** (sidebar)
3. Click **"New Vault"**
4. Name it **`Personal Agents`**
5. Make sure the 1Password Connect server has access to this vault

### Create Items

Create one **Login** or **Secure Note** item per agent with the fields listed below. The item name must match what's in the `onepassword-item.yaml` manifests.

#### Agent Code Review (Lena)

| Field | Value |
|-------|-------|
| `OPENAI_API_KEY` | `sk-...` |
| `GITHUB_TOKEN` | `github_pat_...` |
| `TELEGRAM_BOT_TOKEN` | `123456:ABC...` |
| `TELEGRAM_OWNER_ID` | `tg:123456789` |
| `SLACK_BOT_TOKEN` | `xoxb-...` |
| `SLACK_APP_TOKEN` | `xapp-...` |
| `OPENCLAW_GATEWAY_TOKEN` | (random hex) |

#### Agent PM (Marcus)

| Field | Value |
|-------|-------|
| `OPENAI_API_KEY` | `sk-...` |
| `GITHUB_TOKEN` | `github_pat_...` |
| `SLACK_BOT_TOKEN` | `xoxb-...` |
| `SLACK_APP_TOKEN` | `xapp-...` |
| `OPENCLAW_GATEWAY_TOKEN` | (random hex) |

#### Agent Marketing (Sophie)

| Field | Value |
|-------|-------|
| `OPENAI_API_KEY` | `sk-...` |
| `GITHUB_TOKEN` | `github_pat_...` |
| `TELEGRAM_BOT_TOKEN` | `123456:ABC...` |
| `TELEGRAM_OWNER_ID` | `tg:123456789` |
| `SLACK_BOT_TOKEN` | `xoxb-...` |
| `SLACK_APP_TOKEN` | `xapp-...` |
| `OPENCLAW_GATEWAY_TOKEN` | (random hex) |

#### Agent Personal (Ada)

| Field | Value |
|-------|-------|
| `OPENAI_API_KEY` | `sk-...` |
| `TELEGRAM_BOT_TOKEN` | `123456:ABC...` |
| `TELEGRAM_OWNER_ID` | `tg:123456789` |
| `GOOGLE_CLIENT_ID` | `123456-abc.apps.googleusercontent.com` |
| `GOOGLE_CLIENT_SECRET` | `GOCSPX-...` |
| `OPENCLAW_GATEWAY_TOKEN` | (random hex) |

#### Agent DevOps (Kai)

| Field | Value |
|-------|-------|
| `OPENAI_API_KEY` | `sk-...` |
| `TELEGRAM_BOT_TOKEN` | `123456:ABC...` |
| `TELEGRAM_OWNER_ID` | `tg:123456789` |
| `SLACK_BOT_TOKEN` | `xoxb-...` |
| `SLACK_APP_TOKEN` | `xapp-...` |
| `OPENCLAW_GATEWAY_TOKEN` | (random hex) |

#### Agent Research (Nora)

| Field | Value |
|-------|-------|
| `OPENAI_API_KEY` | `sk-...` |
| `TELEGRAM_BOT_TOKEN` | `123456:ABC...` |
| `TELEGRAM_OWNER_ID` | `tg:123456789` |
| `SLACK_BOT_TOKEN` | `xoxb-...` |
| `SLACK_APP_TOKEN` | `xapp-...` |
| `OPENCLAW_GATEWAY_TOKEN` | (random hex) |

#### Agent Security (Rex)

| Field | Value |
|-------|-------|
| `OPENAI_API_KEY` | `sk-...` |
| `GITHUB_TOKEN` | `github_pat_...` |
| `SLACK_BOT_TOKEN` | `xoxb-...` |
| `SLACK_APP_TOKEN` | `xapp-...` |
| `OPENCLAW_GATEWAY_TOKEN` | (random hex) |

---

## 8. Verify Secrets are Syncing

After creating the 1Password items and deploying the Flux manifests:

```bash
# Check that OnePasswordItem CRDs are reconciled
kubectl get onepassworditems -A

# Verify secrets exist in each namespace
for ns in agent-code-review agent-pm agent-marketing agent-personal agent-devops agent-research agent-security; do
  echo "=== $ns ==="
  kubectl get secret -n $ns
done

# Check a specific secret has the expected keys
kubectl get secret agent-code-review-secrets -n agent-code-review -o jsonpath='{.data}' | jq -r 'keys[]'
```

Expected keys for Lena (code-review):
```
GITHUB_TOKEN
OPENCLAW_GATEWAY_TOKEN
OPENAI_API_KEY
SLACK_APP_TOKEN
SLACK_BOT_TOKEN
TELEGRAM_BOT_TOKEN
TELEGRAM_OWNER_ID
```

---

## Secret Rotation

### Schedule

| Secret | Rotation Period | Notes |
|--------|----------------|-------|
| `OPENAI_API_KEY` | 90 days | Rotate in OpenAI dashboard, update 1Password |
| `GITHUB_TOKEN` | 90 days | Fine-grained PATs have configurable expiry |
| `TELEGRAM_BOT_TOKEN` | Rarely | Only if compromised — revoke via BotFather `/revoke` |
| `SLACK_BOT_TOKEN` | Rarely | Regenerate in Slack app settings if compromised |
| `SLACK_APP_TOKEN` | Rarely | Regenerate in Socket Mode settings if compromised |
| `GOOGLE_CLIENT_SECRET` | Rarely | Rotate in Google Cloud Console if compromised |
| `OPENCLAW_GATEWAY_TOKEN` | 90 days | Regenerate with `openssl rand -hex 32` |

### Rotation Process

1. Generate or obtain the new secret value
2. Update the field in the 1Password item
3. 1Password Connect automatically syncs to Kubernetes (within polling interval)
4. Pods pick up new env vars on next restart:
   ```bash
   kubectl rollout restart deployment -n agent-<name> agent-<name>
   ```
