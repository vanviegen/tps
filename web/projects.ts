import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { arrowDown, arrowUp, check, code, folder, gitBranch, keyRound, plus, settings, trash2, x } from 'staffa/icons.js';
import { askLabel, askSummary, hostAsk, showAsk } from './ask.ts';
import { drawBoard } from './board.ts';
import { drawCode } from './code.ts';
import { $state } from './conn.ts';
import { hold, release } from './holds.ts';
import { addHostDialog, hostColor, sortedHosts } from './hosts.ts';
import { addTask, drawTaskFields } from './task.ts';
import { cmd, debounce, drawStrip, ELLIPSIS, hostIcon, hostName, pathTo, PROJECT_COLORS, projectColor, projectInitials, selection, shortDir } from './util.ts';

/**
 * Projects: what the sidebar lists, in the order the user put them in (kept
 * by the dashboard, as it spans hosts), each in its colour and with its code
 * (kept by the project's daemon, like its name).
 */

/** Where a project sits in the list, as a sort key: its place in the user's order, the unplaced ones after that by name. */
export function projectOrder(pid: string, $p: any): (number | string)[] {
	const order: string[] = $state.projectOrder ?? [];
	const i = order.indexOf(pid);
	return [i < 0 ? order.length : i, ($p.name ?? '').toLowerCase()];
}

/** The same, in the argument order onEach hands out. */
export function projectSortKey($p: any, pid: string): (number | string)[] {
	return projectOrder(pid, $p);
}

/** The projects in the order they are listed. */
export function sortedProjects(): [string, any][] {
	return (Object.entries($state.projects ?? {}) as [string, any][])
		.sort((a, b) => compareKeys(projectOrder(a[0], a[1]), projectOrder(b[0], b[1])));
}

function compareKeys(a: (number | string)[], b: (number | string)[]): number {
	for (let i = 0; i < a.length; i++) {
		if (a[i] < b[i]) return -1;
		if (a[i] > b[i]) return 1;
	}
	return 0;
}

/** The projects on one host, by name. */
export function projectsOn(hid: string): [string, any][] {
	return (Object.entries($state.projects ?? {}) as [string, any][])
		.filter(([, $p]) => $p.host === hid)
		.sort((a, b) => a[1].name.localeCompare(b[1].name));
}

/** Put `pid` right before `beforePid` in the list (or at the end, without one), and tell the server the whole order. */
export function reorderProjects(pid: string, beforePid?: string): void {
	const ids = A.peek(sortedProjects).map(([id]) => id).filter(id => id !== pid);
	const at = beforePid ? ids.indexOf(beforePid) : -1;
	if (at < 0) ids.push(pid);
	else ids.splice(at, 0, pid);
	void cmd('setProjectOrder', { order: ids });
}

/** Move a project one place up or down. */
function moveProject(pid: string, by: -1 | 1): void {
	const ids = A.peek(sortedProjects).map(([id]) => id);
	const i = ids.indexOf(pid);
	const j = i + by;
	if (i < 0 || j < 0 || j >= ids.length) return;
	[ids[i], ids[j]] = [ids[j], ids[i]];
	void cmd('setProjectOrder', { order: ids });
}

/**
 * Remove a project from its host, once confirmed. The list belongs to the
 * host, so this is not just a matter of this dashboard: the tasks go with it,
 * on every dashboard.
 */
async function removeProject(pid: string, $p: any): Promise<boolean> {
	const host = A.peek(() => hostName($p.host));
	const tasks = A.peek(() => Object.keys($state.projects[pid]?.tasks ?? {}).length);
	const also = tasks ? ` Its ${tasks} task${tasks > 1 ? 's are' : ' is'} deleted, workspaces and all.` : '';
	if (!(await S.confirm(`Remove "${A.peek($p, 'name')}" from ${host}?${also} The repository itself is left alone, and every dashboard using ${host} stops showing the project.`))) return false;
	if (!(await cmd('removeProject', { pid }))) return false;
	if (A.peek(selection).pid === pid) route.go('/');
	return true;
}

/** The project's menu: everything to do with it, from its row in the sidebar. */
export function projectMenuItems(pid: string, $p: any): S.MenuEntry[] {
	return [
		{ label: 'Add task', icon: plus, click: () => addTask(pid) },
		{ label: 'View code', icon: code, click: () => void route.go(pathTo(pid, 'base')) },
		{ label: 'Settings…', icon: settings, click: () => projectSettingsDialog(pid, $p) },
		{ separator: true },
		{ label: 'Move up', icon: arrowUp, click: () => moveProject(pid, -1) },
		{ label: 'Move down', icon: arrowDown, click: () => moveProject(pid, 1) },
		{ separator: true },
		{ label: 'Remove project…', icon: trash2, attrs: 'fg:$s-danger', click: () => void removeProject(pid, $p) },
	];
}

