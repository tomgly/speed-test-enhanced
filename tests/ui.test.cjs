const { test } = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
function ui({ clipboard = true, stored = null, reducedMotion = false } = {}) {
  const elements = new Map(), workers = [], copied = [];
  const documentHandlers = {};
  const frames = new Map(); let frameID = 0;
  const html = fs.readFileSync(require.resolve('../index.html'), 'utf8');
  function element(id, hidden = false) {
    const classes = new Set();
    return { id, hidden, textContent: '', value: '', style: {}, handlers: {}, classList: {
      add(name) { classes.add(name); }, remove(name) { classes.delete(name); },
      toggle(name, enabled) { if (enabled) classes.add(name); else classes.delete(name); }, contains(name) { return classes.has(name); } },
      addEventListener(type, handler) { this.handlers[type] = handler; }, focus() {}, select() {}, remove() {} };
  }
  for (const match of html.matchAll(/<[^>]+\bid="([^"]+)"[^>]*>/g)) elements.set(match[1], element(match[1], /\bhidden\b/.test(match[0])));
  elements.get('settings-form').elements = Object.fromEntries(['mode', 'connections', 'duration', 'theme'].map(n => [n, element(n)]));
  elements.get('settings-dialog').showModal = elements.get('settings-dialog').close = () => {};
  class Worker {
    constructor() { workers.push(this); }
    postMessage(data) { this.request = structuredClone(data); }
    terminate() { this.terminated = true; }
    emit(data) { this.onmessage({ data }); }
  }
  const sandbox = vm.createContext({ SpeedCore: require('../assets/js/measurement-core.js'), SpeedResults: require('../assets/js/results.js'),
    Worker, performance: { now: () => 0 },
    localStorage: { getItem: () => stored, setItem: (key, value) => { stored = value; } }, matchMedia: query => ({ matches: query.includes('reduced-motion') && reducedMotion, addEventListener() {} }),
    FormData: class { constructor(form) { this.values = Object.entries(form.elements).map(([key, element]) => [key, element.value]); } [Symbol.iterator]() { return this.values[Symbol.iterator](); } },
    requestAnimationFrame: callback => { frames.set(++frameID, callback); return frameID; }, cancelAnimationFrame: id => frames.delete(id), setTimeout: () => 1, clearTimeout() {},
    navigator: { clipboard: clipboard ? { writeText: async text => copied.push(text) } : undefined },
    document: { getElementById: id => elements.get(id), documentElement: { dataset: {} }, body: { dataset: {}, append() {} },
      createElement: () => element('temporary'), addEventListener: (type, handler) => { documentHandlers[type] = handler; }, execCommand: command => { copied.push(command); return true; } } });
  vm.runInContext(fs.readFileSync(require.resolve('../assets/js/ui.js'), 'utf8'), sandbox);
  return { elements, workers, copied, click: id => elements.get(id).handlers.click(), escape: () => documentHandlers.keydown({ key: 'Escape' }), saved: () => stored,
    pendingFrames: () => frames.size,
    draw: time => { const pending = [...frames.values()]; frames.clear(); for (const callback of pending) callback(time); } };
}
const complete = { type: 'complete', usage: { download: { bytes: 2e9, duration: 13.2 }, upload: { bytes: 1e9, duration: 13.1 } }, connection: { client: '2001:db8::1234', server: '2001:db8::1' } };

test('header and footer links preserve the GitHub icon and attribution', () => {
  const html = fs.readFileSync(require.resolve('../index.html'), 'utf8');
  assert.match(html, /<a class="github-link" href="https:\/\/github.com\/tomgly\/speed-test-enhanced" target="_blank" rel="noopener noreferrer"[^>]*><svg[\s\S]*?<\/svg><span>GitHub<\/span><\/a>/);
  assert.match(html, /<footer><a href="\/">Speed Test Enhanced<\/a><span>made by<\/span><a href="https:\/\/github.com\/tomgly"[^>]*>tomgly<\/a><\/footer>/);
  assert.ok(fs.existsSync(require.resolve('../LICENSE')));
});

