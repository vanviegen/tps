// What the demo video shows, beat by beat, in the order it is shot; the lines
// said meanwhile are script.md's, and the video puts the beats in the order of
// their ids. A beat's `run` does what is seen, with `d`, the director (see
// make.mjs): d.page is the Playwright page, and d.click, d.type and d.zoom show
// what they do. `ff` speeds a beat's footage up: by that much, or by whatever
// fits it to its line ('fit'), and `montage` shows slices of other beats'
// footage instead of its own. Plain functions in between run off camera.
//
// Tasks are known by number, in the order they are made: snip/1 is the first
// task the story makes, and so on.

const PLANS = {
	1: 'A URL shortener in one Python file, snip.py, standard library only: POST a link to get a short key back, GET the key to be redirected, and a small form at / to try it. Also a Containerfile.dev, built on the default one from TPS, that runs it as the app on port 8000.',
	2: 'Keep the links in a JSON file, so a restart does not forget them.',
	3: 'Let a link have a name of its own: a name given with the POST becomes its key, and one that is taken gets a 409.',
	4: 'Tests for snip.py with pytest, run as the project\'s test in Containerfile.dev. And a deploy.sh as its deploy, which for now just prints the commit it ships. Give the project a version too: 0.1.0, in a VERSION file, with a CHANGELOG.md.',
	5: 'A /stats page: links made per day, and the ten most followed. Tested like the rest.',
};

import { readFileSync } from 'node:fs';

const project = '/p/local:snip';

/** What the video opens with, over a montage of what is to come. */
const TITLE = '{robot}<div style="font-size:96px;font-weight:800">TPS</div>';

/** The README's last word, as a data URL for a title. */
const REPORTS = `data:image/jpeg;base64,${readFileSync(new URL('../../doc/tps-reports.jpg', import.meta.url)).toString('base64')}`;

/** VS Code, in the task on screen, once it is up. */
async function vscode(d) {
	const code = d.page.frameLocator('iframe:visible').first();
	await code.locator('.monaco-workbench').waitFor({ timeout: 600_000 });
	return code;
}

/**
 * Opens the file in the task's VS Code, from its Explorer: again, where VS
 * Code went away meanwhile, its container being made anew.
 */
async function open(d, file) {
	for (let tries = 1; ; tries++) {
		try {
			const code = await vscode(d);
			const row = code.locator('.monaco-list-row', { has: code.locator('.label-name', { hasText: new RegExp(`^${file.replace('.', '\\.')}$`) }) });
			await row.waitFor({ timeout: 120_000 });
			await d.click(row);
			await d.page.waitForTimeout(1000);
			return code;
		} catch (e) {
			if (tries === 5) throw e;
			await d.page.waitForTimeout(5000);
		}
	}
}

/** The project's default task settings, in its settings dialog. */
async function defaults(d, set) {
	await d.go(project);
	await d.page.getByRole('button', { name: 'Settings', exact: true }).click();
	const field = (label) => d.page.getByRole('dialog').locator('.s-field', { hasText: label }).last();
	await field('On agent ready').scrollIntoViewIfNeeded();
	await set(field);
}

/**
 * Clicks with the mouse, where it is: VS Code's activity bar and lists act on
 * mouse events of their own, which an element's click does not give them.
 */
async function tap(d, target) {
	await d.point(target);
	const b = await target.boundingBox();
	await d.page.mouse.click(b.x + Math.min(10, b.width / 2), b.y + b.height / 2);
	await d.page.waitForTimeout(600);
}

/** Is a tool of this name at work in the task, its call made and its result not in yet? */
function running(d, tid, name) {
	const chat = d.chat(tid);
	return chat.some(e => e.k === 'tool' && e.name === name && !chat.some(r => r.id === e.id && r.res));
}

/** Has the chat of the task said something of this kind yet? */
const said = (d, tid, kind) => d.chat(tid).some(e => e.k === kind);

/**
 * Makes a task on the board on screen, and waits for its VS Code: starting,
 * VS Code takes the focus for a moment, and what is typed meanwhile is lost.
 */
async function create(d) {
	await d.click(d.page.getByRole('button', { name: 'Create task', exact: true }).first());
	await vscode(d);
}

