const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

test('Worker aborts the active request and waits for session deletion before confirming cancellation', async () => {
  const messages = [];
  let startDownload, startCleanup, finishCleanup, transferSignal;
  const downloading = new Promise(resolve => { startDownload = resolve; });
  const cleaningUp = new Promise(resolve => { startCleanup = resolve; });
  const cleanup = new Promise(resolve => { finishCleanup = resolve; });
  const context = vm.createContext({
    SpeedCore: require('../assets/js/measurement-core.js'),
    SpeedConnection: require('../assets/js/connection.js'),
    importScripts() {}, onmessage: null, postMessage: message => messages.push(message),
    TextEncoder, AbortController, performance, setTimeout, clearTimeout, setInterval, clearInterval,
    async fetch(path, { method, signal } = {}) {
      if (path === '/api/connection') return Response.json({});
      if (path === '/api/sessions') return Response.json({ id: 'test-session' });
      if (path === '/api/spec') return Response.json({ probeSeconds: .4, probeSamples: 3, factor: 2, gain: 1.08, warmup: 0, pingSamples: 10 });
      if (path === '/api/ping') return new Response(null);
      if (path.startsWith('/api/download?')) {
        transferSignal = signal;
        startDownload();
        return new Promise((resolve, reject) => signal.addEventListener('abort', () => reject(signal.reason), { once: true }));
      }
      assert.equal(path, '/api/session?id=test-session');
      assert.equal(method, 'DELETE');
      assert.equal(signal.aborted, false);
      startCleanup();
      return cleanup;
    }
  });
  vm.runInContext(fs.readFileSync(require.resolve('../assets/js/measurement-worker.js'), 'utf8'), context);
  vm.runInContext('payload = () => null', context);
  const running = context.onmessage({ data: { type: 'start', settings: { mode: 'manual', connections: 1, duration: 5 } } });
  await Promise.race([downloading, running.then(() => assert.fail('Download never started'))]);
  await context.onmessage({ data: { type: 'cancel' } });
  try {
    await Promise.race([cleaningUp, running.then(() => assert.fail('Session cleanup never started'))]);
    assert.equal(transferSignal.aborted, true);
    assert.ok(!messages.some(message => message.type === 'cancelled'));
  } finally {
    finishCleanup(new Response(null, { status: 204 }));
    await running;
  }
  assert.equal(messages.at(-1).type, 'cancelled');
});
