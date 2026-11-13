'use strict';
importScripts('measurement-core.js', 'connection.js');

let running = false, cancelled = false, route, session, spec, usage, transport;
const active = new Set(), clients = new Set(), servers = new Set();
const downloadEnd = new TextEncoder().encode('speedtest-end-v1');
const now = () => performance.now();
const check = () => { if (cancelled) throw new Error('Cancelled'); };
const endpoint = path => `${route?.origin || ''}${path}`;
function addresses(response) {
  const info = SpeedConnection.addresses(name => response.headers.get(name));
  if (route?.family && !SpeedConnection.matches(info, route.family)) throw new Error('Unexpected measurement address family');
  if (info.client) clients.add(info.client);
  if (info.server) servers.add(info.server);
}
async function request(path, options = {}, origin) {
  check();
  const controller = new AbortController(), abort = () => controller.abort();
  active.add(abort);
  const timer = setTimeout(abort, options.timeout || 35000);
  try {
    const response = await fetch(origin === undefined ? endpoint(path) : `${origin}${path}`, { cache: 'no-store', ...options, signal: controller.signal });
    if (!response.ok) throw new Error(`Measurement endpoint: HTTP ${response.status}`);
    return { response, release: () => { clearTimeout(timer); active.delete(abort); } };
  } catch (error) { clearTimeout(timer); active.delete(abort); throw error; }
}
async function json(path, options, origin) {
  const r = await request(path, options, origin);
  try { return await r.response.json(); } finally { r.release(); }
}
function abortAll() { for (const abort of active) abort(); }
async function deleteSession() {
  if (!session) return;
  const path = endpoint(`/api/session?id=${encodeURIComponent(session.id)}`);
  session = null;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 2000);
  try {
    await fetch(path, { method: 'DELETE', signal: controller.signal });
  } catch { /* The server expires sessions that could not be deleted. */ }
  finally { clearTimeout(timer); }
}
function payload() {
  const data = new Uint8Array(8 * 1024 * 1024);
  for (let i = 0; i < data.length; i += 65536) crypto.getRandomValues(data.subarray(i, i + 65536));
  return new Blob([data], { type: 'application/octet-stream' });
}
async function ping() {
  postMessage({ type: 'phase', phase: 'ping' });
  const samples = [];
  for (let i = 0; i <= spec.pingSamples; i++) {
    const start = now(), r = await request('/api/ping', { method: 'HEAD', timeout: 5000 });
    try { addresses(r.response); if (i) samples.push(now() - start); } finally { r.release(); }
  }
  const ordered = [...samples].sort((a, b) => a - b), middle = Math.floor(ordered.length / 2);
  const ping = ordered.length % 2 ? ordered[middle] : (ordered[middle - 1] + ordered[middle]) / 2;
  const jitter = samples.slice(1).reduce((sum, value, i) => sum + Math.abs(value - samples[i]), 0) / Math.max(1, samples.length - 1);
  postMessage({ type: 'latency', ping, jitter });
  return { ping, jitter };
}
function xhrUpload(path, body, timeout) {
  check();
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest(), abort = () => xhr.abort(); active.add(abort);
    function finish(error) { active.delete(abort); if (error) reject(error); else resolve(); }
    xhr.open('POST', endpoint(path)); xhr.timeout = timeout;
    xhr.onload = () => {
      if (xhr.status !== 200 || Number(xhr.getResponseHeader('X-Received-Bytes')) !== body.size) return finish(new Error('Invalid upload acknowledgment'));
      try { addresses({ headers: { get: name => xhr.getResponseHeader(name) } }); finish(); } catch (error) { finish(error); }
    };
    xhr.onerror = () => finish(new Error('Upload connection failed'));
    xhr.ontimeout = () => finish(new Error('Upload timed out'));
    xhr.onabort = () => finish(new Error('Cancelled'));
    xhr.send(body);
  });
}

