import { test } from 'node:test';
import * as assert from 'node:assert/strict';
import * as fs from 'fs';
import * as path from 'path';
import { MASK, redact, redactValue, revealValue, Vault } from '../redact';

/** The TypeScript port must mask exactly like pkg/redact (same table), and the vault must round-trip. */

const cases: { name: string; in: string; want: string }[] = JSON.parse(fs.readFileSync(path.resolve(__dirname, '../../../../pkg/redact/testdata/cases.json'), 'utf8')).string;

test('redact: same results as pkg/redact (Go) for every shared case', () => {
  assert.ok(cases.length >= 30);
  for (const c of cases) assert.equal(redact(c.in), c.want, c.name);
});

test('redact: idempotent', () => {
  const once = redact(`password=x Authorization: Bearer abc "token":"y" https://u:p@h/x`);
  assert.equal(redact(once), once);
});

test('vault: placeholders instead of [REDACTED], same value → same placeholder, reveal restores', () => {
  const v = new Vault();
  const key = 'aGVsbG8gd29ybGQgdGhpcyBpcyBhIHRlc3Qga2V5IDE=';
  const text = `[Interface]\nPrivateKey = ${key}\nAddress = 10.64.0.2/32\n[Peer]\nPublicKey = ${key}\nEndpoint = 95.173.222.2:51820`;
  const red = redact(text, v);
  assert.ok(!red.includes(key), 'value hidden');
  assert.ok(!red.includes(MASK));
  assert.match(red, /PrivateKey = «s1»/);
  assert.match(red, /Endpoint = 95\.173\.222\.2:51820/, 'addresses stay');
  assert.equal(v.reveal(red), text);
  assert.equal(redact(red, v), red, 'placeholders are not masked again');
  assert.deepEqual(v.labels('use «s1» and «s9»'), [{ name: '«s1»', label: 'PrivateKey' }]);
  // Round-trip through storage.
  assert.equal(new Vault(v.entries()).reveal('«s1»'), key);
});

test('vault: JSON values (Remnawave answers) and bodies of a plan', () => {
  const v = new Vault();
  const uuid = '3f2a1b4c-5d6e-7f80-91a2-b3c4d5e6f708';
  const red = redactValue({ uuid, name: 'Iran-Mullvad', privateKey: 'abc', nested: { shortIds: ['a1', 'b2'] } }, v) as any;
  assert.equal(red.name, 'Iran-Mullvad');
  assert.match(red.uuid, /^«s\d+»$/);
  assert.ok(!JSON.stringify(red).includes(uuid) && !JSON.stringify(red).includes('"abc"') && !JSON.stringify(red).includes('a1'));
  const back = revealValue({ path: `/api/nodes/${red.uuid}`, body: { shortIds: red.nested.shortIds, privateKey: red.privateKey } }, v) as any;
  assert.equal(back.path, `/api/nodes/${uuid}`);
  assert.deepEqual(back.body.shortIds, ['a1', 'b2'], 'arrays come back as arrays');
  assert.equal(back.body.privateKey, 'abc');
});
