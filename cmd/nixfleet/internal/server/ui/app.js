// NixFleet Dashboard JavaScript

const API_BASE = "/api";
let refreshInterval = null;

// Initialize dashboard
document.addEventListener("DOMContentLoaded", () => {
  refreshAll();
  // Auto-refresh every 30 seconds
  refreshInterval = setInterval(refreshAll, 30000);
});

// Refresh all data
async function refreshAll() {
  await Promise.all([loadHosts(), loadActivity()]);
}

// Load hosts list
async function loadHosts() {
  const grid = document.getElementById("hosts-grid");

  try {
    const response = await fetch(`${API_BASE}/hosts`);
    if (!response.ok) throw new Error("Failed to fetch hosts");

    const data = await response.json();
    const hosts = data.hosts || [];

    // Update stats
    document.getElementById("total-hosts").textContent = hosts.length;

    let healthy = 0,
      drifted = 0,
      pullMode = 0;

    // Render host cards
    grid.innerHTML = hosts
      .map((host) => {
        const status = getHostStatus(host);
        if (status === "healthy") healthy++;
        if (status === "drifted") drifted++;
        if (host.pull_mode) pullMode++;

        return renderHostCard(host, status);
      })
      .join("");

    document.getElementById("healthy-hosts").textContent = healthy;
    document.getElementById("drifted-hosts").textContent = drifted;
    document.getElementById("pull-mode-hosts").textContent = pullMode;

    if (hosts.length === 0) {
      grid.innerHTML = '<div class="loading">No hosts configured</div>';
    }
  } catch (error) {
    console.error("Error loading hosts:", error);
    grid.innerHTML = `<div class="loading">Error loading hosts: ${error.message}</div>`;
  }
}

// Determine host status
function getHostStatus(host) {
  if (host.error) return "error";
  if (host.drift_detected) return "drifted";
  if (host.last_health_check && !host.healthy) return "error";
  if (host.healthy || host.last_apply) return "healthy";
  return "unknown";
}

// Render a host card
function renderHostCard(host, status) {
  const pullModeBadge = host.pull_mode
    ? '<span class="pull-mode-badge">Pull Mode</span>'
    : "";

  const lastApply = host.last_apply ? formatTime(host.last_apply) : "Never";

  const lastCheck = host.last_drift_check
    ? formatTime(host.last_drift_check)
    : "Never";

  return `
        <div class="host-card" data-host="${host.name}">
            <div class="host-card-header">
                <div>
                    <div class="host-name">${host.name}</div>
                    <div class="host-addr">${host.addr || "Unknown"}</div>
                </div>
                <div>
                    <span class="status-badge status-${status}">${status}</span>
                    ${pullModeBadge}
                </div>
            </div>
            <div class="host-info">
                <div class="info-item">
                    <span class="info-label">Base</span>
                    <span class="info-value">${host.base || "Unknown"}</span>
                </div>
                <div class="info-item">
                    <span class="info-label">Generation</span>
                    <span class="info-value">${host.generation || "-"}</span>
                </div>
                <div class="info-item">
                    <span class="info-label">Last Apply</span>
                    <span class="info-value">${lastApply}</span>
                </div>
                <div class="info-item">
                    <span class="info-label">Last Drift Check</span>
                    <span class="info-value">${lastCheck}</span>
                </div>
            </div>
            <div class="host-actions">
                <button onclick="triggerApply('${host.name}')" class="btn btn-primary btn-small">Apply</button>
                <button onclick="checkDrift('${host.name}')" class="btn btn-secondary btn-small">Check Drift</button>
                ${host.pull_mode ? `<button onclick="triggerPull('${host.name}')" class="btn btn-secondary btn-small">Trigger Pull</button>` : ""}
                <button onclick="viewDetails('${host.name}')" class="btn btn-secondary btn-small">Details</button>
            </div>
        </div>
    `;
}