/** The project's initials in its colour: what stands for it wherever there is little room, and what its rows are recognised by. */
export function drawProjectChip($p: any): void {
	A(() => {
		A('span display:inline-flex align-items:center justify-content:center flex-shrink:0 min-width:2em h:1.5em ph:0.3em r:$s-radius-sm font-size:0.72em font-weight:800 line-height:1 fg:#14161a letter-spacing:0.03em',
			`bg:${projectColor($p)}`, 'text=', projectInitials($p));
	});
}

/**
 * The project's page: its name and facts, what wants attention, what to do
 * with it, and its board of four columns underneath.
 */
export function drawProjectPage(pid: string, $p: any): void {
	A('div display:flex flex-direction:column gap:$3 h:100% min-width:0', () => {
		A('div display:flex align-items:center gap:$3 flex-wrap:wrap min-width:0', () => {
			A('h2 m:0 font-size:1.15em display:flex align-items:center gap:$2 min-width:0', () => {
				drawProjectChip($p);
				A(`span ${ELLIPSIS} text=`, A.ref($p, 'name'));
			});
			A(() => drawProjectFacts($p));
			A('div flex:1');
			// The keys are on these rather than on the board's ✛ or the menu's
			// rows, so each is bound once while the project is on screen (see
			// KEYS in main.ts).
			A('div display:flex gap:$2 flex-wrap:wrap', () => {
				S.button({ content: 'Add task', icon: plus, attrs: '.small', key: 'mod+shift+s', click: () => addTask(pid) });
				S.button({ content: 'View code', icon: code, attrs: '.small .neutral', key: 'mod+shift+f', click: () => void route.go(pathTo(pid, 'base')) });
				S.button({ content: 'Settings', icon: settings, attrs: '.small .neutral', click: () => projectSettingsDialog(pid, $p) });
			});
		});
		drawNotices(pid, $p);
		A('div flex:1 min-height:0', () => drawBoard(pid, $p));
	});
}

/** The machine a project lives on, saying by its colour whether it is up. */
function drawHostCell(hid: string): void {
	const $h = $state.hosts?.[hid];
	const color = hostColor(hid, $h);
	A(`div display:flex align-items:center gap:$1 min-width:0 ${color === 'success' ? '' : `fg:$s-${color}`}`, () => {
		S.addTooltip({ tip: `${hid === 'local' ? 'This machine' : $h?.dest ?? hid} · ${$h?.status ?? 'unknown'}` });
		A('span display:flex flex-shrink:0', () => hostIcon(hid)({ size: '1em' }));
		A(`span ${ELLIPSIS} text=`, hostName(hid));
	});
}

/**
 * Adding a project: a directory on the host picked here — the one whose menu
 * this was started from, when it was. The name follows the directory as it is
 * typed, until the name is typed in itself: from then on it is the user's.
 */
export function addProjectDialog(hid?: string): void {
	const $form = A.proxy({ host: hid ?? 'local', dir: '', name: '' });
	const $busy = A.proxy({ adding: false });
	let named = false; // the name is the user's own now
	let host = $form.host; // the last host that was really picked
	void S.dialog({ header: 'Add project', attrs: 'w:32rem', content: close => {
		S.form({
			submit: async () => {
				if (!$form.dir.trim() || $form.host === ADD_HOST || $busy.adding) return;
				$busy.adding = true;
				const result = await cmd('addProject', { hid: $form.host, dir: $form.dir.trim(), name: $form.name.trim() });
				$busy.adding = false;
				if (!result) return;
				close();
				route.go(pathTo(result.pid));
			},
			content: () => {
				A('p rich=', 'A directory holding a git repository, on one of your hosts. Work there goes on while this dashboard is closed.');
				S.select({
					label: 'Host', bind: A.ref($form, 'host'),
					options: () => [
						...sortedHosts().map(([id]) => ({ value: id, label: hostName(id) })),
						{ value: ADD_HOST, label: 'Add host…' },
					],
				});
				// "Add host…" is not a host but a detour: it opens that dialog at
				// once, and whatever it adds takes its place in the list.
				A(() => {
					if ($form.host !== ADD_HOST) { host = $form.host; return; }
					void addHostDialog().then(added => { $form.host = added ?? host; });
				});
				S.textline({
					label: 'Directory', placeholder: '~/projects/app', required: true, bind: A.ref($form, 'dir'),
					input: (e: Event) => {
						if (!named) $form.name = dirName((e.target as HTMLInputElement).value);
					},
				});
				S.textline({
					label: 'Name', help: 'What to call it in the list; the directory name by default.', bind: A.ref($form, 'name'),
					input: () => { named = true; },
				});
				// Adding waits for the host to answer, and one being reached for the
				// first time may want a login. That question waits in the sidebar,
				// which this dialog covers — so while we wait, it comes here instead.
				A(() => {
					if (!$busy.adding) return;
					A('p.s-help #Waiting for the host…');
					const ask = hostAsk($form.host);
					if (ask) showAsk(ask[0]);
				});
			},
			actions: () => S.button({ content: 'Add', icon: plus, type: 'submit' }),
		});
	}});
}