// Fresh transfers exclude calibration and warm-up. Complete acknowledgments and
// their actual drain time are included; incomplete bodies never enter the rate.
async function windowTransfer(direction, count, seconds, body, live = false) {
  check();
  const start = now(), end = start + seconds * 1000;
  let bytes = 0, lastBytes = 0, lastTime = start, failure;
  const path = `/api/${direction}?id=${encodeURIComponent(session.id)}`;
  const timer = live ? setInterval(() => {
    const t = now();
    postMessage({ type: 'sample', phase: direction, elapsed: (t - start) / 1000,
      bps: SpeedCore.rate(bytes - lastBytes, t - lastTime), progress: Math.min(.99, (t - start) / (seconds * 1000)), connections: count });
    lastBytes = bytes; lastTime = t;
  }, 200) : null;
  async function stream() {
    if (direction === 'download') {
      const r = await request(`${path}&framing=1&seconds=${seconds}`, { timeout: seconds * 1000 + 5000 });
      try {
        if (r.response.headers.get('Content-Encoding')) throw new Error('Compressed measurement response');
        addresses(r.response);
        if (r.response.headers.get('X-Speedtest-Download') !== 'framed-v1') throw new Error('Server does not support confirmed HTTP downloads');
        const reader = r.response.body.getReader();
        // Hold the suffix back from both live and final payload counts.
        const tail = new Uint8Array(downloadEnd.length);
        let received = 0;
        try { while (true) {
          const { done, value } = await reader.read(); if (done) break;
          check();
          const size = value.byteLength;
          if (size < tail.length) tail.copyWithin(0, size);
          tail.set(value.subarray(Math.max(0, size - tail.length)), Math.max(0, tail.length - size));
          const confirmed = Math.max(0, size - Math.max(0, tail.length - received));
          received += size;
          bytes += confirmed; usage.download.bytes += confirmed;
        } } finally { reader.releaseLock(); }
        if (received < tail.length || !tail.every((value, i) => value === downloadEnd[i])) throw new Error('Missing HTTP download completion marker');
      } finally { r.release(); }
    } else while (now() < end) {
      check();
      if (transport === 'xhr') await xhrUpload(path, body, seconds * 1000 + 5000);
      else {
        const r = await request(path, { method: 'POST', body, timeout: seconds * 1000 + 5000 });
        try {
          if (Number(r.response.headers.get('X-Received-Bytes')) !== body.size) throw new Error('Invalid upload acknowledgment');
          await r.response.arrayBuffer(); addresses(r.response);
        } finally { r.release(); }
      }
      check(); bytes += body.size; usage.upload.bytes += body.size;
    }
  }
  try {
    await Promise.allSettled(Array.from({ length: count }, async () => {
      try { await stream(); } catch (error) { failure ||= error; abortAll(); throw error; }
    }));
    check(); if (failure) throw failure;
    const elapsed = now() - start;
    if (!(bytes > 0 && elapsed >= seconds * 1000) || !Number.isSafeInteger(bytes)) throw new Error('No valid confirmed data or early transfer end');
    return { bytes, seconds: elapsed / 1000, bps: SpeedCore.rate(bytes, elapsed), connections: count };
  } finally { if (timer !== null) clearInterval(timer); }
}
async function measure(direction, config, body) {
  const start = now(); postMessage({ type: 'phase', phase: direction });
  try {
    // Compare browser upload APIs on this path, outside the measured result.
    if (direction === 'upload') {
      const rates = { fetch: [], xhr: [] };
      for (let i = 0; i < spec.probeSamples; i++) {
        for (const api of i % 2 ? ['xhr', 'fetch'] : ['fetch', 'xhr']) {
          transport = api;
          rates[api].push((await windowTransfer(direction, 1, .3, body)).bps);
        }
      }
      const fetchBps = rates.fetch.sort((a, b) => a - b)[1];
      const xhrBps = rates.xhr.sort((a, b) => a - b)[1];
      transport = xhrBps > fetchBps * spec.gain ? 'xhr' : 'fetch';
      postMessage({ type: 'transport', transport, fetchBps, xhrBps });
    }
    let count = config.connections;
    if (config.mode === 'auto' && count > 1) {
      let best = 1, bestRate = 0;
      for (let n = 1; ; n = Math.min(config.connections, n * spec.factor)) {
        const samples = [];
        for (let i = 0; i < spec.probeSamples; i++) {
          const probe = await windowTransfer(direction, n, spec.probeSeconds, body);
          if (!Number.isFinite(probe.bps) || probe.bps < 0) throw new Error('Invalid calibration result');
          samples.push(probe.bps);
          postMessage({ type: 'calibration', phase: direction, ...probe });
        }
        const bps = samples.sort((a, b) => a - b)[Math.floor(samples.length / 2)];
        if (bps > bestRate * spec.gain) { best = n; bestRate = bps; } else if (n > 1) break;
        if (n === config.connections) break;
      }
      count = best;
    }
    if (spec.warmup > 0) await windowTransfer(direction, count, spec.warmup, body);
    return await windowTransfer(direction, count, config.duration, body, true);
  } finally { usage[direction].duration += (now() - start) / 1000; }
}
onmessage = async ({ data }) => {
  if (data.type === 'cancel') { cancelled = true; abortAll(); return; }
  if (data.type !== 'start' || running) return;
  running = true; cancelled = false; route = null; session = null;
  usage = { download: { bytes: 0, duration: 0 }, upload: { bytes: 0, duration: 0 } };
  clients.clear(); servers.clear();
  const started = new Date().toISOString();
  try {
    const config = SpeedCore.settings(data.settings);
    const connection = await json('/api/connection', { timeout: 2000 });
    route = await SpeedConnection.select(connection, async origin => {
      const r = await request('/api/connection', { timeout: 2000 }, origin);
      try { return SpeedConnection.addresses(name => r.response.headers.get(name)); } finally { await r.response.arrayBuffer(); r.release(); }
    });
    const body = payload();
    for (let attempt = 0; attempt < 2; attempt++) {
      try {
        session = await json('/api/sessions', { method: 'POST' }); spec = await json('/api/spec');
        if (!(spec.probeSeconds > 0 && spec.probeSeconds <= 1 && spec.probeSamples === 3 && spec.factor > 1 && spec.factor <= 4 && spec.gain >= 1 && spec.warmup >= 0 && spec.warmup <= 5 && spec.pingSamples === 10)) throw new Error('Unsupported measurement specification');
        const latency = await ping();
        const download = await measure('download', config, body);
        postMessage({ type: 'result', phase: 'download', ...download });
        const upload = await measure('upload', config, body);
        postMessage({ type: 'result', phase: 'upload', ...upload }); check();
        const result = { config: { mode: config.mode, connections: config.connections, duration: config.duration, protocol: 'http', warmup: spec.warmup, rate: 1e9, packetSize: 1400 },
          ...latency, download, upload, started, client: [...clients].join(', '), server: [...servers].join(', ') };
        await json(`/api/results?id=${encodeURIComponent(session.id)}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(result) });
        postMessage({ type: 'complete', usage, connection: { client: [...clients].join(', '), server: [...servers].join(', ') }, result });
        break;
      } catch (error) {
        if (cancelled || attempt || route.origin === route.fallback) throw error;
        await deleteSession();
        check();
        route = { origin: route.fallback, fallback: route.fallback, family: route.fallbackFamily };
        clients.clear(); servers.clear(); postMessage({ type: 'restart' });
      }
    }
  } catch (error) { postMessage({ type: cancelled ? 'cancelled' : 'error', message: error.message }); }
  finally {
    abortAll(); running = false;
    if (session) fetch(endpoint(`/api/session?id=${encodeURIComponent(session.id)}`), { method: 'DELETE', keepalive: true }).catch(() => {});
  }
};