// Format timestamp
function formatTime(timestamp) {
  if (!timestamp) return "Never";
  const date = new Date(timestamp);
  const now = new Date();
  const diff = now - date;

  if (diff < 60000) return "Just now";
  if (diff < 3600000) return `${Math.floor(diff / 60000)}m ago`;
  if (diff < 86400000) return `${Math.floor(diff / 3600000)}h ago`;
  return date.toLocaleDateString();
}

// Trigger apply on a host
async function triggerApply(hostName) {
  if (!confirm(`Apply configuration to ${hostName}?`)) return;

  addActivity(`Triggering apply on ${hostName}...`);

  try {
    const response = await fetch(`${API_BASE}/hosts/${hostName}/apply`, {
      method: "POST",
    });

    if (!response.ok) throw new Error("Apply failed");

    const result = await response.json();
    addActivity(`Apply on ${hostName}: ${result.status || "started"}`);

    // Refresh after a short delay
    setTimeout(refreshAll, 2000);
  } catch (error) {
    addActivity(`Apply on ${hostName} failed: ${error.message}`, "error");
  }
}

// Check drift on a host
async function checkDrift(hostName) {
  addActivity(`Checking drift on ${hostName}...`);

  try {
    const response = await fetch(`${API_BASE}/drift/check?host=${hostName}`, {
      method: "POST",
    });

    if (!response.ok) throw new Error("Drift check failed");

    const result = await response.json();
    const status = result.drifted ? "drift detected" : "in sync";
    addActivity(`Drift check on ${hostName}: ${status}`);

    refreshAll();
  } catch (error) {
    addActivity(`Drift check on ${hostName} failed: ${error.message}`, "error");
  }
}

// Check drift on all hosts
async function checkAllDrift() {
  addActivity("Checking drift on all hosts...");

  try {
    const response = await fetch(`${API_BASE}/drift/check`, {
      method: "POST",
    });

    if (!response.ok) throw new Error("Drift check failed");

    addActivity("Drift check completed on all hosts");
    refreshAll();
  } catch (error) {
    addActivity(`Drift check failed: ${error.message}`, "error");
  }
}

// Trigger pull on a host
async function triggerPull(hostName) {
  addActivity(`Triggering pull on ${hostName}...`);

  try {
    const response = await fetch(`${API_BASE}/pull-mode/${hostName}/trigger`, {
      method: "POST",
    });

    if (!response.ok) throw new Error("Pull trigger failed");

    addActivity(`Pull triggered on ${hostName}`);

    // Refresh after a short delay
    setTimeout(refreshAll, 5000);
  } catch (error) {
    addActivity(
      `Pull trigger on ${hostName} failed: ${error.message}`,
      "error",
    );
  }
}