/** The value the host selector uses for its last entry; no host id can be it. */
const ADD_HOST = '\x00add-host';

/** The name a directory suggests: its last part, if that says anything. */
function dirName(dir: string): string {
	const last = dir.trim().replace(/\/+$/, '').split('/').pop() ?? '';
	return last === '~' || last === '.' || last === '..' ? '' : last;
}

/** A strip per thing worth knowing: the host's state, the project, its checkout. */
export function drawNotices(pid: string, $p: any): void {
	A(() => {
		const $h = $state.hosts?.[$p.host];
		if (!$h) return;
		// A login the host is waiting for is shown wherever the host is: the
		// sidebar has its strip, and here it is the reason nothing happens.
		const ask = hostAsk($p.host);
		if (ask) {
			drawStrip('warning', `${$h.name}: ${askSummary(ask[1])}`, () => {
				keyRound({ size: '1em' });
				A('span font-weight:600 text=', askLabel(ask[1]));
			}, () => showAsk(ask[0]));
		} else if ($h.status !== 'connected') {
			drawStrip('danger', `${$h.name}: ${$h.status}${$h.error ? ' · ' + $h.error : ''}`,
				() => S.button({ content: 'Connect', attrs: '.small', click: () => void cmd('connectHost', { hid: $p.host }) }));
		} else if ($p.error) {
			drawStrip('danger', `${shortDir($p.host, $p.dir)}: ${$p.error}`);
		} else if ($h.warning) {
			drawStrip('warning', $h.warning);
		} else if ($h.restarting) {
			drawStrip('neutral', `The daemon on ${$h.name} restarts into this build as soon as nothing is running.`);
		} else if ($h.updatable) {
			drawStrip('neutral', `The daemon on ${$h.name} runs another build of TPS.`,
				() => S.button({ content: 'Update daemon', attrs: '.small', click: () => void cmd('updateDaemon', { hid: $p.host }) }));
		}
	});
	A(() => {
		if (!$p.dirty) return;
		drawStrip('warning', `Uncommitted work in \`${shortDir($p.host, $p.dir)}\` (${$p.git}). Task workspaces clone the committed state only, so this stays out of them and may block a merge.`,
			() => S.button({ content: 'Open', attrs: '.small', click: () => void route.go(pathTo(pid, 'base')) }));
	});
}

/**
 * Where the project stands, as one wrapping line of plain facts: the branch of
 * its checkout and what is uncommitted in it, then the machine and directory
 * it lives on. Nothing here is a button — reading is all it is for, and colour
 * is left to say what wants a second look.
 */
export function drawProjectFacts($p: any): void {
	const facts: Array<() => void> = [
		() => {
			S.addTooltip({ tip: 'The branch task workspaces start from, and merge back into' });
			gitBranch({ size: '1em' });
			A('span', () => A('text=', $p.defaultBranch ?? 'main'));
		},
		// Its own scope: work appearing in the checkout must not redraw the line.
		() => A(() => {
			A(`span ${$p.dirty ? 'fg:$s-warning' : ''}`, () => {
				S.addTooltip({ tip: $p.dirty ? 'Uncommitted work in the checkout' : 'Nothing uncommitted in the checkout' });
				A('text=', $p.git ?? '');
			});
		}),
		() => A(() => drawHostCell($p.host)),
		// The directory is the one that may not fit, so it is the one cut off.
		() => {
			S.addTooltip({ tip: () => A('text=', $p.dir) });
			folder({ size: '1em' });
			A(`span ${ELLIPSIS}`, () => A('text=', shortDir($p.host, $p.dir)));
		},
	];
	A('div display:flex flex-wrap:wrap align-items:center gap:$1 min-width:0 font-size:0.9em fg:$s-muted', () => {
		facts.forEach((fact, i) => {
			if (i) A('span flex-shrink:0 fg:$s-faint aria-hidden=true #·');
			A('span display:inline-flex align-items:center gap:0.35em min-width:0', fact);
		});
	});
}

