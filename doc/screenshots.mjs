// Takes the screenshots next to the features in the README, from a freshly
// seeded demo dashboard: the project's 'app' service, restarted (see
// demo/seed.sh).
//
//   node doc/screenshots.mjs [dashboard URL [shot...]]
//
// Each shot frames the elements `show` returns, `pad` pixels around them (one
// number, or top, right, bottom and left) in a 1600 by 1000 window, cut off at
// `max` pixels wide and high where that is all it takes to make the point, at
// twice the pixels for sharpness unless `dpr` says otherwise: VS Code's
// terminal draws no text at all at two. The README shows them all 320 pixels
// wide.
// $CHROMIUM names the browser, /usr/bin/chromium by default.

import { chromium } from 'playwright-core';

const [url = 'http://localhost:8080', ...only] = process.argv.slice(2);
const out = new URL('features/', import.meta.url).pathname;
const task = (pid, tid) => `/p/local:${pid}/t/${tid}`;

/** The task's VS Code, once it is up and has turned dark. */
async function vscode(page) {
	const code = page.locator('iframe').first().contentFrame();
	// The first time round, the task's image is built first.
	await code.locator('.monaco-workbench.vs-dark').waitFor({ timeout: 600_000 });
	await page.waitForTimeout(3000);
	return code;
}

/** Commands typed into VS Code's terminal, and the terminal they ran in. */
async function terminal(page, lines, wait) {
	const code = await vscode(page);
	await code.getByRole('tab', { name: 'bash' }).click();
	await code.locator('.xterm').first().click();
	await page.keyboard.type('clear\n', { delay: 20 });
	for (const line of lines) await page.keyboard.type(line + '\n', { delay: 20 });
	await page.waitForTimeout(wait);
	return code.locator('.editor-group-container').first(); // where VS Code has the terminal
}

/** A file opened in VS Code's editor, and the editor. */
async function editor(page, file) {
	const code = await vscode(page);
	await code.locator('.monaco-workbench').click({ position: { x: 600, y: 300 } });
	await page.keyboard.press('Control+p');
	await page.keyboard.type(file, { delay: 20 });
	await page.keyboard.press('Enter');
	await page.waitForTimeout(1500);
	return code.locator('.editor-group-container').first();
}

const shots = {
	sandbox: { max: [420, 160], path: task('snip', 3), dpr: 1, pad: [-30, -40, -740, 0], show: (page) => terminal(page, ['# its own container, its own clone', 'sudo whoami; git status --short'], 1500) },
	board: { max: [400, 330], path: '/', pad: [56, 8, 40, 30], show: (page) => [page.getByText('Plan', { exact: true }), page.getByText('Agent', { exact: true }), page.getByText('standup week should skip')] },
	vscode: { max: [460, 330], path: task('snip', 3), pad: [0, -200, -460, 0], show: async (page) => {
		await editor(page, 'snip.py');
		return page.locator('iframe').first();
	} },
	merge: { max: [440, 260], path: task('snip', 3), show: async (page) => {
		await page.getByLabel('Merge…').click();
		return page.getByRole('dialog');
	} },
	review: { max: [400, 175], path: task('snip', 6), pad: [8, 12, 52, 8], show: (page) => [page.getByText('On agent ready'), page.getByText('10×'), page.getByText('On review accept')] },
	environment: { max: [440, 240], path: task('snip', 3), pad: [0, 0, -420, 0], show: (page) => editor(page, 'Containerfile.dev') },
	containers: { max: [420, 200], path: task('snip', 3), dpr: 1, pad: [-30, -110, -700, 0], show: (page) => terminal(page, ["# in the task's sandbox", 'docker run --rm docker.io/grycap/cowsay /usr/games/cowsay Moo'], 30000) },
	services: { path: task('snip', 3), pad: [6, 6, 0, 6], show: async (page) => {
		await page.getByLabel('Task menu').click();
		const item = (text) => page.locator('.s-menu-item', { hasText: text });
		return [item('python3 snip.py'), item('8000 →')];
	} },
	revert: { max: [420, 200], path: task('snip', 3), show: async (page) => {
		await page.getByText('Save point').last().click();
		return page.getByRole('dialog');
	} },
	followup: { max: [460, 230], path: '/p/local:snip', pad: [2, 2, 56, 10], show: (page) => [page.locator('section', { hasText: 'Human' }).getByText('Refuse links that'), page.locator('section', { hasText: 'Done' }).locator('header'), page.getByText('A dev container so tasks can run the tests')] },
	scratch: { max: [460, 340], path: task('scratch', 1), pad: 8, show: (page) => [page.getByText('scratch at localhost'), page.getByText('has six slides')] },
	projects: { max: [400, 260], path: '/', show: async (page) => {
		await page.keyboard.press('Control+l');
		return [page.getByRole('dialog'), page.getByRole('listbox')];
	} },
	hosts: { max: [400, 250], path: '/', show: async (page) => {
		await page.getByText('Add host').click();
		return page.getByRole('dialog');
	} },
	models: { max: [400, 250], path: task('standup', 4), show: (page) => page.getByRole('dialog') },
};

const [width, height] = [1600, 1000];
const browser = await chromium.launch({ executablePath: process.env.CHROMIUM ?? '/usr/bin/chromium' });
for (const [name, { path, dpr = 2, pad = 0, max = [width, height], show }] of Object.entries(shots)) {
	if (only.length && !only.includes(name)) continue;
	const page = await browser.newPage({ viewport: { width, height }, deviceScaleFactor: dpr });
	await page.goto(url + path);
	await page.waitForTimeout(3000); // the board arriving
	const shown = [await show(page)].flat();
	await page.waitForTimeout(500); // menus and dialogs fading in
	const boxes = await Promise.all(shown.map(l => l.first().boundingBox()));
	const [top, right, bottom, left] = typeof pad === 'number' ? [pad, pad, pad, pad] : pad;
	const x = Math.max(0, Math.min(...boxes.map(b => b.x)) - left);
	const y = Math.max(0, Math.min(...boxes.map(b => b.y)) - top);
	const clip = {
		x, y,
		width: Math.min(max[0], width - x, Math.max(...boxes.map(b => b.x + b.width)) + right - x),
		height: Math.min(max[1], height - y, Math.max(...boxes.map(b => b.y + b.height)) + bottom - y),
	};
	await page.screenshot({ path: `${out}${name}.png`, clip });
	await page.close();
	console.log(name);
}
await browser.close();
