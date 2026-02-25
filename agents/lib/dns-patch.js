// Patch dns.lookup to use c-ares resolver with retry logic.
//
// Problem: CoreDNS intermittently returns FORMERR when upstream DNS
// (local LAN servers) is flaky. Both getaddrinfo and c-ares fail at the
// same timestamps, confirming it's a cluster DNS issue.
//
// Solution: Replace dns.lookup with c-ares (resolve4/6) which operates on
// the event loop (not the libuv thread pool), with 5 retries at 500ms
// intervals using a fresh Resolver per attempt.
const dns = require("dns");
const net = require("net");
const { Resolver } = dns;
const origLookup = dns.lookup;

function resolveRetry(hostname, type, retries, cb) {
  const r = new Resolver();
  const method = type === 6 ? "resolve6" : "resolve4";
  r[method](hostname, (err, addrs) => {
    if (!err && addrs && addrs.length) return cb(null, addrs);
    if (retries > 0)
      return setTimeout(
        () => resolveRetry(hostname, type, retries - 1, cb),
        500,
      );
    cb(err);
  });
}

dns.lookup = function caresLookup(hostname, opts, cb) {
  if (typeof opts === "function") {
    cb = opts;
    opts = {};
  }
  opts = opts || {};

  // IP addresses — return immediately
  if (net.isIP(hostname)) {
    const f = net.isIPv4(hostname) ? 4 : 6;
    if (opts.all) return cb(null, [{ address: hostname, family: f }]);
    return cb(null, hostname, f);
  }

  // Local names — safe to use original getaddrinfo
  if (
    !hostname ||
    hostname === "localhost" ||
    hostname === "loopback" ||
    hostname.endsWith(".local")
  ) {
    return origLookup.call(dns, hostname, opts, cb);
  }

  const family = (opts && opts.family) || 0;

  if (family === 4 || family === 0) {
    resolveRetry(hostname, 4, 5, (err4, addrs4) => {
      if (!err4 && addrs4 && addrs4.length) {
        if (opts.all)
          return cb(
            null,
            addrs4.map((a) => ({ address: a, family: 4 })),
          );
        return cb(null, addrs4[0], 4);
      }
      if (family === 4)
        return cb(err4 || new Error("queryA ENOTFOUND " + hostname));
      // family=0: fall through to IPv6
      resolveRetry(hostname, 6, 5, (err6, addrs6) => {
        if (!err6 && addrs6 && addrs6.length) {
          if (opts.all)
            return cb(
              null,
              addrs6.map((a) => ({ address: a, family: 6 })),
            );
          return cb(null, addrs6[0], 6);
        }
        cb(err4 || err6 || new Error("queryA ENOTFOUND " + hostname));
      });
    });
  } else {
    // IPv6 only
    resolveRetry(hostname, 6, 5, (err, addrs) => {
      if (err || !addrs || !addrs.length)
        return cb(err || new Error("queryAAAA ENOTFOUND " + hostname));
      if (opts.all)
        return cb(
          null,
          addrs.map((a) => ({ address: a, family: 6 })),
        );
      cb(null, addrs[0], 6);
    });
  }
};