test('waiting dots cycle in the existing frame loop and stop on speed, errors or cancellation', () => {
  for (const end of ['error', 'cancelled', 'escape', 'worker-error']) {
    const h = ui();
    assert.equal(h.elements.get('live').hidden, true);
    assert.equal(h.pendingFrames(), 0);
    h.click('go'); const worker = h.workers[0], value = h.elements.get('live-value');
    for (const [time, text] of [[0, '\u00a0'], [400, '.'], [800, '..'], [1200, '...'], [1600, '\u00a0']]) {
      h.draw(time); assert.equal(value.textContent, text);
      assert.equal(value.classList.contains('waiting'), true);
      assert.equal(h.pendingFrames(), 1);
    }
    worker.emit({ type: 'phase', phase: 'download' });
    worker.emit({ type: 'sample', bps: 0, elapsed: .2, progress: .1 }); h.draw(1400);
    assert.equal(value.textContent, '0'); assert.equal(value.classList.contains('waiting'), false);
    worker.emit({ type: 'phase', phase: 'upload' });
    assert.equal(value.textContent, '\u00a0');
    if (end === 'escape') h.escape();
    else if (end === 'worker-error') worker.onerror();
    else worker.emit({ type: end, message: 'Server unavailable' });
    assert.equal(h.pendingFrames(), 0); assert.equal(worker.terminated, true);
    assert.equal(value.classList.contains('waiting'), false);
    assert.equal(h.elements.get('progress').hidden, true);
    worker.emit({ type: 'phase', phase: 'download' }); h.draw(1800);
    assert.equal(value.textContent, '—'); assert.equal(h.pendingFrames(), 0);
    h.click('retry'); assert.equal(value.textContent, '\u00a0'); assert.equal(h.pendingFrames(), 1);
  }
});

test('Auto and Manual share one progress bar that resets and completes each direction', () => {
  for (const mode of ['auto', 'manual']) {
    const h = ui({ stored: JSON.stringify({ mode, duration: 20 }) });
    h.click('go'); const worker = h.workers[0], progress = h.elements.get('progress');
    assert.equal(worker.request.settings.mode, mode);
    for (const phase of ['download', 'upload']) {
      worker.emit({ type: 'phase', phase }); assert.equal(progress.value, 0);
      worker.emit({ type: 'calibration', phase, bps: 1e9 }); assert.equal(progress.value, 0);
      for (const elapsed of [2, 10, 19.8, 21]) {
        worker.emit({ type: 'sample', phase, bps: 1e9, elapsed, progress: Math.min(.99, elapsed / 20) });
        assert.equal(progress.value, Math.min(.99, elapsed / 20));
      }
      worker.emit({ type: 'result', phase, bps: 2e9 });
      assert.equal(progress.value, 1); assert.equal(h.elements.get('live-value').textContent, '2');
      h.draw(200); assert.equal(h.elements.get('live-value').textContent, '2');
    }
    worker.emit({ type: 'restart' }); assert.equal(progress.value, 0);
    assert.equal(h.elements.get('live-value').textContent, '\u00a0');
    worker.emit({ type: 'phase', phase: 'download' }); assert.equal(progress.value, 0);
    worker.emit({ type: 'result', phase: 'download', bps: 2e9 });
    worker.emit({ type: 'phase', phase: 'upload' }); assert.equal(progress.value, 0);
    worker.emit({ type: 'result', phase: 'upload', bps: 3e9 });
    worker.emit(complete);
    assert.equal(h.pendingFrames(), 0); assert.equal(progress.hidden, true);
    assert.equal(h.elements.get('download-value').textContent, '2');
    assert.equal(h.elements.get('upload-value').textContent, '3');
  }
});
test('measurement shows only the shared instrument and progress; details appear on completion', () => {
  const h = ui();
  assert.equal(h.elements.has('history'), false);
  assert.equal(h.elements.has('total-data'), false);
  h.click('go'); const worker = h.workers[0];
  worker.emit({ type: 'latency', ping: 1.2, jitter: .3 });
  worker.emit({ type: 'calibration', phase: 'download', bps: 125e9, connections: 128 });
  assert.equal(h.elements.get('live-value').textContent, '\u00a0');
  assert.equal(h.elements.get('results').hidden, true);
  for (const phase of ['download', 'upload']) {
    worker.emit({ type: 'phase', phase });
    worker.emit({ type: 'sample', phase, bps: 1e9, elapsed: .2, progress: .5 });
    worker.emit({ type: 'result', phase, bps: 1e9 });
    assert.equal(h.elements.get('results').hidden, true);
    assert.equal(h.elements.get('latency').hidden, true);
    assert.equal(h.elements.get('result-details').hidden, true);
    assert.equal(h.elements.get('instrument').hidden, false);
    assert.equal(h.elements.get('progress').hidden, false);
  }
  worker.emit(complete);
  assert.equal(h.elements.get('instrument').hidden, true);
  assert.equal(h.elements.get('progress').hidden, true);
  for (const id of ['results', 'latency', 'result-details', 'download-timing', 'upload-timing', 'copy']) assert.equal(h.elements.get(id).hidden, false);
  assert.equal(h.elements.get('download-data').textContent, '2 GB');
  assert.equal(h.elements.get('upload-data').textContent, '1 GB');
  assert.equal(h.elements.get('download-duration').textContent, '13 s');
  assert.equal(h.elements.get('client-ip').textContent, '2001:db8::1234');
  h.click('retry'); assert.equal(h.elements.get('results').hidden, true);
  assert.equal(h.elements.has('cancel'), false);
  h.escape(); assert.equal(h.elements.get('results').hidden, true); assert.equal(h.elements.get('copy').hidden, true);
});

