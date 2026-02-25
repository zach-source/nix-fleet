// Patch dns.lookup to use c-ares resolver instead of glibc getaddrinfo.
// Nix-patched glibc in nix2container images cannot dlopen NSS modules,
// causing getaddrinfo to fail for external hostnames. The c-ares resolver
// (used by dns.resolve) works correctly with the kernel DNS stack.
//
// IMPORTANT: Never fall back to the original dns.lookup (getaddrinfo) for
// external hostnames — it will always fail in nix2container images.
const dns = require("dns");
const http = require("http");
const https = require("https");
const { Resolver } = dns;
const r = new Resolver();
const origLookup = dns.lookup;

function caresLookup(hostname, opts, cb) {
  if (typeof opts === "function") {
    cb = opts;
    opts = {};
  }
  opts = opts || {};

  // localhost and .local addresses can use original lookup (no NSS needed)
  if (!hostname || hostname === "localhost" || hostname.endsWith(".local")) {
    return origLookup.call(dns, hostname, opts, cb);
  }

  const family = (opts && opts.family) || 0;

  if (family === 4 || family === 0) {
    // Try IPv4 first
    r.resolve4(hostname, (err4, addrs4) => {
      if (!err4 && addrs4 && addrs4.length) {
        if (opts.all) {
          if (family === 0) {
            // Also try IPv6 for completeness when family=0 and all=true
            r.resolve6(hostname, (err6, addrs6) => {
              const res = addrs4.map((a) => ({ address: a, family: 4 }));
              if (!err6 && addrs6)
                res.push(...addrs6.map((a) => ({ address: a, family: 6 })));
              cb(null, res);
            });
            return;
          }
          return cb(
            null,
            addrs4.map((a) => ({ address: a, family: 4 })),
          );
        }
        return cb(null, addrs4[0], 4);
      }
      // IPv4 failed
      if (family === 4) {
        return cb(err4 || new Error("queryA ENOTFOUND " + hostname));
      }
      // family=0: fall through to IPv6
      r.resolve6(hostname, (err6, addrs6) => {
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
    r.resolve6(hostname, (err, addrs) => {
      if (err || !addrs || !addrs.length) {
        return cb(err || new Error("queryAAAA ENOTFOUND " + hostname));
      }
      if (opts.all)
        return cb(
          null,
          addrs.map((a) => ({ address: a, family: 6 })),
        );
      cb(null, addrs[0], 6);
    });
  }
}

// Replace dns.lookup globally
dns.lookup = caresLookup;

// Patch http/https Agent constructors so new agents default to c-ares lookup
const OrigAgent = http.Agent;
const OrigSAgent = https.Agent;
const patchAgent = (Orig) => {
  return function PatchedAgent(opts) {
    opts = opts || {};
    if (!opts.lookup) opts.lookup = caresLookup;
    return new Orig(opts);
  };
};
http.Agent = patchAgent(OrigAgent);
http.Agent.prototype = OrigAgent.prototype;
https.Agent = patchAgent(OrigSAgent);
https.Agent.prototype = OrigSAgent.prototype;

// Patch existing global agents
if (http.globalAgent) http.globalAgent.options.lookup = caresLookup;
if (https.globalAgent) https.globalAgent.options.lookup = caresLookup;
