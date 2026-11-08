(function (root) {
  'use strict';
  const core = typeof module !== 'undefined' ? require('./measurement-core.js') : root.SpeedCore;
  function data(bytes) {
    if (!Number.isFinite(bytes) || bytes < 0) return { value: '—', unit: '' };
    const index = bytes >= 1e12 ? 2 : bytes >= 1e9 ? 1 : 0;
    return { value: String(Number((bytes / 1000 ** (index + 2)).toPrecision(3))), unit: ['MB', 'GB', 'TB'][index] };
  }
  function duration(seconds) { return Number.isFinite(seconds) && seconds >= 0 ? `${Math.round(seconds)} s` : '—'; }
  // Display only: time-based EMA, independent of animation frame rate and measurement totals.
  class LiveSpeed {
    constructor() { this.reset(); }
    reset() { this.value = null; this.time = null; }
    update(bps, time) {
      if (!Number.isFinite(bps) || bps < 0 || !Number.isFinite(time) || (this.time !== null && time <= this.time)) return this.value;
      this.value = this.value === null ? bps : this.value + (bps - this.value) * -Math.expm1(-(time - this.time) / 600);
      this.time = time;
      return this.value;
    }
  }
  function text(result) {
    const speed = direction => { const n = core.format(result.speeds[direction]); return `${n.value} ${n.unit}`; };
    const bytes = direction => { const n = data(result.usage[direction].bytes); return `${n.value} ${n.unit} (${result.usage[direction].bytes} bytes)`; };
    const totalBytes = result.usage.download.bytes + result.usage.upload.bytes, total = data(totalBytes);
    return [
      `Download: ${speed('download')}`, `Upload: ${speed('upload')}`,
      `Download Data: ${bytes('download')}`, `Upload Data: ${bytes('upload')}`,
      `Total Data Used: ${total.value} ${total.unit} (${totalBytes} bytes)`,
      `Download Duration: ${duration(result.usage.download.duration)}`, `Upload Duration: ${duration(result.usage.upload.duration)}`,
      `Ping: ${result.ping} ms`, `Jitter: ${result.jitter} ms`,
      `Client IP: ${result.connection.client || 'Unavailable'}`, `Server IP: ${result.connection.server || 'Unavailable'}`,
      'Data is observed test payload; control traffic, unconfirmed data and protocol overhead are excluded. Durations include calibration and warm-up.',
    ].join('\n');
  }
  const api = { data, duration, text, LiveSpeed };
  if (typeof module !== 'undefined') module.exports = api;
  else root.SpeedResults = api;
})(globalThis);
