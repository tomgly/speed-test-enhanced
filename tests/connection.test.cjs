const { test } = require('node:test');
const assert = require('node:assert/strict');
const { origin, address, addresses, matches, select } = require('../assets/js/connection.js');
test('origins and IP addresses are validated without guessing', () => {
  assert.equal(origin('https://test.example/'), 'https://test.example');
  for (const input of ['javascript:alert(1)', 'http://user:pass@test.example', 'http://test.example/path', 'invalid']) assert.equal(origin(input), '');
  assert.equal(address('::ffff:192.0.2.1'), '192.0.2.1');
  assert.equal(address('::ffff:c000:201'), '192.0.2.1');
  assert.equal(address('2001:0db8::1'), '2001:db8::1');
  for (const input of ['example.com', '999.1.2.3', '1.2.3', '', null]) assert.equal(address(input), '');
  assert.deepEqual(addresses(() => null), { client: '', server: '' });
});
test('IPv6 preference requires actual client and server IPv6 metadata', async () => {
  const config = { ipv6Origin: 'http://v6.test', ipv4Origin: 'http://v4.test' };
  const route = await select(config, async () => ({ client: '2001:db8::20', server: '2001:db8::1' }));
  assert.equal(route.origin, 'http://v6.test'); assert.equal(route.family, 6);
  assert.ok(matches({ client: '::1', server: '::1' }, 6));
  for (const info of [{ client: '192.0.2.1', server: '2001:db8::1' }, { client: '', server: '' }]) {
    assert.equal((await select(config, async () => info)).origin, 'http://v4.test');
  }
  assert.equal((await select(config, async () => { throw new Error('unreachable'); })).origin, 'http://v4.test');
  assert.equal((await select({}, async () => assert.fail())).origin, '');
});
