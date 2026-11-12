// Makes the demo video out of script.md (what is said) and beats.mjs (what is
// shown meanwhile); see README.md.
//
//   node demo/video/make.mjs [voice] [shoot] [cut]    all three, by default
//
// voice  each beat's line as a clip: voice/<beat>.* where there is one (a
//        recording of your own), or else spoken by a synthetic voice through
//        OpenRouter, kept in out/voice/ until its line changes.
// shoot  TPS in a home of its own (home/), seeded afresh, driven through the
//        beats in Chromium and screen-captured to out/capture.mkv; agents run
//        on tape (see replay/), and tapes it did not have are added to
//        cassettes/.
// cut    each beat's stretch of the capture, sped up where it says so and at
//        least as long as its line, with the line and the overlays on top:
//        out/tps.mp4, a smaller out/tps-github.mp4 for the README, and the
//        lines as subtitles in out/tps.srt.
//
// $OPENROUTER_API_KEY is asked of TPS (/tps/guest-tool) where it is not set,
// and $CHROMIUM names the browser (/usr/bin/chromium by default).

import { chromium } from 'playwright-core';
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { cpSync, existsSync, globSync, mkdirSync, readdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { basename, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import beats from './beats.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, '../..');
const out = join(here, 'out');
const home = join(here, 'home');
const data = join(home, '.local/share/tps');

const [W, H] = [1920, 1080]; // the video
const VIEW = { width: 1440, height: 810 }; // the browser window, at W / 1440 device pixels per pixel
const FPS = 30;
const PORT = 8090;
const GAP = 0.6; // seconds of quiet after each line

/** The lines, by beat: the Voice column of script.md's tables. */
const lines = new Map();
for (const row of readFileSync(join(here, 'script.md'), 'utf8').split('\n')) {
	const m = row.match(/^\|\s*(\d+\.\d+)\s*\|([^|]*)\|/);
	if (m) lines.set(m[1], m[2].replace(/[*`]/g, '').trim());
}
const byId = (a, b) => a.id.localeCompare(b.id, 'en', { numeric: true });

const run = (cmd, args, opts = {}) => String(execFileSync(cmd, args, { stdio: ['ignore', 'pipe', 'inherit'], ...opts }) ?? '');
const secsOf = (file) => +run('ffprobe', ['-v', 'error', '-show_entries', 'format=duration', '-of', 'csv=p=0', file]);
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
const readJSON = (file) => JSON.parse(readFileSync(file, 'utf8'));

// --- voice ---------------------------------------------------------------------

const MODEL = 'elevenlabs/eleven-v4-turbo';
const VOICE = 'brian';

async function voice() {
	mkdirSync(join(out, 'voice'), { recursive: true });
	let key = process.env.OPENROUTER_API_KEY;
	const clips = {};
	for (const [id, text] of lines) {
		const own = existsSync(join(here, 'voice')) && readdirSync(join(here, 'voice')).find(f => f.replace(/\.[^.]+$/, '') === id);
		let file = own && join(here, 'voice', own);
		if (!file) {
			file = join(out, 'voice', createHash('sha256').update(`${MODEL} ${VOICE} ${text}`).digest('hex').slice(0, 16) + '.mp3');
			if (!existsSync(file)) {
				key ??= run('/tps/guest-tool', ['openrouter', '1']).trim().split('\n').pop();
				const res = await fetch('https://openrouter.ai/api/v1/audio/speech', {
					method: 'POST',
					headers: { authorization: `Bearer ${key}`, 'content-type': 'application/json' },
					body: JSON.stringify({ model: MODEL, voice: VOICE, input: text, response_format: 'mp3' }),
				});
				if (!res.ok) throw new Error(`speaking ${id}: ${res.status} ${await res.text()}`);
				writeFileSync(file, Buffer.from(await res.arrayBuffer()));
			}
		}
		clips[id] = { file, own: !!own, secs: secsOf(file) };
		console.log(`voice ${id}: ${clips[id].secs.toFixed(1)}s${own ? ' (yours)' : ''}`);
	}
	writeFileSync(join(out, 'voice.json'), JSON.stringify(clips, null, '\t'));
}

// --- shoot ---------------------------------------------------------------------

/**
 * The home the shoot runs TPS in: the demo's seed (demo/seed.sh), less the
 * project the story makes from nothing. The toolbox stays, being a download.
 */
function reset() {
	mkdirSync(home, { recursive: true });
	run(join(root, 'demo/seed.sh'), ['--force'], { env: { ...process.env, TPS_DEMO_HOME: home }, stdio: 'inherit' });
	const config = join(home, '.config/tps/projects.json');
	const c = readJSON(config);
	c.projects = c.projects.filter(p => p.name !== 'snip');
	writeFileSync(config, JSON.stringify(c, null, '\t'));
	rmSync(join(home, 'projects/snip'), { recursive: true, force: true });
	rmSync(join(data, 'tasks/snip'), { recursive: true, force: true });
	writeFileSync(join(home, '.gitconfig'), '[user]\n\tname = Robin Aalders\n\temail = robin@example.com\n[init]\n\tdefaultBranch = main\n');
	for (const dir of globSync(join(data, 'toolbox/*/replay'))) rmSync(dir, { recursive: true });
}

/**
 * Puts the replaying claude (see replay/) in the toolbox, with the tapes, as
 * soon as there is one: TPS downloads it when the first task wants it, before
 * that task's agent can start. Once a shoot, which reset() starts by taking
 * the tapes of the last one away.
 */
function wrapClaude() {
	const [bin] = globSync(join(data, 'toolbox/*/bin'));
	if (!bin || existsSync(join(bin, '../replay'))) return;
	const tapes = join(bin, '../replay.new');
	mkdirSync(tapes);
	for (const tape of globSync(join(here, 'cassettes/**/*.json'))) cpSync(tape, join(tapes, basename(tape)));
	renameSync(tapes, join(bin, '../replay'));
	if (!existsSync(join(bin, 'claude.real'))) renameSync(join(bin, 'claude'), join(bin, 'claude.real'));
	cpSync(join(out, 'claude'), join(bin, 'claude.new'));
	renameSync(join(bin, 'claude.new'), join(bin, 'claude'));
	console.log('shoot: agents run on tape');
}

/**
 * The tapes recorded in this shoot, into cassettes/: the host's in host/, and
 * each task's (those of the tasks TPS has archived since too) in
 * <project>/<task>/, so that a task is re-recorded by deleting its directory.
 */
function collect() {
	const archived = join(out, 'archived');
	rmSync(archived, { recursive: true, force: true });
	for (const archive of globSync(join(data, 'tasks/*/*/archive.tar.gz'))) {
		const to = join(archived, basename(dirname(dirname(archive))), basename(dirname(archive)));
		mkdirSync(to, { recursive: true });
		try {
			run('tar', ['-xzf', archive, '-C', to, 'claude/replay'], { stdio: 'ignore' });
		} catch { /* it had none */ }
	}
	const have = new Set(globSync(join(here, 'cassettes/**/*.json')).map(f => basename(f)));
	const groups = [[join(data, 'toolbox/*/replay/*.json'), () => 'host']];
	// The archived first: a follow-up task carries the tapes of the one it
	// follows, which are that one's.
	for (const tasks of [archived, join(data, 'tasks')]) {
		groups.push([join(tasks, '*/*/claude/replay/*.json'), tape => join(...tape.split('/').slice(-5, -3))]);
	}
	// In the order the tasks were made, for the same reason.
	const made = (tape) => tape.split('/').slice(-4, -3).map(Number)[0] || 0;
	let n = 0;
	for (const [pattern, group] of groups) {
		for (const tape of globSync(pattern).sort((a, b) => made(a) - made(b))) {
			if (have.has(basename(tape))) continue;
			have.add(basename(tape));
			mkdirSync(join(here, 'cassettes', group(tape)), { recursive: true });
			cpSync(tape, join(here, 'cassettes', group(tape), basename(tape)));
			n++;
		}
	}
	console.log(`shoot: ${n} new tapes in cassettes/`);
}

/** What the beats get to work with: the page, and ways to show it off. */
class Director {
	constructor(page) {
		this.page = page;
		this.cam = { x: 0, y: 0, s: 1 };
	}

	/** The project's task, as TPS last saved it. */
	task(tid, project = 'snip') {
		const c = readJSON(join(home, '.config/tps/projects.json'));
		return c.projects.find(p => p.name === project)?.tasks?.[tid] ?? {};
	}

	/** The chat log of the project's task, as far as TPS wrote it down. */
	chat(tid, project = 'snip') {
		const file = join(data, 'tasks', project, String(tid), 'chat.jsonl');
		return existsSync(file) ? readFileSync(file, 'utf8').split('\n').filter(Boolean).map(l => JSON.parse(l)) : [];
	}

	/** A file in the project's task's workspace, or '' while there is none. */
	file(tid, path, project = 'snip') {
		const file = join(data, 'tasks', project, String(tid), 'repo', path);
		return existsSync(file) ? readFileSync(file, 'utf8') : '';
	}

	/** Waits until test() holds, checking every `ms` milliseconds. */
	async until(test, minutes = 15, ms = 500) {
		for (const end = Date.now() + minutes * 60_000; Date.now() < end; await sleep(ms)) {
			try {
				if (await test()) return;
			} catch { /* not yet */ }
		}
		throw new Error(`waited ${minutes} minutes for ${test}`);
	}

	/** Waits until the task is in one of the phases. */
	phase(tid, ...phases) {
		return this.until(() => phases.includes(this.task(tid).phase));
	}

	/** The board column of that name. */
	column(name) {
		return this.page.locator('section', { has: this.page.locator('header', { hasText: name }) }).first();
	}

	/**
	 * The card of the task on the board on screen, by its title: the one claude
	 * gives it, which takes over from the plan's first words a moment after
	 * the task starts.
	 */
	async card(tid, project = 'snip') {
		let t;
		await this.until(() => {
			t = this.task(tid, project);
			return t.title && !t.title.endsWith('…') && t.title !== (t.description ?? '').split('\n')[0];
		}, 0.5).catch(() => {});
		return this.page.locator('section div[draggable]', { hasText: t.title }).first();
	}

	/** Goes to the page at the path, as the dashboard's own links do: without a reload. */
	async go(path) {
		await this.zoom();
		await this.page.evaluate(path => {
			history.pushState(null, '', path);
			dispatchEvent(new PopStateEvent('popstate'));
		}, path);
		await this.page.waitForTimeout(1500);
	}

	/**
	 * Eases the camera onto the element, `pad` pixels around it, or back to
	 * the whole window without one. The page itself is scaled, so text stays
	 * sharp however close it gets.
	 */
	async zoom(target, { pad = 40, max = 2.2, ms = 700 } = {}) {
		let to = { x: 0, y: 0, s: 1 };
		if (target) {
			await target.waitFor();
			const b = await target.boundingBox();
			// The box as it was before the camera moved there.
			const [x, y, w, h] = [(b.x - this.cam.x) / this.cam.s, (b.y - this.cam.y) / this.cam.s, b.width / this.cam.s, b.height / this.cam.s];
			const s = Math.max(1, Math.min(max, VIEW.width / (w + 2 * pad), VIEW.height / (h + 2 * pad)));
			to = {
				s,
				x: Math.min(0, Math.max(VIEW.width * (1 - s), VIEW.width / 2 - (x + w / 2) * s)),
				y: Math.min(0, Math.max(VIEW.height * (1 - s), VIEW.height / 2 - (y + h / 2) * s)),
			};
		}
		if (to.x === this.cam.x && to.y === this.cam.y && to.s === this.cam.s) return;
		this.cam = to;
		await this.page.evaluate(({ x, y, s, ms }) => {
			const ease = `${ms}ms cubic-bezier(.4,0,.2,1)`;
			Object.assign(document.documentElement.style, { transformOrigin: '0 0', transition: `transform ${ease}`, transform: `translate(${x}px, ${y}px) scale(${s})` });
		}, { ...to, ms });
		await this.page.waitForTimeout(ms + 100);
	}

	/**
	 * Glides the mouse pointer (one drawn in the page; a real one is not
	 * captured) onto the element. It is in the top layer, above dialogs, where
	 * neither the camera nor scrolling moves it: its place is the screen's.
	 */
	async point(target) {
		await target.waitFor();
		const b = await target.boundingBox();
		const [x, y] = [b.x + b.width / 2, b.y + b.height / 2];
		await this.page.evaluate(({ x, y }) => {
			let cursor = document.getElementById('tps-cursor');
			if (!cursor) {
				cursor = document.createElement('div');
				cursor.id = 'tps-cursor';
				cursor.popover = 'manual'; // in the top layer, so above dialogs too
				cursor.innerHTML = '<svg width="26" height="26" viewBox="0 0 24 24"><path d="M4 2l16 11-7 1-4 7z" fill="#fff" stroke="#000" stroke-width="1.5" stroke-linejoin="round"/></svg>';
				Object.assign(cursor.style, { position: 'fixed', inset: 'auto', margin: 0, padding: 0, border: 0, background: 'none', overflow: 'visible', pointerEvents: 'none', left: `${innerWidth / 2}px`, top: `${innerHeight}px`, transition: 'left 500ms ease, top 500ms ease' });
				document.documentElement.append(cursor);
			}
			// Again on top of whatever opened in the top layer since.
			if (cursor.matches(':popover-open')) cursor.hidePopover();
			cursor.showPopover();
			Object.assign(cursor.style, { left: `${x - 4}px`, top: `${y - 2}px` });
		}, { x, y });
		await this.page.waitForTimeout(600);
	}

	async click(target) {
		await target.scrollIntoViewIfNeeded();
		await this.point(target);
		await target.click();
		await this.page.waitForTimeout(400);
	}

	/**
	 * Types into the field, at a pace that reads as typing. Keystrokes now and
	 * then go astray, as something in the page takes the focus for a moment,
	 * so what the field ends up with is checked, and finished where it falls
	 * short.
	 */
	async type(target, text, delay = 25) {
		await this.click(target);
		await this.page.keyboard.type(text, { delay });
		for (let tries = 0; tries < 3; tries++) {
			const value = await target.inputValue();
			if (value === text) return;
			await target.focus();
			if (text.startsWith(value)) await this.page.keyboard.type(text.slice(value.length), { delay });
			else await target.fill(text);
		}
	}

	async press(keys) {
		await this.page.keyboard.press(keys);
		await this.page.waitForTimeout(400);
	}

	/** Rings the element until the beat ends. */
	async highlight(target) {
		await target.evaluate(el => {
			el.dataset.tpsHighlight = '';
			Object.assign(el.style, { outline: '3px solid #f5c542', outlineOffset: '4px', borderRadius: '4px' });
		});
	}

	/** A caption in the corner, placing the beat in time. */
	chip(text) {
		this.overlays.push({ kind: 'chip', text });
	}

	/** A label over the element, where it is now. */
	async label(text, target) {
		const b = await target.boundingBox();
		this.overlays.push({ kind: 'label', text, box: [b.x, b.y, b.width, b.height].map(v => v * W / VIEW.width) });
	}

	/** A title over the whole picture, the page dimmed beneath it. */
	title(html) {
		this.overlays.push({ kind: 'title', html });
	}
}

async function shoot() {
	const clips = readJSON(join(out, 'voice.json'));
	reset();
	console.log('shoot: building TPS');
	run('npm', ['run', 'build'], { cwd: root });
	run('go', ['build', '-o', join(out, 'tps'), '.'], { cwd: root });
	run('go', ['build', '-o', join(out, 'claude'), './demo/video/replay'], { cwd: root, env: { ...process.env, CGO_ENABLED: '0' } });
	mkdirSync(join(here, 'cassettes'), { recursive: true });
	// Commits of a fixed date are the same commits every time, so their ids
	// are too, and what an agent is told about them is what it was told on tape.
	const date = '2026-11-02T09:30:00Z';
	const tps = spawn(join(out, 'tps'), ['--port', PORT, '--no-open'], { env: { ...process.env, HOME: home, GIT_AUTHOR_DATE: date, GIT_COMMITTER_DATE: date }, stdio: 'ignore' });
	let done = false;
	tps.on('exit', code => {
		if (done) return;
		console.error(`shoot: TPS exited (${code}): is port ${PORT} taken?`);
		process.exit(1);
	});
	const wrapping = setInterval(wrapClaude, 100);

	const browser = await chromium.launch({ executablePath: process.env.CHROMIUM ?? '/usr/bin/chromium' });
	const page = await browser.newPage({ viewport: VIEW, deviceScaleFactor: W / VIEW.width });
	await page.goto(`http://localhost:${PORT}/`);
	await page.waitForTimeout(3000);

	// The capture: the browser's own screencast, its latest frame written out
	// FPS times a second whether it changed or not, so that a frame's place in
	// the video is the time it was on screen.
	const enc = spawn('ffmpeg', ['-y', '-loglevel', 'error', '-f', 'image2pipe', '-framerate', FPS, '-c:v', 'mjpeg', '-i', '-',
		'-vf', `scale=${W}:${H}`, '-c:v', 'libx264', '-preset', 'veryfast', '-crf', '16', '-pix_fmt', 'yuv420p', join(out, 'capture.mkv')], { stdio: ['pipe', 'inherit', 'inherit'] });
	const cdp = await page.context().newCDPSession(page);
	let frame, written = 0, t0;
	cdp.on('Page.screencastFrame', ({ data, sessionId }) => {
		frame = Buffer.from(data, 'base64');
		t0 ??= performance.now();
		void cdp.send('Page.screencastFrameAck', { sessionId }).catch(() => {});
	});
	await cdp.send('Page.startScreencast', { format: 'jpeg', quality: 92, maxWidth: W, maxHeight: H });
	const ticking = setInterval(() => {
		if (!frame) return;
		for (const due = Math.floor((performance.now() - t0) / 1000 * FPS); written < due; written++) enc.stdin.write(frame);
	}, 1000 / FPS / 2);
	const now = () => (performance.now() - t0) / 1000;
	while (!frame) await sleep(50);

	const d = new Director(page);
	const shot = [];
	try {
		for (const beat of beats) {
			if (typeof beat === 'function') { // off camera
				await beat(d);
				continue;
			}
			if (!lines.has(beat.id)) throw new Error(`beat ${beat.id} is not in script.md`);
			console.log(`shoot: ${beat.id} ${lines.get(beat.id)}`);
			d.overlays = [];
			const start = now();
			await beat.run(d);
			// The footage takes as long as the line does, sped up as the beat is.
			const speed = beat.ff ?? 1;
			while (now() - start < (clips[beat.id].secs + GAP) * (speed === 'fit' ? 1 : speed)) await sleep(50);
			for (const frame of page.frames()) { // VS Code's too
				await frame.evaluate(() => document.querySelectorAll('[data-tps-highlight]').forEach(el => { delete el.dataset.tpsHighlight; el.style.outline = ''; })).catch(() => {});
			}
			shot.push({ id: beat.id, start, end: now(), ff: speed, overlays: d.overlays });
		}
	} finally {
		clearInterval(ticking);
		enc.stdin.end();
		await new Promise(r => enc.on('close', r));
		writeFileSync(join(out, 'shoot.json'), JSON.stringify(shot, null, '\t'));
		await browser.close();
		clearInterval(wrapping);
		done = true;
		tps.kill();
		collect();
	}
}

// --- cut -----------------------------------------------------------------------

/** The overlays of a beat, drawn over a transparent frame. */
async function drawOverlays(page, beat, speed, file) {
	const corner = (side, text) => `<div style="position:absolute;${side}:40px;bottom:36px;padding:10px 20px;border-radius:12px;background:#000c;color:#fff;font:600 30px system-ui">${text}</div>`;
	const parts = beat.overlays.map(o => {
		if (o.kind === 'chip') return corner('left', o.text);
		if (o.kind === 'label') {
			const [x, y, w, h] = o.box;
			return `<div style="position:absolute;left:${x}px;top:${y}px;width:${w}px;height:${h}px;display:flex;align-items:center;justify-content:center"><span style="padding:12px 26px;border-radius:14px;background:#000d;color:#fff;font:700 44px system-ui">${o.text}</span></div>`;
		}
		return `<div style="position:absolute;inset:0;background:radial-gradient(circle, #000d 0%, #0009 45%, #0006 100%);display:flex;flex-direction:column;align-items:center;justify-content:center;gap:24px;color:#fff;font:600 40px system-ui;text-align:center">${o.html}</div>`;
	});
	const ff = '<svg width="34" height="22" viewBox="0 0 34 22" style="vertical-align:-3px;margin-right:10px"><path d="M0 0l16 11L0 22zM17 0l16 11-16 11z" fill="#fff"/></svg>';
	if (speed > 1.5) parts.push(corner('right', `${ff}${Math.round(speed)}×`));
	if (!parts.length) return false;
	const robot = `<img src="data:image/svg+xml;base64,${readFileSync(join(root, 'web/icon.svg')).toString('base64')}" style="width:220px">`;
	await page.setContent(`<body style="margin:0;width:${W}px;height:${H}px;position:relative;background:transparent">${parts.join('').replaceAll('{robot}', robot)}</body>`);
	await page.screenshot({ path: file, omitBackground: true });
	return true;
}

async function cut() {
	const clips = readJSON(join(out, 'voice.json'));
	const shot = readJSON(join(out, 'shoot.json')).sort(byId);
	const dir = join(out, 'beats');
	rmSync(dir, { recursive: true, force: true });
	mkdirSync(dir, { recursive: true });
	// How fast each beat's footage plays, and how long the beat lasts.
	for (const beat of shot) {
		const footage = beat.end - beat.start;
		const said = clips[beat.id].secs + GAP;
		beat.speed = beat.ff === 'fit' ? Math.max(1, footage / said) : beat.ff;
		beat.secs = Math.max(said, footage / beat.speed);
	}
	const browser = await chromium.launch({ executablePath: process.env.CHROMIUM ?? '/usr/bin/chromium' });
	const page = await browser.newPage({ viewport: { width: W, height: H } });
	const list = [], subs = [];
	let at = 0;
	for (const beat of shot) {
		const clip = clips[beat.id];
		const { speed, secs } = beat;
		// The stretches of the capture the beat shows: its own footage, or a
		// slice of each of the beats it is a montage of, around the point of
		// that beat its entry names ('3.2@0.9': nine tenths in), or its middle.
		const montage = beats.find(b => b.id === beat.id)?.montage;
		const parts = montage ? montage.map(entry => {
			const [id, at = 0.5] = entry.split('@');
			const from = shot.find(b => b.id === id);
			const len = secs / montage.length;
			const need = Math.min(len * from.speed, from.end - from.start);
			const start = Math.min(from.end - need, Math.max(from.start, from.start + (from.end - from.start) * at - need / 2));
			return { start, end: start + need, speed: from.speed, len };
		}) : [{ start: beat.start, end: beat.end, speed, len: secs }];
		const png = join(dir, `${beat.id}.png`);
		const over = await drawOverlays(page, beat, montage ? 1 : speed, png);
		// Overlays fade in and out, unless the beat beside them has the same
		// kinds: a title then only changes its words.
		const kinds = (b) => JSON.stringify(b?.overlays.map(o => o.kind));
		const same = (other) => kinds(other) === kinds(beat);
		const i = shot.indexOf(beat);
		const fadeIn = same(shot[i - 1]) ? 0 : 0.25, fadeOut = same(shot[i + 1]) ? 0 : 0.25;
		const n = parts.length;
		const graph = [
			...parts.map((p, i) => `[${i}:v]setpts=(PTS-STARTPTS)/${p.speed},fps=${FPS},tpad=stop_mode=clone:stop_duration=${p.len},trim=duration=${p.len},setpts=PTS-STARTPTS[p${i}]`),
			`${parts.map((p, i) => `[p${i}]`).join('')}concat=n=${n}:v=1:a=0[v]`,
			over ? `[${n + 1}:v]format=rgba${fadeIn ? `,fade=t=in:d=${fadeIn}:alpha=1` : ''}${fadeOut ? `,fade=t=out:st=${secs - fadeOut}:d=${fadeOut}:alpha=1` : ''}[o];[v][o]overlay=shortest=1[vo]` : '[v]null[vo]',
			// Your own recordings lose the rumble a synthetic voice does not
			// have; their level is set for the video as a whole, below.
			`[${n}:a]${clip.own ? 'highpass=f=80,' : ''}aresample=48000,apad,atrim=duration=${secs}[a]`,
		].join(';');
		run('ffmpeg', ['-y', '-loglevel', 'error',
			...parts.flatMap(p => ['-ss', p.start, '-to', p.end, '-i', join(out, 'capture.mkv')]),
			'-i', clip.file,
			...(over ? ['-loop', '1', '-framerate', FPS, '-t', secs, '-i', png] : []),
			'-filter_complex', graph, '-map', '[vo]', '-map', '[a]',
			'-c:v', 'libx264', '-preset', 'medium', '-crf', '18', '-pix_fmt', 'yuv420p', '-c:a', 'pcm_s16le', '-ac', '2',
			join(dir, `${beat.id}.mkv`)].map(String));
		list.push(`file '${beat.id}.mkv'`);
		const stamp = (s) => new Date(s * 1000).toISOString().slice(11, 23).replace('.', ',');
		subs.push(`${subs.length + 1}\n${stamp(at)} --> ${stamp(at + clip.secs)}\n${lines.get(beat.id)}\n`);
		console.log(`cut ${beat.id}: ${secs.toFixed(1)}s${montage ? `, a montage of ${montage.join(' ')}` : speed > 1 ? ` at ${speed.toFixed(1)}×` : ''}`);
		at += secs;
	}
	await browser.close();
	writeFileSync(join(dir, 'list.txt'), list.join('\n') + '\n');
	// One gain for the whole soundtrack, so that the lines keep the levels
	// they were recorded at: normalising each on its own goes wrong for the
	// short ones, which are shorter than the window loudness is measured over.
	const concat = ['-f', 'concat', '-safe', '0', '-i', join(dir, 'list.txt')];
	const measured = spawnSync('ffmpeg', ['-hide_banner', ...concat, '-vn', '-af', 'ebur128', '-f', 'null', '-'], { encoding: 'utf8' }).stderr;
	const loudness = +[...measured.matchAll(/I:\s+(-?[\d.]+) LUFS/g)].pop()[1];
	run('ffmpeg', ['-y', '-loglevel', 'error', ...concat, '-af', `volume=${(-16 - loudness).toFixed(1)}dB,alimiter=limit=0.89:level=false`,
		'-c:v', 'copy', '-c:a', 'aac', '-b:a', '192k', '-movflags', '+faststart', join(out, 'tps.mp4')]);
	writeFileSync(join(out, 'tps.srt'), subs.join('\n'));
	// GitHub plays a video in a README inline when it is uploaded there, up to
	// 10 MB on a free account.
	run('ffmpeg', ['-y', '-loglevel', 'error', '-i', join(out, 'tps.mp4'), '-vf', 'scale=1280:720', '-c:v', 'libx264', '-preset', 'slow', '-crf', '32', '-tune', 'stillimage',
		'-c:a', 'aac', '-b:a', '64k', '-ac', '1', '-movflags', '+faststart', join(out, 'tps-github.mp4')]);
	console.log(`cut: out/tps.mp4, ${at.toFixed(0)}s, out/tps-github.mp4, and out/tps.srt`);
}

const steps = process.argv.slice(2);
for (const [name, step] of Object.entries({ voice, shoot, cut })) {
	if (!steps.length || steps.includes(name)) await step();
}
