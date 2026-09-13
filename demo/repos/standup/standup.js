#!/usr/bin/env node
// standup: the three lines you say every morning, kept in a file.
//
//   standup add "fixed the flaky login test"   append a line to today
//   standup                                    show today
//   standup week                               show the last seven days
//   standup copy                               today, ready to paste in chat

const fs = require('fs');
const path = require('path');

const FILE = process.env.STANDUP_FILE || path.join(process.env.HOME, '.standup.json');

function load() {
	try {
		return JSON.parse(fs.readFileSync(FILE, 'utf8'));
	} catch {
		return {};
	}
}

function save(days) {
	fs.writeFileSync(FILE, JSON.stringify(days, null, 1) + '\n');
}

function today() {
	return new Date().toISOString().slice(0, 10);
}

function show(days, dates) {
	for (const date of dates) {
		if (!days[date]?.length) continue;
		console.log(date);
		for (const line of days[date]) console.log('  - ' + line);
	}
}

function lastWeek() {
	const dates = [];
	for (let i = 6; i >= 0; i--) {
		const d = new Date(Date.now() - i * 86400000);
		dates.push(d.toISOString().slice(0, 10));
	}
	return dates;
}

const [cmd, ...rest] = process.argv.slice(2);
const days = load();

switch (cmd) {
	case undefined:
		show(days, [today()]);
		break;
	case 'add': {
		const line = rest.join(' ').trim();
		if (!line) {
			console.error('nothing to add');
			process.exit(1);
		}
		(days[today()] ||= []).push(line);
		save(days);
		break;
	}
	case 'week':
		show(days, lastWeek());
		break;
	case 'copy':
		console.log((days[today()] || []).map((l) => '• ' + l).join('\n'));
		break;
	default:
		console.error(`usage: standup [add "<line>" | week | copy]`);
		process.exit(2);
}
