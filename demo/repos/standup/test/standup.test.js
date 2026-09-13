const { test } = require('node:test');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

function run(file, ...args) {
	return execFileSync('node', [path.join(__dirname, '..', 'standup.js'), ...args], {
		env: { ...process.env, STANDUP_FILE: file },
		encoding: 'utf8',
	});
}

test('a line added shows up in today', () => {
	const file = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'standup-')), 'notes.json');
	run(file, 'add', 'reviewed the release notes');
	assert.match(run(file), /reviewed the release notes/);
});

test('copy prints bullets', () => {
	const file = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'standup-')), 'notes.json');
	run(file, 'add', 'paired on the parser');
	assert.strictEqual(run(file, 'copy').trim(), '• paired on the parser');
});
