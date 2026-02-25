// Patch dns.lookup to use c-ares resolver instead of glibc getaddrinfo.
// Nix-patched glibc in nix2container images cannot dlopen NSS modules,
// causing getaddrinfo to fail for external hostnames. The c-ares resolver
// (used by dns.resolve) works correctly with the kernel DNS stack.
const dns = require("dns");
const { Resolver } = dns;
const r = new Resolver();
const origLookup = dns.lookup;
dns.lookup = function (hostname, opts, cb) {
  if (typeof opts === "function") {
    cb = opts;
    opts = {};
  }
  if (!hostname || hostname === "localhost" || hostname.endsWith(".local")) {
    return origLookup.call(dns, hostname, opts, cb);
  }
  const family = (opts && opts.family) || 0;
  const resolve = family === 6 ? r.resolve6.bind(r) : r.resolve4.bind(r);
  resolve(hostname, (err, addrs) => {
    if (err) return origLookup.call(dns, hostname, opts, cb);
    if (opts && opts.all)
      return cb(
        null,
        addrs.map((a) => ({ address: a, family: family === 6 ? 6 : 4 })),
      );
    cb(null, addrs[0], family === 6 ? 6 : 4);
  });
};