/** A task made on the board on screen, its plan typed and sent to the agent. */
async function plan(d, tid) {
	await create(d);
	await d.type(d.page.getByPlaceholder('Message the agent…'), PLANS[tid], 10);
	await sent(d, tid);
}

/** Sends the plan on screen to the agent, until the task has left Plan. */
async function sent(d, tid) {
	for (let tries = 1; ; tries++) {
		await d.press('Control+Enter');
		try {
			return await d.until(() => d.task(tid).phase !== 'plan' || d.task(tid).waitFor, 0.1);
		} catch (e) {
			if (tries === 3) throw e;
		}
	}
}

/** A line of the file open in the task's VS Code, ringed, with the camera on it. */
async function ring(d, code, text) {
	const line = code.locator('.view-line', { hasText: text }).first();
	await d.zoom(line, { pad: 160, max: 2 });
	await d.highlight(line);
}

export default [
	async d => d.go('/'),

	// Shot first, on the board the story starts from.
	// The title over a glimpse of what is to come.
	{ id: '0.1', montage: ['2.1@0.8', '4.1@0.35', '5.1@0.3', '3.6@0.92', '3.3@0.9', '4.3@0.7', '7.3@0.8', '6.3@0.75'], async run(d) {
		d.title(TITLE);
	} },

	{ id: '0.2', montage: ['1.2@0.6', '3.2@0.3', '2.2@0.4', '1.5@0.9', '3.1', '3.5@0.8', '4.4@0.6', '7.4@0.6', '7.5@0.6', '7.6@0.6'], async run(d) {
		d.title(TITLE);
	} },

	async d => d.click(d.page.getByRole('link', { name: /^localhost/ })),

	// Shot on the localhost page, before 1.1 makes the project there.
	{ id: '1.3', async run(d) {
		const signIn = d.page.getByRole('button', { name: 'Sign in to claude' });
		await d.zoom(signIn.locator('xpath=..'), { pad: 40, max: 1.8 }); // the Claude account row
		await d.point(signIn);
		await d.highlight(signIn);
	} },

	{ id: '1.1', async run(d) {
		await d.zoom();
		await d.click(d.page.getByRole('button', { name: 'Add project…' }));
		await d.zoom(d.page.getByRole('dialog'), { max: 1.6 });
		await d.type(d.page.getByLabel('Directory'), '~/projects/snip');
		await d.press('Enter');
		await d.click(d.page.getByRole('button', { name: 'OK' }));
		await d.zoom();
	} },

	// The first task comes to the user, and runs on $MODEL where that is set.
	async d => defaults(d, async field => {
		if (process.env.MODEL) await field('Agent model').locator('select').first().selectOption(process.env.MODEL);
		await field('On agent ready').locator('button').nth(0).click();
		await d.page.waitForTimeout(1000);
		await d.press('Escape');
	}),

	async d => create(d),

	{ id: '1.2', async run(d) {
		const box = d.page.getByPlaceholder('Message the agent…');
		await d.zoom(box, { pad: 120, max: 1.8 });
		await d.type(box, PLANS[1], 18);
		await sent(d, 1);
	} },

	{ id: '1.4', ff: 'fit', async run(d) {
		await d.zoom();
		await d.until(() => said(d, 1, 'tool'));
	} },

	{ id: '2.1', ff: 'fit', async run(d) {
		await d.label('agent', d.page.locator('.tps-grip').locator('xpath=preceding-sibling::div[1]'));
		await d.label('VS Code', d.page.locator('iframe:visible').first());
		await d.until(() => d.chat(1).some(e => e.k === 'tool' && /snip\.py/.test(e.arg ?? '')));
		await open(d, 'snip.py');
	} },

	{ id: '2.2', ff: 'fit', async run(d) {
		const box = d.page.getByPlaceholder('Message the agent…');
		await d.zoom(box, { pad: 160, max: 1.6 });
		await d.type(box, 'Make the keys four characters, base62.');
		// Sent while a command of the agent's runs, so that it reaches the
		// agent with what that command answers: at the same point of the
		// conversation each time, which replaying it needs.
		await d.until(() => running(d, 1, 'Bash'), 15, 50);
		await d.press('Control+Enter');
		await d.zoom();
		const before = d.chat(1).length;
		await d.until(() => d.chat(1).slice(before).some(e => e.k === 'text'));
	} },

	async d => {
		await d.phase(1, 'human');
		await d.go(project);
	},

	{ id: '3.2', async run(d) {
		await d.click(d.page.getByRole('button', { name: 'Settings', exact: true }));
		const dialog = d.page.getByRole('dialog');
		const accept = dialog.locator('.s-field', { hasText: 'On review accept' }).last();
		await accept.scrollIntoViewIfNeeded();
		await d.zoom(accept, { pad: 120, max: 1.8 });
		await d.highlight(accept.locator('button').nth(1));
		await d.page.waitForTimeout(1500);
		const deploy = dialog.getByText('Deploy after every merge');
		await deploy.scrollIntoViewIfNeeded();
		await d.zoom(deploy, { pad: 120, max: 1.8 });
		await d.highlight(deploy);
		await d.page.waitForTimeout(1500);
		await d.press('Escape');
		await d.zoom(await d.card(1), { max: 2 });
	} },

	{ id: '3.3', async run(d) {
		await d.zoom();
		await d.click(await d.card(1));
		const code = await vscode(d);
		await tap(d, code.locator('.action-item a[aria-label^="Source Control"]').first());
		await tap(d, code.locator('.scm-view .monaco-list-row .label-name', { hasText: /^snip\.py$/ }).first());
		await d.zoom(d.page.locator('iframe:visible').first(), { pad: 0 });
	} },

	{ id: '3.4', async run(d) {
		const code = await vscode(d);
		await d.zoom(code.locator('.scm-view .monaco-list-row', { hasText: /^snip\.py/ }).first(), { pad: 140, max: 1.8 });
	} },

	// The chat's save points, each a commit of that branch.
	{ id: '3.5', async run(d) {
		const points = d.page.getByText('Save point');
		await points.first().scrollIntoViewIfNeeded();
		await d.zoom(points.first(), { pad: 260, max: 1.6 });
		for (const point of await points.all()) await d.highlight(point.locator('xpath=..'));
	} },

	// Shot here, where the first task waits for the user with its save points
	// still to go back to.
	{ id: '7.3', async run(d) {
		await d.go(`${project}/t/1`);
		const point = d.page.getByText('Save point').last();
		await point.scrollIntoViewIfNeeded();
		await d.zoom(point, { pad: 200, max: 1.8 });
		await d.click(point);
		await d.zoom(d.page.getByRole('dialog'), { pad: 20 });
		await d.page.waitForTimeout(2000);
		await d.press('Escape');
	} },

	async d => d.go(project),

	// The agent left the app running; stopped, it is started on camera, and
	// VS Code sees its port open.
	async d => {
		await d.click(await d.card(1));
		await d.page.getByLabel('Task menu').click();
		await d.page.locator('.s-menu-item', { hasText: 'app' }).first().click();
		const stop = d.page.getByRole('dialog').getByRole('button', { name: 'Stop' });
		await d.page.waitForTimeout(1500);
		if (await stop.isVisible()) await stop.click();
		await d.page.getByLabel('Port 8000').waitFor({ state: 'detached' });
		await d.press('Escape');
	},

	{ id: '3.6', async run(d) {
		await d.click(d.page.getByLabel('Task menu'));
		await d.click(d.page.locator('.s-menu-item', { hasText: 'app' }).first());
		const dialog = d.page.getByRole('dialog');
		await d.page.waitForTimeout(1000);
		const start = dialog.getByRole('button', { name: /^(Start|Run again)$/ });
		if (await start.isVisible()) await d.click(start);
		await d.click(dialog.getByRole('button', { name: 'Background' }));
		const port = d.page.getByLabel('Port 8000');
		await port.waitFor({ timeout: 120_000 });
		await d.point(port);
		const preview = (await vscode(d)).locator('iframe.webview').first();
		await preview.waitFor({ timeout: 60_000 });
		await d.page.waitForTimeout(800);
		await d.zoom(preview, { pad: 30, max: 2.2 });
		await d.page.waitForTimeout(1500);
	} },

	{ id: '4.1', async run(d) {
		await d.zoom();
		await d.click(d.page.getByLabel('Merge…'));
		await d.zoom(d.page.getByRole('dialog'), { pad: 20 });
		await d.page.waitForTimeout(2500);
		await d.click(d.page.getByRole('dialog').getByRole('button', { name: 'Merge', exact: true }));
		await d.zoom();
		await d.go(project);
		await d.phase(1, 'merged');
		await d.page.waitForTimeout(1500);
	} },

	// From here on, the agents' work is reviewed, and merged once accepted.
	async d => defaults(d, async field => {
		await field('On agent ready').locator('button').nth(1).click();
		await field('On review feedback').locator('button').nth(4).click(); // back to the agent, ten times
		await field('On review accept').locator('button').nth(1).click();
		await d.page.waitForTimeout(1000);
		await d.press('Escape');
	}),

	// Three tasks, and a fourth waiting for the tests.
	async d => {
		for (const tid of [2, 3, 4]) {
			await plan(d, tid);
			await d.go(project);
		}
		await create(d);
		await d.type(d.page.getByPlaceholder('Tasks to wait for…'), d.task(4).title.slice(0, 8), 60);
		await d.press('Enter');
		await d.type(d.page.getByPlaceholder('Message the agent…'), PLANS[5], 10);
		await d.press('Control+Enter');
		await d.until(() => said(d, 4, 'tool'));
	},

	{ id: '5.1', async run(d) {
		await d.go('/');
	} },

	// A task the reviewer is reading over, and has had its say in.
	async d => {
		await d.until(() => (d.reviewed = [2, 3, 4].find(tid => d.task(tid).phase === 'review' && d.chat(tid).filter(e => e.rev).length >= 3)));
		await d.go(`${project}/t/${d.reviewed}`);
	},

	{ id: '3.1', async run(d) {
		await d.zoom(d.page.locator('.tps-grip').locator('xpath=preceding-sibling::div[1]'), { pad: 0 });
		await d.page.waitForTimeout(3000);
	} },

	{ id: '4.3', ff: 'fit', async run(d) {
		await d.zoom();
		await d.go(project);
		await d.until(() => [2, 3, 4].every(tid => d.task(tid).phase === 'merged'), 45);
		await d.page.waitForTimeout(2000);
	} },

	// The tests task's merge failed the test of the main it was rebased onto.
	{ id: '4.2', async run(d) {
		await d.go(`${project}/t/4`);
		// Where its merge passed first time, the test it passed.
		let note = d.page.getByText('The merge did not pass the test').first();
		if (!await note.count()) note = d.page.getByText('Testing the merge onto the latest main').first();
		await note.scrollIntoViewIfNeeded();
		await d.zoom(note, { pad: 160, max: 1.8 });
		await d.highlight(note);
	} },

	async d => d.go(project),

	{ id: '6.2', ff: 'fit', async run(d) {
		await d.zoom(d.column('Done'), { pad: 20 });
		const top = d.column('Done').locator('div[draggable]').first();
		await d.point(top);
		await top.hover();
		await d.page.waitForTimeout(700);
		await d.click(top.getByLabel('Deploy up to here'));
		await d.click(d.page.getByRole('button', { name: 'OK' }));
		await d.page.getByLabel('Deploying', { exact: true }).waitFor();
		await d.page.getByLabel('Deploy', { exact: true }).waitFor({ timeout: 600_000 });
		await d.page.waitForTimeout(1500);
	} },

	{ id: '4.4', async run(d) {
		d.chip('a week later');
		await d.zoom();
		await d.click(await d.card(3));
		const box = d.page.getByPlaceholder('Message the agent…');
		await d.zoom(box, { pad: 160, max: 1.6 });
		await d.type(box, 'Names should not care about case: /Docs and /docs are the same link.');
		await d.press('Control+Enter');
		await d.zoom();
		await d.go(project);
		await d.zoom(d.column('Agent'), { pad: 20 });
	} },

	// The Containerfile the tests task left, in the follow-up 4.4 made: the
	// sixth task.
	async d => {
		await d.go(`${project}/t/6`);
		d.code = await open(d, 'Containerfile.dev');
		// Wrapped, for its long lines to be seen whole.
		await d.code.locator('.monaco-editor').first().click();
		await d.press('Alt+z');
	},

	{ id: '1.5', async run(d) {
		await ring(d, d.code, 'pytest');
	} },

	{ id: '6.1', async run(d) {
		await ring(d, d.code, 'tps.deploy');
	} },

	{ id: '7.4', async run(d) {
		await d.go('/p/local:standup/t/5');
		const on = d.page.getByText('Enabled', { exact: true });
		await on.scrollIntoViewIfNeeded();
		await d.zoom(on, { pad: 260, max: 1.6 });
		await d.highlight(d.page.getByLabel('Prompt'));
	} },

	{ id: '5.2', async run(d) {
		await d.zoom();
		await d.click(d.page.getByText('Add host'));
		await d.zoom(d.page.getByRole('dialog'), { max: 1.6 });
		await d.type(d.page.getByPlaceholder('user@host'), 'robin@buildbox');
		await d.page.waitForTimeout(1500);
	} },

	{ id: '7.5', async run(d) {
		await d.press('Escape');
		await d.zoom();
		await d.press('Control+l');
		await d.zoom(d.page.getByRole('dialog'), { max: 1.6 });
		await d.page.keyboard.type('sta', { delay: 120 });
		await d.page.waitForTimeout(800);
		await d.press('Enter');
		await d.zoom();
	} },

	// The text-to-speech task asks for more OpenRouter budget than it has.
	{ id: '7.6', async run(d) {
		await d.go('/p/local:standup/t/4');
		const dialog = d.page.getByRole('dialog');
		await d.zoom(dialog, { pad: 30, max: 1.8 });
		await d.highlight(dialog.locator('.s-field', { hasText: 'OpenRouter budget' }).last());
	} },

	// A Docker client in the scratch task's container, and an image to build.
	async d => {
		await d.press('Escape');
		await d.zoom();
		await d.click(d.page.getByText("Slides for Sam's first morning").first());
		const code = await vscode(d);
		// A terminal of the editor's height, where the build's output fits.
		await tap(d, code.locator('.part.editor').first());
		await d.press('F1');
		await d.page.keyboard.type('Terminal: Create New Terminal in Editor Area', { delay: 10 });
		await d.page.waitForTimeout(800);
		await d.press('Enter');
		await code.locator('.terminal-wrapper').first().waitFor({ timeout: 30_000 });
		await d.page.waitForTimeout(1500);
		await d.page.keyboard.type([
			'curl -fsSL https://download.docker.com/linux/static/stable/x86_64/docker-27.5.1.tgz | tar xz -C /tmp',
			'export PATH=/tmp/docker:$PATH DOCKER_BUILDKIT=0',
			'mkdir -p /tmp/hello',
			'printf "FROM alpine:3\\nRUN echo hello from a nested container\\n" > /tmp/hello/Dockerfile',
			'clear',
		].join(' && '));
		await d.press('Enter');
		await d.page.waitForTimeout(30_000);
	},

	{ id: '7.2', ff: 'fit', async run(d) {
		const code = await vscode(d);
		await d.zoom(code.locator('.xterm-helper-textarea').first(), { pad: 260, max: 1.6 }); // the cursor, at the top
		await d.page.keyboard.type('cat /tmp/hello/Dockerfile; docker build --no-cache /tmp/hello', { delay: 25 });
		await d.press('Enter');
		await d.page.waitForTimeout(12_000);
	} },

	// The follow-up, made after the deploy, on the version it lands in: its
	// reviewer's say, and its agent's once main had moved on.
	async d => {
		await d.phase(6, 'merged');
		await d.go(`${project}/t/6`);
	},

	{ id: '6.3', async run(d) {
		for (const said of ['which already shipped 0.1.0', 'which is already deployed']) {
			const text = d.page.getByText(said).last();
			await text.scrollIntoViewIfNeeded();
			await d.zoom(text, { pad: 100, max: 1.8 });
			await d.highlight(text);
			await d.page.waitForTimeout(5000);
		}
	} },

	async d => d.go('/'),

	{ id: '7.1', async run() {} },

	{ id: '8.1', async run(d) {
		d.title(`<img src="${REPORTS}" style="height:760px;border-radius:12px">`);
	} },

	{ id: '8.2', async run(d) {
		d.title('{robot}<div style="font-size:72px;font-weight:800">TPS</div><code style="font-size:30px">github.com/vanviegen/tps</code>');
	} },
];