// View host details
async function viewDetails(hostName) {
  const modal = document.getElementById("modal");
  const title = document.getElementById("modal-title");
  const body = document.getElementById("modal-body");

  title.textContent = `Host: ${hostName}`;
  body.innerHTML = '<div class="loading">Loading details...</div>';
  modal.classList.remove("hidden");

  try {
    // Fetch host details and OS info in parallel
    const [hostResponse, osInfoResponse] = await Promise.all([
      fetch(`${API_BASE}/hosts/${hostName}`),
      fetch(`${API_BASE}/hosts/${hostName}/os-info`),
    ]);

    if (!hostResponse.ok) throw new Error("Failed to fetch details");

    const host = await hostResponse.json();
    const osInfo = osInfoResponse.ok ? await osInfoResponse.json() : null;

    const pullModeInfo = host.pull_mode_status
      ? `
                <div class="info-item">
                    <span class="info-label">Timer Active</span>
                    <span class="info-value">${host.pull_mode_status.timer_active ? "Yes" : "No"}</span>
                </div>
                <div class="info-item">
                    <span class="info-label">Last Pull</span>
                    <span class="info-value">${host.pull_mode_status.last_run || "Never"}</span>
                </div>
                <div class="info-item">
                    <span class="info-label">Last Result</span>
                    <span class="info-value">${host.pull_mode_status.last_result || "-"}</span>
                </div>
                <div class="info-item">
                    <span class="info-label">Next Pull</span>
                    <span class="info-value">${host.pull_mode_status.next_run || "-"}</span>
                </div>
                <div class="info-item">
                    <span class="info-label">Current Commit</span>
                    <span class="info-value" style="font-family: monospace;">${host.pull_mode_status.current_commit || "-"}</span>
                </div>
            `
      : "";

    const rolesHtml = host.roles
      ? `<div class="info-item">
                    <span class="info-label">Roles</span>
                    <span class="info-value">${host.roles.join(", ")}</span>
                </div>`
      : "";

    const osInfoHtml = osInfo
      ? `
            <h4 style="margin-top: 1.5rem; margin-bottom: 0.5rem; color: var(--accent-blue);">Operating System</h4>
            <div class="host-info" style="grid-template-columns: 1fr 1fr;">
                <div class="info-item">
                    <span class="info-label">OS</span>
                    <span class="info-value">${osInfo.pretty_name || osInfo.name || "-"}</span>
                </div>
                <div class="info-item">
                    <span class="info-label">Kernel</span>
                    <span class="info-value" style="font-family: monospace;">${osInfo.kernel || "-"}</span>
                </div>
                <div class="info-item">
                    <span class="info-label">Architecture</span>
                    <span class="info-value">${osInfo.architecture || "-"}</span>
                </div>
                <div class="info-item">
                    <span class="info-label">Uptime</span>
                    <span class="info-value">${osInfo.uptime || "-"}</span>
                </div>
                <div class="info-item">
                    <span class="info-label">Last Boot</span>
                    <span class="info-value">${osInfo.last_boot || "-"}</span>
                </div>
                <div class="info-item">
                    <span class="info-label">Codename</span>
                    <span class="info-value">${osInfo.codename || "-"}</span>
                </div>
            </div>
            `
      : "";

    const aptActionsHtml =
      host.base === "ubuntu"
        ? `
            <h4 style="margin-top: 1.5rem; margin-bottom: 0.5rem; color: var(--accent-orange);">Package Management</h4>
            <div id="apt-status-${hostName}" style="margin-bottom: 1rem; padding: 0.5rem; background: var(--bg-tertiary); border-radius: 4px;">
                <span style="color: var(--text-secondary);">Click "Check Updates" to see available updates</span>
            </div>
            <div style="display: flex; gap: 0.5rem; flex-wrap: wrap;">
                <button onclick="checkAptUpdates('${hostName}')" class="btn btn-secondary btn-small">Check Updates</button>
                <button onclick="aptUpgrade('${hostName}', false)" class="btn btn-primary btn-small">Upgrade All</button>
                <button onclick="aptUpgrade('${hostName}', true)" class="btn btn-secondary btn-small">Security Only</button>
                <button onclick="aptAutoremove('${hostName}')" class="btn btn-secondary btn-small">Autoremove</button>
                <button onclick="aptClean('${hostName}')" class="btn btn-secondary btn-small">Clean Cache</button>
            </div>
            `
        : "";

    body.innerHTML = `
            <div class="host-info" style="grid-template-columns: 1fr 1fr;">
                <div class="info-item">
                    <span class="info-label">Name</span>
                    <span class="info-value">${host.name}</span>
                </div>
                <div class="info-item">
                    <span class="info-label">Address</span>
                    <span class="info-value">${host.addr || "Unknown"}</span>
                </div>
                <div class="info-item">
                    <span class="info-label">Base OS</span>
                    <span class="info-value">${host.base || "Unknown"}</span>
                </div>
                <div class="info-item">
                    <span class="info-label">SSH User</span>
                    <span class="info-value">${host.ssh_user || "default"}</span>
                </div>
                <div class="info-item">
                    <span class="info-label">Online</span>
                    <span class="info-value" style="color: ${host.online ? "var(--accent-green)" : "var(--accent-red)"}">${host.online ? "Yes" : "No"}</span>
                </div>
                <div class="info-item">
                    <span class="info-label">Generation</span>
                    <span class="info-value">${host.generation || "-"}</span>
                </div>
                ${rolesHtml}
                <div class="info-item">
                    <span class="info-label">Reboot Needed</span>
                    <span class="info-value" style="color: ${host.reboot ? "var(--accent-orange)" : "inherit"}">${host.reboot ? "Yes" : "No"}</span>
                </div>
            </div>
            ${osInfoHtml}
            ${
              host.pull_mode
                ? `<h4 style="margin-top: 1.5rem; margin-bottom: 0.5rem; color: var(--accent-purple);">Pull Mode</h4>
                <div class="host-info" style="grid-template-columns: 1fr 1fr;">
                    ${pullModeInfo}
                </div>`
                : ""
            }
            ${aptActionsHtml}
            ${host.state && Object.keys(host.state).length > 0 ? `<h4 style="margin-top: 1.5rem; margin-bottom: 0.5rem;">State</h4><pre>${JSON.stringify(host.state, null, 2)}</pre>` : ""}
        `;
  } catch (error) {
    body.innerHTML = `<div class="loading">Error: ${error.message}</div>`;
  }
}

