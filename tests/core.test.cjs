const { test } = require('node:test');
const assert = require('node:assert/strict');
const { rate, format, scale, settings } = require('../assets/js/measurement-core.js');
test('SI units and precision through terabits', () => {
  for (const [bps, value, unit] of [[850, '850', 'bps'], [850e3, '850', 'Kbps'], [850e6, '850', 'Mbps'], [1.25e9, '1.25', 'Gbps'], [10.5e9, '10.5', 'Gbps'], [98.7e9, '98.7', 'Gbps'], [125e9, '125', 'Gbps'], [1.2e12, '1.2', 'Tbps']]) {
    assert.equal(format(bps).value, value); assert.equal(format(bps).unit, unit);
  }
  assert.equal(rate(150e9, 12000), 100e9);
  assert.equal(rate(187.5e9, 12000), 125e9);
  assert.equal(rate(1800e9, 12000), 1.2e12);
  assert.equal(rate(1, 0.125), 64000);
  assert.equal(rate(1, 0), 0);
});
test('live units and scale avoid boundary oscillation', () => {
  assert.equal(format(999e6, 3).unit, 'Gbps');
  assert.equal(format(899e6, 3).unit, 'Mbps');
  assert.equal(scale(125e9, 1e9), 2e11);
  assert.equal(scale(1e9, 1e12), 1e12);
});
test('settings validate limits including 64 and 128', () => {
  for (const connections of [1, 64, 128]) assert.equal(settings({ connections }).connections, connections);
  assert.equal(settings({ connections: 129 }).connections, 32);
  assert.equal(settings({ duration: -1, mode: 'bad' }).duration, 10);
});

test('shared duration accepts only supported intervals and migrates saved settings', () => {
  for (const duration of [5, 10, 20, 30]) assert.equal(settings({ duration: String(duration) }).duration, duration);
  for (const duration of [0, 12, 60, '', null]) assert.equal(settings({ duration }).duration, 10);
  assert.equal(settings({ download: 5, upload: 5 }).duration, 5);
  assert.equal(settings({ download: 5, upload: 30 }).duration, 10);
  assert.equal(settings({ download: 12, upload: 12 }).duration, 10);
});
