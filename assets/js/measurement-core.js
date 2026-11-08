(function (root) {
  'use strict';
  const units = ['bps', 'Kbps', 'Mbps', 'Gbps', 'Tbps'];
  const defaults = { mode: 'auto', connections: 32, duration: 10, theme: 'auto' };
  function settings(value = {}) {
    const integer = (key, min, max) => Number.isInteger(Number(value[key])) && Number(value[key]) >= min && Number(value[key]) <= max ? Number(value[key]) : defaults[key];
    // Preserve a supported common duration from settings saved by the previous UI.
    const duration = value.duration ?? (value.download === value.upload ? value.download : undefined);
    return { mode: ['auto', 'manual'].includes(value.mode) ? value.mode : defaults.mode,
      connections: integer('connections', 1, 128), duration: [5, 10, 20, 30].includes(Number(duration)) ? Number(duration) : defaults.duration,
      theme: ['auto', 'light', 'dark'].includes(value.theme) ? value.theme : 'auto' };
  }
  function rate(bytes, milliseconds) { return milliseconds > 0 ? bytes * 8000 / milliseconds : 0; }
  function format(bps, previous) {
    bps = Math.max(0, Number.isFinite(bps) ? bps : 0);
    let index = Math.min(4, Math.max(0, Math.floor(Math.log10(bps || 1) / 3)));
    if (previous !== undefined && previous >= 0 && previous <= 4 && index < previous && bps >= 0.9 * 1000 ** previous) index = previous;
    const number = bps / 1000 ** index;
    return { value: String(Number(number.toPrecision(3))), unit: units[index], index };
  }
  function scale(bps, current = 1e9) {
    while (bps > current * 0.9) {
      const power = 10 ** Math.floor(Math.log10(current));
      const step = current / power;
      current = (step < 1.5 ? 2 : step < 3 ? 5 : 10) * power;
    }
    return current;
  }
  const api = { defaults, settings, rate, format, scale };
  if (typeof module !== 'undefined') module.exports = api;
  else root.SpeedCore = api;
})(globalThis);
