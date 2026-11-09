'use strict';
const { settings, defaults, format, scale } = SpeedCore;
const $ = id => document.getElementById(id);
let config = { ...defaults };
try { config = settings(JSON.parse(localStorage.getItem('100g-settings')) || {}); } catch { /* Storage is optional. */ }
const systemTheme = matchMedia('(prefers-color-scheme: dark)');
const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');
let worker = null, busy = false, phase = '', displaySpeed = 0, animatedSpeed = 0;
const liveSpeed = new SpeedResults.LiveSpeed();
let gaugeScale = 1e9, tickScale = 0, liveUnit, frame = 0, previousFrame = 0, lastText = 0;
let report = null, copyTimer;
let runConfig;
let waitingSince = 0;
function theme() {
  document.documentElement.dataset.theme = config.theme === 'auto' ? (systemTheme.matches ? 'dark' : 'light') : config.theme;
}
systemTheme.addEventListener('change', theme);
function result(direction, bps) {
  const value = format(bps);
  $(`${direction}-value`).textContent = value.value;
  $(`${direction}-unit`).textContent = value.unit;
  $(`${direction}-result`).hidden = false;
}
function drawScale() {
  if (tickScale === gaugeScale) return;
  tickScale = gaugeScale;
  const top = format(gaugeScale);
  for (let i = 1; i < 4; i++) $(`scale-${i}`).textContent = String(Number((Number(top.value) * i / 4).toPrecision(3)));
  $('scale-label').textContent = top.value;
}
function showLive(bps) {
  const value = format(bps, liveUnit); liveUnit = value.index;
  $('live-value').classList.remove('waiting');
  $('live-value').textContent = value.value; $('live-unit').textContent = value.unit;
}
function resetLive(waiting = busy) {
  liveSpeed.reset(); displaySpeed = animatedSpeed = 0;
  gaugeScale = 1e3; liveUnit = undefined; previousFrame = lastText = 0;
  waitingSince = performance.now();
  $('live-value').classList.toggle('waiting', waiting);
  $('live-value').textContent = waiting ? '\u00a0' : '—'; $('live-unit').textContent = '';
  $('gauge-fill').style.strokeDashoffset = '100'; $('gauge-needle').style.transform = 'rotate(-120deg)'; drawScale();
}
function animate(time) {
  if (!busy) { frame = 0; return; }
  if (liveSpeed.value === null) {
    // A non-breaking space preserves the text baseline during the empty step.
    $('live-value').textContent = '.'.repeat(Math.floor((time - waitingSince) / 400) % 4) || '\u00a0';
    frame = requestAnimationFrame(animate); return;
  }
  const dt = Math.min(100, time - (previousFrame || time)); previousFrame = time;
  animatedSpeed += (displaySpeed - animatedSpeed) * (reducedMotion.matches ? 1 : 1 - Math.exp(-dt / 180));
  gaugeScale = scale(Math.max(animatedSpeed, displaySpeed), gaugeScale);
  const fraction = Math.max(0, Math.min(1, animatedSpeed / gaugeScale));
  $('gauge-fill').style.strokeDashoffset = String(100 - fraction * 100);
  $('gauge-needle').style.transform = `rotate(${-120 + fraction * 240}deg)`;
  if (time - lastText >= 100) {
    showLive(displaySpeed);
    drawScale();
    lastText = time;
  }
  frame = requestAnimationFrame(animate);
}
function finish(message, state = 'interrupted') {
  busy = false; worker?.terminate(); worker = null;
  cancelAnimationFrame(frame); frame = 0;
  resetLive();
  $('retry').hidden = false; $('progress').hidden = true;
  $('status').textContent = message; $('settings-open').disabled = false;
  $('status').classList.remove('sr-only');
  document.body.dataset.state = state;
  $('instrument').hidden = true;
  $('result-actions').hidden = false;
  $('copy').hidden = state !== 'complete';
  $('retry').focus();
}
function resetResults() {
  report = { speeds: {}, usage: null, connection: {}, ping: null, jitter: null };
  for (const id of ['results', 'throughput', 'latency', 'download-result', 'upload-result', 'result-details', 'result-actions', 'copy', 'download-timing', 'upload-timing']) $(id).hidden = true;
  for (const direction of ['download', 'upload']) { $(`${direction}-value`).textContent = '—'; $(`${direction}-unit`).textContent = 'Mbps'; }
  $('ping-value').textContent = $('jitter-value').textContent = '—';
  clearTimeout(copyTimer); $('copy').textContent = 'Copy Results';
}
function showDetails(data) {
  report.usage = data.usage; report.connection = data.connection;
  for (const direction of ['download', 'upload']) {
    const n = SpeedResults.data(data.usage[direction].bytes);
    $(`${direction}-data`).textContent = `${n.value} ${n.unit}`;
    $(`${direction}-duration`).textContent = SpeedResults.duration(data.usage[direction].duration);
    $(`${direction}-timing`).hidden = false;
  }
  $('client-ip').textContent = data.connection.client || '—'; $('server-ip').textContent = data.connection.server || '—';
  $('results').hidden = false; $('throughput').hidden = false;
  $('latency').hidden = false; $('result-details').hidden = false;
}
function start() {
  if (busy) return;
  resetResults();
  runConfig = { ...config };
  resetLive(true);
  document.body.dataset.state = 'running';
  $('instrument').hidden = false;
  $('go').hidden = true; $('live').hidden = false; $('retry').hidden = true;
  $('progress').hidden = false; $('progress').value = 0;
  $('settings-open').disabled = true; $('status').textContent = 'Connecting…';
  $('status').classList.add('sr-only');
  $('phase-label').textContent = 'Connecting';
  phase = ''; document.body.dataset.phase = '';
  busy = true; $('instrument').focus();
  try {
    worker = new Worker('assets/js/measurement-worker.js');
    worker.onerror = () => finish('Measurement could not start. Check browser support and server access.');
    worker.onmessage = ({ data }) => {
      if (!busy) return;
      if (data.type === 'phase') {
        phase = data.phase; document.body.dataset.phase = phase; resetLive();
        $('progress').value = 0;
        $('phase-label').textContent = phase === 'ping' ? 'Measuring latency' : phase === 'download' ? 'Download' : 'Upload';
        $('status').textContent = phase === 'ping' ? 'Measuring ping and jitter…' : `Measuring ${phase}…`;
      } else if (data.type === 'latency') {
        report.ping = Number(data.ping.toFixed(2)); report.jitter = Number(data.jitter.toFixed(2));
        $('ping-value').textContent = String(Number(data.ping.toFixed(2)));
        $('jitter-value').textContent = String(Number(data.jitter.toFixed(2)));
      } else if (data.type === 'sample') {
        displaySpeed = liveSpeed.update(data.bps, data.elapsed * 1000) ?? 0;
        $('phase-label').textContent = phase === 'download' ? 'Download' : 'Upload';
        // Worker progress uses this direction's measured elapsed time and duration,
        // excluding calibration/warm-up and reserving 100% for confirmed completion.
        $('progress').value = Math.max(0, Math.min(.99, data.progress));
      } else if (data.type === 'result') {
        report.speeds[data.phase] = data.bps;
        result(data.phase, data.bps);
        $('progress').value = 1;
        liveSpeed.reset(); displaySpeed = liveSpeed.update(data.bps, 0);
        showLive(data.bps);
      } else if (data.type === 'complete') {
        showDetails(data);
        finish('', 'complete');
      }
      else if (data.type === 'restart') {
        resetResults(); resetLive(); $('progress').value = 0;
        phase = ''; document.body.dataset.phase = '';
        $('phase-label').textContent = 'Connecting'; $('status').textContent = 'Connecting…';
      }
      else if (data.type === 'error') finish(data.message);
      else if (data.type === 'cancelled') finish('Test cancelled');
    };
    worker.postMessage({ type: 'start', settings: runConfig });
    frame = requestAnimationFrame(animate);
  } catch { finish('This browser could not start the measurement worker.'); }
}
function cancel() {
  if (!busy) return;
  worker.postMessage({ type: 'cancel' });
  // Terminating also closes networking if the worker is stalled.
  finish('Test cancelled');
}
$('go').addEventListener('click', start); $('retry').addEventListener('click', start);
document.addEventListener('keydown', event => { if (event.key === 'Escape' && busy) cancel(); });
const form = $('settings-form');
function modeLabel() { $('connections-label').textContent = form.elements.mode.value === 'auto' ? 'Maximum parallel requests' : 'Parallel requests'; }
$('settings-open').addEventListener('click', () => {
  for (const [key, value] of Object.entries(config)) form.elements[key].value = value;
  modeLabel(); $('settings-dialog').showModal();
});
form.elements.mode.addEventListener('change', modeLabel);
$('settings-close').addEventListener('click', () => $('settings-dialog').close());
form.addEventListener('submit', event => {
  event.preventDefault(); config = settings(Object.fromEntries(new FormData(form)));
  try { localStorage.setItem('100g-settings', JSON.stringify(config)); } catch { /* Storage is optional. */ }
  theme(); $('settings-dialog').close();
});

$('copy').addEventListener('click', async () => {
  if (!report?.usage || busy) return;
  const text = SpeedResults.text(report);
  try {
    if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(text);
    else {
      const field = document.createElement('textarea');
      field.value = text; field.readOnly = true; field.className = 'clipboard-field';
      document.body.append(field); field.select();
      try { if (!document.execCommand('copy')) throw new Error('Copy unavailable'); }
      finally { field.remove(); $('copy').focus(); }
    }
    $('copy').textContent = 'Copied';
    clearTimeout(copyTimer); copyTimer = setTimeout(() => { $('copy').textContent = 'Copy Results'; }, 1500);
  } catch { $('status').textContent = 'Could not copy results. Check browser clipboard permissions.'; }
});
theme();
