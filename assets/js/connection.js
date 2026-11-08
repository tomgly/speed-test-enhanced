(function (root) {
  'use strict';
  function origin(value) {
    if (!value) return '';
    try {
      const url = new URL(value);
      return ['http:', 'https:'].includes(url.protocol) && !url.username && !url.password && url.pathname === '/' && !url.search && !url.hash ? url.origin : '';
    } catch { return ''; }
  }
  function address(value) {
    if (typeof value !== 'string') return '';
    value = value.toLowerCase();
    const mapped = value.match(/^::ffff:(\d+\.\d+\.\d+\.\d+)$/);
    if (mapped) value = mapped[1];
    const hex = value.match(/^::ffff:([\da-f]{1,4}):([\da-f]{1,4})$/);
    if (hex) {
      const a = parseInt(hex[1], 16), b = parseInt(hex[2], 16);
      value = `${a >> 8}.${a & 255}.${b >> 8}.${b & 255}`;
    }
    if (/^\d+\.\d+\.\d+\.\d+$/.test(value)) return value.split('.').every(n => Number(n) <= 255 && String(Number(n)) === n) ? value : '';
    try { return value.includes(':') ? new URL(`http://[${value}]/`).hostname.slice(1, -1) : ''; } catch { return ''; }
  }
  function addresses(get) { return { client: address(get('X-Speedtest-Client')), server: address(get('X-Speedtest-Server')) }; }
  function matches(info, family) {
    return !!info.client && !!info.server && [info.client, info.server].every(ip => ip.includes(':') === (family === 6));
  }
  async function select(config, probe) {
    const fallback = origin(config.ipv4Origin);
    const preferred = origin(config.ipv6Origin);
    if (preferred) {
      try { if (matches(await probe(preferred), 6)) return { origin: preferred, fallback, family: 6, fallbackFamily: fallback ? 4 : 0 }; } catch { /* Try the default/IPv4 endpoint. */ }
    }
    return { origin: fallback, fallback, family: fallback ? 4 : 0, fallbackFamily: fallback ? 4 : 0 };
  }
  const api = { origin, address, addresses, matches, select };
  if (typeof module !== 'undefined') module.exports = api;
  else root.SpeedConnection = api;
})(globalThis);
