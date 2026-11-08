const { test } = require('node:test');
const assert = require('node:assert/strict');
const { data, duration, text, LiveSpeed } = require('../assets/js/results.js');
test('data formatting uses decimal bytes and does not mutate raw totals', () => {
  for (const [bytes, value, unit] of [[1e6, '1', 'MB'], [850e6, '850', 'MB'], [1e9, '1', 'GB'], [1.25e9, '1.25', 'GB'], [1e12, '1', 'TB']]) {
    assert.deepEqual(data(bytes), { value, unit });
  }
  assert.equal(data(null).value, '—'); assert.equal(data(-1).value, '—');
  assert.equal(duration(13.456), '13 s'); assert.equal(duration(13.5), '14 s');
});
test('copied results include independent directions, exact total bytes and measured addresses', () => {
  const report = { speeds: { download: 125e9, upload: 1.2e12 }, ping: 1.2, jitter: .4,
    usage: { download: { bytes: 999999999, duration: 13.25 }, upload: { bytes: 1, duration: 14.1 } },
    connection: { client: '2001:db8::20', server: '2001:db8::1' } };
  const before = JSON.stringify(report), copied = text(report);
  for (const expected of ['Download: 125 Gbps', 'Upload: 1.2 Tbps', '1000000000 bytes', 'Download Duration: 13 s',
    'Upload Duration: 14 s', 'Ping: 1.2 ms', 'Jitter: 0.4 ms', 'Client IP: 2001:db8::20', 'Server IP: 2001:db8::1']) assert.ok(copied.includes(expected), expected);
  assert.equal(JSON.stringify(report), before);
  report.connection = {}; assert.ok(text(report).includes('Client IP: Unavailable'));
});

test('live EMA reduces short spikes while following sustained changes at any speed', () => {
  for (const speed of [850, 850e3, 1e9, 100e9, 1.2e12]) {
    const live = new LiveSpeed();
    assert.equal(live.value, null); assert.equal(live.update(speed, 200), speed);
    const next = live.update(speed * .7, 400);
    assert.ok(next > speed * .9 && next < speed);
    for (let time = 600; time <= 1600; time += 200) live.update(speed * .7, time);
    assert.ok(live.value >= speed * .7 && live.value < speed * .74);
    live.reset(); assert.equal(live.value, null);
    assert.equal(live.update(0, 200), 0);
  }
});

test('live EMA uses sample elapsed time, ignores stale values and never overshoots', () => {
  const a = new LiveSpeed(), b = new LiveSpeed();
  a.update(10e9, 0); b.update(10e9, 0);
  a.update(7e9, 100); a.update(7e9, 200); b.update(7e9, 200);
  assert.ok(Math.abs(a.value - b.value) < .001);
  const value = a.value;
  for (const [speed, time] of [[1e12, 200], [1e12, 100], [NaN, 300], [-1, 300], [1, NaN]]) assert.equal(a.update(speed, time), value);
  assert.ok(a.update(1e12, 5000) > 990e9); assert.ok(a.value <= 1e12);
  assert.ok(a.update(0, 10000) >= 0);
});