// APT package management functions

async function checkAptUpdates(hostName) {
  const statusDiv = document.getElementById(`apt-status-${hostName}`);
  if (statusDiv) {
    statusDiv.innerHTML =
      '<span style="color: var(--accent-blue);">Checking for updates...</span>';
  }
  addActivity(`Checking updates on ${hostName}...`);

  try {
    const response = await fetch(`${API_BASE}/hosts/${hostName}/apt/updates`);
    if (!response.ok) throw new Error("Failed to check updates");

    const status = await response.json();

    if (statusDiv) {
      const securityBadge =
        status.security_updates > 0
          ? `<span style="color: var(--accent-red); font-weight: bold;">${status.security_updates} security</span>`
          : "";

      statusDiv.innerHTML = `
                <div style="display: flex; justify-content: space-between; align-items: center;">
                    <div>
                        <strong>${status.pending_updates}</strong> updates available ${securityBadge}
                        ${status.reboot_required ? '<span style="color: var(--accent-orange); margin-left: 1rem;">⚠ Reboot required</span>' : ""}
                    </div>
                    <span style="color: var(--text-secondary); font-size: 0.8rem;">Checked: ${new Date(status.last_check).toLocaleTimeString()}</span>
                </div>
                ${
                  status.packages && status.packages.length > 0
                    ? `<details style="margin-top: 0.5rem;">
                        <summary style="cursor: pointer; color: var(--accent-blue);">View packages</summary>
                        <div style="max-height: 200px; overflow-y: auto; margin-top: 0.5rem;">
                            ${status.packages
                              .map(
                                (pkg) => `
                                <div style="display: flex; justify-content: space-between; padding: 0.25rem 0; border-bottom: 1px solid var(--border-color);">
                                    <span style="font-family: monospace;">${pkg.name}</span>
                                    <span style="color: var(--text-secondary); font-size: 0.8rem;">${pkg.installed_version} → ${pkg.available_version}${pkg.is_security_update ? ' <span style="color: var(--accent-red);">⚠</span>' : ""}</span>
                                </div>
                            `,
                              )
                              .join("")}
                        </div>
                    </details>`
                    : ""
                }
            `;
    }

    addActivity(
      `${hostName}: ${status.pending_updates} updates (${status.security_updates} security)`,
    );
  } catch (error) {
    if (statusDiv) {
      statusDiv.innerHTML = `<span style="color: var(--accent-red);">Error: ${error.message}</span>`;
    }
    addActivity(
      `Failed to check updates on ${hostName}: ${error.message}`,
      "error",
    );
  }
}