test('only live text and gauge are smoothed; final results stay independent', async () => {
  for (const reducedMotion of [false, true]) {
    const h = ui({ reducedMotion }); h.click('go'); const worker = h.workers[0];
    worker.emit({ type: 'phase', phase: 'download' }); h.draw(100);
    assert.equal(h.elements.get('live-value').textContent, '\u00a0');
    worker.emit({ type: 'sample', phase: 'download', bps: 10e9, elapsed: .2, phaseElapsed: 1.2, progress: .1 }); h.draw(200);
    assert.equal(h.elements.get('live-value').textContent, '10');
    worker.emit({ type: 'sample', phase: 'download', bps: 7e9, elapsed: .4, phaseElapsed: 1.4, progress: .2 }); h.draw(300);
    assert.equal(h.elements.get('live-value').textContent, '9.15');
    worker.emit({ type: 'result', phase: 'download', bps: 8.31e9 });
    assert.equal(h.elements.get('download-value').textContent, '8.31');
    worker.emit({ type: 'phase', phase: 'upload' }); h.draw(400);
    assert.equal(h.elements.get('live-value').textContent, '.');
    worker.emit({ type: 'sample', phase: 'upload', bps: 850, elapsed: .2, phaseElapsed: 1.2, progress: .1 }); h.draw(500);
    assert.equal(h.elements.get('live-value').textContent, '850');
    worker.emit({ type: 'result', phase: 'upload', bps: 900 }); worker.emit(complete);
    assert.equal(h.elements.get('live-value').textContent, '—');
    await h.click('copy'); assert.match(h.copied[0], /Download: 8.31 Gbps/); assert.match(h.copied[0], /Upload: 900 bps/);
    h.click('retry'); assert.equal(h.elements.get('live-value').textContent, '\u00a0');
  }
});

test('duration persists and each run retains its starting setting', () => {
  const h = ui(); h.click('settings-open');
  const form = h.elements.get('settings-form');
  assert.equal(form.elements.duration.value, 10);
  form.elements.duration.value = '20'; form.handlers.submit({ preventDefault() {} });
  h.click('go'); const worker = h.workers[0];
  assert.equal(worker.request.settings.duration, 20);
  form.elements.duration.value = '5'; form.handlers.submit({ preventDefault() {} });
  worker.emit({ type: 'phase', phase: 'download' });
  worker.emit({ type: 'sample', phase: 'download', bps: 1e9, phaseElapsed: 1.2, progress: .1 });
  assert.equal(worker.request.settings.duration, 20);
  const next = ui({ stored: h.saved() }); next.click('go');
  assert.equal(next.workers[0].request.settings.duration, 5);
});
test('Copy Results copies final measurements and supports the plain-HTTP fallback', async () => {
  for (const clipboard of [true, false]) {
    const h = ui({ clipboard }); h.click('go'); const worker = h.workers[0];
    worker.emit({ type: 'latency', ping: 1.2, jitter: .3 });
    worker.emit({ type: 'result', phase: 'download', bps: 125e9 }); worker.emit({ type: 'result', phase: 'upload', bps: 1e9 });
    worker.emit(complete); await h.click('copy');
    assert.equal(h.elements.get('copy').textContent, 'Copied');
    if (clipboard) { assert.match(h.copied[0], /Download: 125 Gbps/); assert.match(h.copied[0], /Total Data Used: 3 GB/); }
    else assert.deepEqual(h.copied, ['copy']);
  }
});


test('five gauge marks follow the existing scale and reset independently of measured results', () => {
  for (const [bps, marks] of [
    [850, ['0.25', '0.5', '0.75', '1']],
    [15e9, ['5', '10', '15', '20']],
    [125e9, ['50', '100', '150', '200']],
    [1.2e12, ['0.5', '1', '1.5', '2']]
  ]) {
    const h = ui({ reducedMotion: true }); h.click('go'); const worker = h.workers[0];
    worker.emit({ type: 'phase', phase: 'download' });
    worker.emit({ type: 'sample', bps, elapsed: .2, progress: .1 }); h.draw(200);
    assert.deepEqual(['scale-1', 'scale-2', 'scale-3', 'scale-label'].map(id => h.elements.get(id).textContent), marks);
    const needle = Number(h.elements.get('gauge-needle').style.transform.match(/rotate\(([-\d.]+)deg\)/)[1]);
    assert.ok(Number.isFinite(needle) && needle >= -120 && needle <= 120);
    worker.emit({ type: 'phase', phase: 'upload' });
    assert.equal(h.elements.get('gauge-needle').style.transform, 'rotate(-120deg)');
    assert.equal(h.elements.get('scale-label').textContent, '1');
    assert.equal(h.elements.get('scale-1').textContent, '0.25');
  }
});
