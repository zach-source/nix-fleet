const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const { test } = require("node:test");

function dashboard() {
  const elements = new Map();
  const listeners = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id))
        elements.set(id, {
          innerHTML: "",
          textContent: "",
          dataset: {},
          classList: { add() {}, remove() {} },
          addEventListener() {},
        });
      return elements.get(id);
    },
    addEventListener(type, listener) {
      listeners.set(type, listener);
    },
  };
  const context = vm.createContext({
    document,
    console: { error() {} },
    setTimeout() {},
    setInterval() {},
    localStorage: {
      getItem() {
        return "";
      },
    },
    confirm() {
      return true;
    },
  });
  vm.runInContext(fs.readFileSync(`${__dirname}/ui/app.js`, "utf8"), context);
  return { context, document, listeners };
}

const payloads = [
  "<img src=x onerror='alert(1)'>",
  "</pre><svg onload=alert(1)>",
  '\" onclick=\"alert(1)\" data-x=\"',
  "');alert(1);//&<",
];
function escaped(value) {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}
function safeHTML(html, payload) {
  assert.ok(!html.includes(payload), `raw payload reached HTML: ${payload}`);
  assert.ok(
    html.includes(escaped(payload)),
    "value must remain visible as text",
  );
  assert.doesNotMatch(html, /<button[^>]*\bonclick=/);
}

for (const payload of payloads) {
  test(`host, OS, state, package and error sinks: ${payload}`, async () => {
    const { context: c, document: d } = dashboard();
    const host = Object.fromEntries(
      ["name", "addr", "base", "generation", "ssh_user"].map((key) => [
        key,
        payload,
      ]),
    );
    host.roles = [payload];
    host.state = { value: payload };
    host.pull_mode = true;
    host.pull_mode_status = Object.fromEntries(
      ["last_run", "last_result", "next_run", "current_commit"].map((key) => [
        key,
        payload,
      ]),
    );
    const os = Object.fromEntries(
      [
        "pretty_name",
        "kernel",
        "architecture",
        "uptime",
        "last_boot",
        "codename",
      ].map((key) => [key, payload]),
    );
    c.fetch = async (url) => ({
      ok: true,
      json: async () => (url.endsWith("/os-info") ? os : host),
    });
    safeHTML(c.renderHostCard(host, "healthy"), payload);
    await c.viewDetails(payload);
    safeHTML(d.getElementById("modal-body").innerHTML, payload);
    host.base = "ubuntu";
    await c.viewDetails(payload);
    safeHTML(d.getElementById("modal-body").innerHTML, payload);
    c.fetch = async () => ({
      ok: true,
      json: async () => ({
        pending_updates: payload,
        security_updates: 1,
        packages: [
          {
            name: payload,
            installed_version: payload,
            available_version: payload,
          },
        ],
      }),
    });
    await c.checkAptUpdates(payload);
    safeHTML(d.getElementById(`apt-status-${payload}`).innerHTML, payload);
    safeHTML(d.getElementById("activity-log").innerHTML, payload);
    c.fetch = async () => ({
      ok: true,
      json: async () => ({ success: false, error: payload }),
    });
    await c.aptUpgrade(payload, true);
    safeHTML(d.getElementById(`apt-status-${payload}`).innerHTML, payload);
    c.fetch = async () => {
      throw new Error(payload);
    };
    for (const action of [
      c.loadHosts,
      c.viewDetails,
      c.checkAptUpdates,
      c.aptUpgrade,
    ]) {
      await action(payload);
    }
    for (const id of [
      "hosts-grid",
      "modal-body",
      `apt-status-${payload}`,
      "activity-log",
    ]) {
      safeHTML(d.getElementById(id).innerHTML, payload);
    }
  });
}

test("host actions keep names as data and encode request paths", async () => {
  const { context: c, listeners } = dashboard();
  const name = "ordinary & 'host/other?x=#";
  const requests = [];
  c.fetch = async (url) => {
    requests.push(url);
    return { ok: true, json: async () => ({}) };
  };
  const button = {
    dataset: { action: "apply" },
    closest() {
      return { dataset: { host: name } };
    },
  };
  await listeners.get("click")({
    target: {
      closest() {
        return button;
      },
    },
  });
  assert.equal(requests[0], `/api/hosts/${encodeURIComponent(name)}/apply`);
});