async function aptUpgrade(hostName, securityOnly) {
  const action = securityOnly ? "security upgrade" : "full upgrade";
  if (!confirm(`Run ${action} on ${hostName}? This may take several minutes.`))
    return;

  const statusDiv = document.getElementById(`apt-status-${hostName}`);
  if (statusDiv) {
    statusDiv.innerHTML = `<span style="color: var(--accent-blue);">Running ${action}...</span>`;
  }
  addActivity(`Running ${action} on ${hostName}...`);

  try {
    const response = await fetch(
      `${API_BASE}/hosts/${hostName}/apt/upgrade${securityOnly ? "?security=true" : ""}`,
      { method: "POST" },
    );
    if (!response.ok) throw new Error("Upgrade failed");

    const result = await response.json();

    if (statusDiv) {
      if (result.success) {
        statusDiv.innerHTML = `<span style="color: var(--accent-green);">Upgrade complete! ${result.upgraded_packages?.length || 0} packages upgraded.</span>`;
      } else {
        statusDiv.innerHTML = `<span style="color: var(--accent-red);">Upgrade failed: ${result.error}</span>`;
      }
    }

    addActivity(
      `${hostName}: ${action} ${result.success ? "completed" : "failed"}`,
      result.success ? "info" : "error",
    );
  } catch (error) {
    if (statusDiv) {
      statusDiv.innerHTML = `<span style="color: var(--accent-red);">Error: ${error.message}</span>`;
    }
    addActivity(`${action} failed on ${hostName}: ${error.message}`, "error");
  }
}

async function aptAutoremove(hostName) {
  if (!confirm(`Remove unused packages on ${hostName}?`)) return;

  addActivity(`Running autoremove on ${hostName}...`);

  try {
    const response = await fetch(
      `${API_BASE}/hosts/${hostName}/apt/autoremove`,
      { method: "POST" },
    );
    if (!response.ok) throw new Error("Autoremove failed");

    const result = await response.json();
    addActivity(`${hostName}: Removed ${result.count} unused packages`);
  } catch (error) {
    addActivity(`Autoremove failed on ${hostName}: ${error.message}`, "error");
  }
}

async function aptClean(hostName) {
  addActivity(`Cleaning apt cache on ${hostName}...`);

  try {
    const response = await fetch(`${API_BASE}/hosts/${hostName}/apt/clean`, {
      method: "POST",
    });
    if (!response.ok) throw new Error("Clean failed");

    const result = await response.json();
    const freedMB = result.freed_mb?.toFixed(1) || 0;
    addActivity(`${hostName}: Cleaned cache, freed ${freedMB} MB`);
  } catch (error) {
    addActivity(`Clean failed on ${hostName}: ${error.message}`, "error");
  }
}

// Close modal
function closeModal() {
  document.getElementById("modal").classList.add("hidden");
}

// Close modal on backdrop click
document.getElementById("modal")?.addEventListener("click", (e) => {
  if (e.target.id === "modal") closeModal();
});

// Close modal on Escape key
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") closeModal();
});

// Activity log
const activityItems = [];
const MAX_ACTIVITY = 50;

function addActivity(message, type = "info") {
  const now = new Date();
  const time = now.toTimeString().split(" ")[0];

  activityItems.unshift({ time, message, type });
  if (activityItems.length > MAX_ACTIVITY) {
    activityItems.pop();
  }

  renderActivity();
}

function renderActivity() {
  const log = document.getElementById("activity-log");

  if (activityItems.length === 0) {
    log.innerHTML = '<div class="activity-item">Waiting for activity...</div>';
    return;
  }

  log.innerHTML = activityItems
    .map(
      (item) => `
        <div class="activity-item">
            <span class="activity-time">${item.time}</span>
            <span class="activity-message" style="${item.type === "error" ? "color: var(--accent-red)" : ""}">${item.message}</span>
        </div>
    `,
    )
    .join("");
}

// Load activity from server (if available)
async function loadActivity() {
  // This could be extended to load activity from server
  // For now, activity is client-side only
}

// Add initial activity
addActivity("Dashboard loaded");