/**
 * Everything about the project that is set rather than done: what it is called
 * and what stands for it in the sidebar, and — kept apart, being about the
 * tasks to come rather than the project itself — what its new tasks start out
 * with. Every field saves itself as it is changed, so there is nothing here to
 * confirm and nothing lost by closing the dialog.
 */
export function projectSettingsDialog(pid: string, $p: any): void {
	const save = (patch: object) => void cmd('setProject', { pid, ...patch });
	void S.dialog({ header: 'Project settings', attrs: 'w:36rem', contentAttrs: 'display:flex flex-direction:column gap:$3', content: () => {
		// The values are read once and written back by the fields themselves: a
		// redraw while typing would take the cursor with it.
		A('div display:flex flex-direction:column gap:$2', () => {
			S.textline({
				label: 'Name', value: A.peek($p, 'name') ?? '',
				input: debounce(600, (e: Event) => {
					const name = (e.target as HTMLInputElement).value.trim();
					if (name) save({ name });
				}),
			});
			S.textline({
				label: 'Initials', value: A.peek(() => projectInitials($p)),
				help: 'One to three letters to stand for the project where its name does not fit; emptied, they are made from the name again.',
				input: debounce(600, (e: Event) => save({ initials: (e.target as HTMLInputElement).value.trim().slice(0, 3) })),
			});
			drawColorField(pid, $p);
		});
		// A section of its own, boxed and headed, so what it is about is not
		// mistaken for more of the project's own settings.
		S.box({
			header: 'Default task settings', attrs: 'mt:0',
			contentAttrs: 'display:flex flex-direction:column gap:$2',
			content: () => {
				A('p.s-help m:0 #Copied into every new task of this project; the tasks that exist keep what they have.');
				A(() => {
					// The defaults land with the project itself; each is patched on its
					// own, so typing in one is not interrupted by another being saved.
					const $d = $p.defaults;
					if (!$d) return;
					drawTaskFields(pid, undefined, $d, patch => save({ defaults: patch }));
				});
			},
		});
	}});
}

/** The project's colour, as a field of the settings: the palette, with the one in use ticked. */
function drawColorField(pid: string, $p: any): void {
	A('div.s-field', () => {
		A('label #Colour');
		A('div display:flex flex-wrap:wrap gap:$2', () => {
			for (const color of PROJECT_COLORS) {
				A('button type=button w:2.4rem h:2.4rem r:50% border:0 cursor:pointer display:inline-flex align-items:center justify-content:center fg:#14161a',
					`bg:${color}`, 'aria-label=', color,
					'click=', () => void cmd('setProject', { pid, color }),
					() => { A(() => { if (projectColor($p) === color) check({ size: '1.2em' }); }); });
			}
		});
		A('span.s-help #What the sidebar shows the project and its tasks in.');
	});
}

/**
 * VS Code on the project's own checkout, filling the window beside the
 * sidebar. Arriving here holds the checkout (see holds.ts), which is what
 * starts its code-server; the Close icon lets go of it and returns to the
 * project.
 */
export function drawProjectCode(pid: string, $p: any, left: string): void {
	hold(pid);
	A('div position:relative flex:1 min-width:0 overflow:auto p:$3', () => {
		A(() => {
			if ($p.codePort) {
				// code-server's remote authority is the Host header, which the proxy passes on unchanged.
				drawCode(`${pid}/-`, `/code/${pid}/-/?folder=${encodeURIComponent($p.dir)}`, $p.codePort, left);
				return;
			}
			S.box({ contentAttrs: 'display:flex flex-direction:column align-items:flex-start', content: () => {
				if ($p.codeError) A('p fg:$s-danger text=', $p.codeError);
				else { A('p#Starting VS Code on the checkout…'); A('progress w:100%'); }
				S.button({ content: 'Retry', attrs: '.small .neutral', click: () => void cmd('openProjectCode', { pid }) });
			}});
		});
		// Over the frame's top right corner; the frame is fixed under everything
		// positioned after it, which the z-index sees to.
		A('div.s-s.neutral.shadow position:absolute top:$3 right:$3 z-index:1 display:flex p:0.15rem r:99em', () => {
			S.iconButton({ icon: x, ariaLabel: 'Close VS Code', tooltip: 'Close VS Code on the checkout and go back to the project',
				click: () => { release(pid); void route.go(pathTo(pid)); } });
		});
	});
}
