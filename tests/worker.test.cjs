const { test, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const { webcrypto } = require('node:crypto');
let server, base;
before(async () => {
  const executable = path.resolve(__dirname, process.platform === 'win32' ? '../speedtest.exe' : '../speedtest');
  server = spawn(executable, ['server', '--http', '127.0.0.1:0', '--tcp', '127.0.0.1:0', '--udp', '127.0.0.1:0'], { stdio: ['ignore', 'ignore', 'pipe'] });
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('Go server did not start; build the executable first')), 10000);
    server.once('error', error => { clearTimeout(timer); reject(error); });
    server.stderr.on('data', data => {
      const port = String(data).match(/HTTP 127\.0\.0\.1:(\d+)/);
      if (port) { base = `http://127.0.0.1:${port[1]}`; clearTimeout(timer); resolve(); }
    });
  });
});
after(async () => { if (server && server.exitCode === null) { const closed = once(server, 'close'); server.kill(); await closed; } });

async function harness() {
  const messages = [], requests = [];
  const session = await (await fetch(`${base}/api/sessions`, { method: 'POST' })).json();
  const spec = await (await fetch(`${base}/api/spec`)).json();
  const state = { badAck: false, earlyEnd: false, compressed: false, partialEarly: false };
  const network = async (url, options = {}) => {
    requests.push({ url, method: options.method || 'GET', body: options.body });
    if (state.partialEarly && url.includes('/api/download')) {
      state.partialEarly = false;
      return new Response(new Uint8Array(7));
    }
    if (state.earlyEnd && url.includes('/api/download')) return new Response(new Uint8Array(7));
    if (state.compressed && url.includes('/api/download')) return new Response(new Uint8Array(7), {headers:{'Content-Encoding':'gzip'}});
    const response = await fetch(new URL(url, base), options);
    if (state.badAck && url.includes('/api/upload')) return new Response('', { headers: { 'X-Received-Bytes': '1' } });
    return response;
  };
  // XHR is emulated only for protocol tests; actual browser compatibility is
  // checked in the real UI. Both paths must verify the same server receipt.
  class XHR {
    open(method, url) { this.method = method; this.url = url; }
    send(body) {
      this.controller = new AbortController();
      this.timer = setTimeout(() => { this.controller.abort(); this.ontimeout(); }, this.timeout);
      network(this.url, { method: this.method, body, signal: this.controller.signal }).then(async response => {
        this.status = response.status; this.headers = response.headers; await response.arrayBuffer(); clearTimeout(this.timer); this.onload();
      }).catch(() => { clearTimeout(this.timer); this.onerror(); });
    }
    getResponseHeader(name) { return this.headers.get(name); }
    abort() { clearTimeout(this.timer); this.controller?.abort(); this.onabort(); }
  }
  const context = vm.createContext({ SpeedCore: require('../assets/js/measurement-core.js'), SpeedConnection: require('../assets/js/connection.js'),
    importScripts() {}, onmessage: null, postMessage: data => messages.push(structuredClone(data)),
    performance, crypto: webcrypto, Blob, Uint8Array, AbortController, XMLHttpRequest: XHR,
    fetch: network, setTimeout, clearTimeout, setInterval, clearInterval, console });
  vm.runInContext(fs.readFileSync(path.resolve(__dirname, '../assets/js/measurement-worker.js'), 'utf8'), context);
  vm.runInContext(`session = ${JSON.stringify(session)}; spec = ${JSON.stringify(spec)}; route = {origin:${JSON.stringify(base)}};
    transport = 'fetch'; usage = { download: {bytes:0,duration:0}, upload: {bytes:0,duration:0} };`, context);
  return { context, messages, requests, state, session, stop: async () => {
    vm.runInContext('cancelled=true;abortAll()', context);
    await fetch(`${base}/api/session?id=${session.id}`, { method: 'DELETE' });
  } };
}

test('Worker confirms real HTTP bytes in both directions, with fixed parallelism and raw rates', async () => {
  const h = await harness();
  try {
    for (const direction of ['download', 'upload']) {
      const r = await vm.runInContext(`windowTransfer('${direction}', 4, .25, payload(), true)`, h.context);
      assert.ok(r.bytes > 0); assert.equal(r.connections, 4); assert.ok(r.seconds >= .25);
      assert.ok(Math.abs(r.bps - r.bytes * 8 / r.seconds) < Math.max(1, r.bps * 1e-12));
    }
    const bodies = h.requests.filter(r => r.method === 'POST').map(r => r.body);
    assert.ok(bodies.length > 0); assert.equal(new Set(bodies).size, 1);
  } finally { await h.stop(); }
});
test('Worker rejects bad receipts, compressed/early responses and cancellation without a result', async () => {
  for (const failure of ['badAck', 'earlyEnd', 'partialEarly', 'compressed', 'cancel']) {
    const h = await harness();
    try {
      h.state[failure] = true;
      const direction = failure === 'badAck' ? 'upload' : 'download';
      const pending = vm.runInContext(`windowTransfer('${direction}', 2, 1, payload(), true)`, h.context);
      if (failure === 'cancel') setTimeout(() => h.context.onmessage({ data: { type: 'cancel' } }), 50);
      await assert.rejects(pending);
      assert.ok(!h.messages.some(m => m.type === 'complete' || m.type === 'result'));
    } finally { await h.stop(); }
  }
});
test('Auto ignores isolated spikes and dips and considers intermediate parallelism', async () => {
  const h = await harness();
  try {
    vm.runInContext(`
      globalThis.counts = [];
      const rates = [1000,10,10,20,1,20,21,21,1000];
      windowTransfer = async (direction, count, seconds, body, live) => {
        if (!live && seconds === spec.probeSeconds) {
          counts.push(count);
          return {bytes: 1, seconds, bps: rates.shift(), connections: count};
        }
        return {bytes: 123, seconds, bps: 1, connections: count};
      };`, h.context);
    const r = await vm.runInContext(`measure('download',{mode:'auto',connections:16,duration:5},payload())`, h.context);
    assert.equal(r.connections, 2);
    assert.deepEqual(Array.from(h.context.counts), [1,1,1,2,2,2,4,4,4]);
    assert.equal(r.bytes, 123);
  } finally { await h.stop(); }
});
test('Upload API selection compares repeated probes and preserves Manual parallelism', async () => {
  const h = await harness();
  try {
    vm.runInContext(`
      globalThis.apis = [];
      const rates = {fetch:[1000,10,10],xhr:[20,1,20]};
      windowTransfer = async (direction, count, seconds, body, live) => {
        if (seconds === .3) {
          apis.push(transport);
          return {bps:rates[transport].shift()};
        }
        return {bytes:123,seconds,bps:1,connections:count};
      };`, h.context);
    const r = await vm.runInContext(`measure('upload',{mode:'manual',connections:7,duration:5},payload())`, h.context);
    assert.equal(r.connections, 7);
    assert.deepEqual(Array.from(h.context.apis), ['fetch','xhr','xhr','fetch','fetch','xhr']);
    const selected = h.messages.find(m => m.type === 'transport');
    assert.equal(selected.transport, 'xhr');
    assert.equal(selected.fetchBps, 10); assert.equal(selected.xhrBps, 20);
  } finally { await h.stop(); }
});
test('Auto selects each direction independently; warm-up and API probes stay out of final bytes', async () => {
  const h = await harness();
  try {
    vm.runInContext(`
      globalThis.windows = [];
      spec.warmup = 1;
      windowTransfer = async (direction, count, seconds, body, live) => {
        windows.push({direction, count, seconds, live});
        const bps = direction === 'download' ? count * 100 : 100;
        return { bytes: live ? 123 : 999999, seconds, bps, connections: count };
      };`, h.context);
    const result = await vm.runInContext(`(async () => {
      const c = {mode:'auto',connections:16,duration:5}, body = payload();
      return [await measure('download',c,body),await measure('upload',c,body)];
    })()`, h.context);
    assert.deepEqual(Array.from(result, r => r.connections), [16, 1]);
    assert.deepEqual(Array.from(result, r => r.bytes), [123, 123]);
    for (const direction of ['download', 'upload']) {
      const windows = Array.from(h.context.windows).filter(w => w.direction === direction);
      assert.equal(windows.at(-1).count, windows.at(-2).count);
      assert.equal(windows.at(-2).seconds, 1); assert.equal(windows.at(-1).seconds, 5);
      assert.equal(windows.filter(w => w.live).length, 1);
    }
  } finally { await h.stop(); }
});
for (const mode of ['manual', 'auto']) test(`production ${mode} start completes, validates with Go, and preserves UI message and settings contracts`, { timeout: 40000 }, async () => {
  const h = await harness();
  try {
    // The production settings path retains the minimum supported five seconds.
    await h.context.onmessage({ data: { type: 'start', settings: { mode, connections: 2, duration: 5 } } });
    const complete = h.messages.find(m => m.type === 'complete');
    assert.ok(complete, JSON.stringify(h.messages.at(-1)));
    assert.equal(complete.result.config.duration, 5);
    for (const direction of ['download', 'upload']) {
      const r = complete.result[direction];
      assert.ok(r.connections >= 1 && r.connections <= 2); assert.ok(r.bytes > 0); assert.ok(r.seconds >= 5);
      if (mode === 'manual') assert.equal(r.connections, 2);
      const samples = h.messages.filter(m => m.type === 'sample' && m.phase === direction);
      assert.ok(samples.length > 0); assert.ok(samples.every(m => m.connections === r.connections));
      assert.ok(complete.usage[direction].bytes > r.bytes);
      assert.ok(complete.usage[direction].duration > r.seconds);
    }
    assert.ok(h.messages.some(m => m.type === 'transport'));
    assert.equal(complete.connection.client, '127.0.0.1');
  } finally { await h.stop(); }
});
