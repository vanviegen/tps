import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { arrowDown, arrowUp, check, folder, gitBranch, plus, settings, trash2 } from 'staffa/icons.js';
import { showAsk } from './ask.ts';
import { drawBoard } from './board.ts';
import { drawCode } from './code.ts';
import { $state, watch } from './conn.ts';
import { drawAction, hostInk, hostState } from './hosts.ts';
import { addTask, drawTaskFields } from './task.ts';
import { cmd, debounce, drawStrip, ELLIPSIS, hostIcon, hostName, hostPath, pathTo, PROJECT_COLORS, projectColor, projectName, selection, shortDir } from './util.ts';

/**
 * Projects: what the sidebar lists, in the order the user put them in (kept
 * by the dashboard, as it spans hosts), each under its host, in its colour and with its code
 * (kept by the project's daemon, like its name).
 */

/**
 * Where a project sits in the list, as a sort key: the user's order, the
 * unplaced ones after that by name, and the hosts' scratch tasks last of all.
 */
export function projectOrder(pid: string, $p: any): (number | string)[] {
	if ($p.scratch) return [Infinity, hostName($p.host)];
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

/** The projects on one host, by name; its scratch project is none of them. */
export function projectsOn(hid: string): [string, any][] {
	return (Object.entries($state.projects ?? {}) as [string, any][])
		.filter(([, $p]) => $p.host === hid && !$p.scratch)
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

/** Move a project one place up or down among those on its host. */
function moveProject(pid: string, by: -1 | 1): void {
	const list = A.peek(sortedProjects);
	const i = list.findIndex(([id]) => id === pid);
	let j = i + by;
	while (j >= 0 && j < list.length && list[j][1].host !== list[i][1].host) j += by;
	if (i < 0 || j < 0 || j >= list.length) return;
	[list[i], list[j]] = [list[j], list[i]];
	void cmd('setProjectOrder', { order: list.map(([id]) => id) });
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

/** Leave the project directory, stopping the VS Code on it rather than leaving that for the idle sweep. */
export function closeBase(pid: string): void {
	const { pid: shown, tid } = A.peek(selection);
	if (shown === pid && tid === 'base') void route.go(pathTo(pid));
	void cmd('closeProjectCode', { pid });
}

/**
 * The pid of the host's scratch tasks: a project to the daemon, in which
 * every task starts in an empty repository of its own (see ensureScratch),
 * and no project to the user, who finds them under the host itself.
 */
export function scratchOf(hid: string): string | undefined {
	return Object.keys($state.projects ?? {}).find(pid => $state.projects[pid].host === hid && $state.projects[pid].scratch);
}

/** The project's menu: everything to do with it, from its row in the sidebar. */
export function projectMenuItems(pid: string, $p: any): S.MenuEntry[] {
	return [
		{ label: 'Create task', icon: plus, click: () => addTask(pid) },
		{ label: 'Open project directory', icon: folder, click: () => void route.go(pathTo(pid, 'base')) },
		{ label: 'Settings…', icon: settings, click: () => projectSettingsDialog(pid, $p) },
		{ separator: true },
		{ label: 'Move up', icon: arrowUp, click: () => moveProject(pid, -1) },
		{ label: 'Move down', icon: arrowDown, click: () => moveProject(pid, 1) },
		{ separator: true },
		{ label: 'Remove project…', icon: trash2, attrs: 'fg:$s-danger', click: () => void removeProject(pid, $p) },
	];
}

/**
 * The project's page: its name and facts, what wants attention, what to do
 * with it, and its board of four columns underneath.
 */
export function drawProjectPage(pid: string, $p: any): void {
	A('div display:flex flex-direction:column gap:$3 h:100% min-width:0', () => {
		A('div display:flex align-items:center gap:$3 flex-wrap:wrap min-width:0', () => {
			// The name in the project's colour, as the sidebar has it.
			A(() => A(`h2 m:0 font-size:1.15em min-width:0 ${ELLIPSIS} fg:${projectColor($p)} text=`, projectName($p)));
			if ($p.scratch) A('span font-size:0.9em fg:$s-muted #Tasks that belong to no project, each in an empty repository of its own');
			else A(() => drawProjectFacts($p));
			A('div flex:1');
			// The keys are on these rather than on the board's ✛ or the menu's
			// rows, so each is bound once while the project is on screen (see
			// KEYS in main.ts).
			A('div display:flex gap:$2 flex-wrap:wrap', () => {
				S.button({ content: 'Create task', icon: plus, attrs: '.small', key: 'mod+shift+s', click: () => addTask(pid) });
				if ($p.scratch) return;
				S.button({ content: 'Open project directory', icon: folder, attrs: '.small .neutral', key: 'mod+shift+f', click: () => void route.go(pathTo(pid, 'base')) });
				S.button({ content: 'Settings', icon: settings, attrs: '.small .neutral', click: () => projectSettingsDialog(pid, $p) });
			});
		});
		drawNotices(pid, $p);
		A('div flex:1 min-height:0', () => drawBoard(pid, () => addTask(pid)));
	});
}

/**
 * The front page: one board with the tasks of every project. A task started
 * here goes to the first project in the list (scratch ones aside), and its
 * Plan settings are where another one is picked.
 */
export function drawDashboard(): void {
	const create = () => {
		const first = sortedProjects().find(([, $p]) => !$p.scratch);
		if (first) void addTask(first[0]);
		else void route.go(hostPath('local')); // where a project is added
	};
	A('div display:flex flex-direction:column gap:$3 h:100% min-width:0', () => {
		A('div display:flex align-items:center gap:$3', () => {
			A('h2 m:0 font-size:1.15em flex:1 #All projects');
			S.button({ content: 'Create task', icon: plus, attrs: '.small', key: 'mod+shift+s', click: create });
		});
		A('div flex:1 min-height:0', () => drawBoard(undefined, create));
	});
}

/** The machine a project lives on, saying by its colour whether it is up, and leading to its page. */
function drawHostCell(hid: string): void {
	const $h = $state.hosts?.[hid];
	const state = hostState(hid, $h);
	A(`a display:flex align-items:center gap:$1 min-width:0 text-decoration:none fg:${state.ok ? 'inherit' : hostInk(state.color)}`, 'href=', hostPath(hid), () => {
		S.addTooltip({ tip: `${hid === 'local' ? 'This machine' : $h?.dest ?? hid} · ${state.text}` });
		A('span display:flex flex-shrink:0', () => hostIcon(hid)({ size: '1em' }));
		A(`span ${ELLIPSIS} text=`, hostName(hid));
	});
}

/**
 * Adding a project: a directory on the host whose page or menu this was
 * started from. The name follows the directory as it is typed, until the name
 * is typed in itself: from then on it is the user's.
 */
export function addProjectDialog(hid: string): void {
	const $form = A.proxy({ dir: '', name: '' });
	const $busy = A.proxy({ adding: false });
	let named = false; // the name is the user's own now
	void S.dialog({ header: () => A('text=', `Add project to ${hostName(hid)}`), attrs: 'w:32rem', content: close => {
		S.form({
			submit: async () => {
				if (!$form.dir.trim() || $busy.adding) return;
				$busy.adding = true;
				const args = { hid, dir: $form.dir.trim(), name: $form.name.trim() };
				let result = await cmd('addProject', args);
				// A directory that is not there yet, or holds no repository, is made one once confirmed.
				if (result?.missing) {
					const question = result.missing === 'dir'
						? `${args.dir} does not exist. Create it, as a new git repository?`
						: `${args.dir} is not a git repository. Make it one (git init)?`;
					result = await S.confirm(question) && await cmd('addProject', { ...args, init: true });
				}
				$busy.adding = false;
				if (!result) return;
				close();
				route.go(pathTo(result.pid));
			},
			content: () => {
				A('p rich=', 'A git repository on this host, or a directory to become one. Work there goes on while this dashboard is closed.');
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
				// Adding waits for the host to answer, which may want a login first.
				// That question waits in the sidebar, which this dialog covers — so
				// while we wait, it comes here instead.
				A(() => {
					if (!$busy.adding) return;
					A('p.s-help #Waiting for the host…');
					if ($state.hosts?.[hid]?.ask) showAsk(hid);
				});
			},
			actions: () => S.button({ content: 'Add', icon: plus, type: 'submit' }),
		});
	}});
}

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
		// Whatever the host has to say is said the same here as in the sidebar,
		// and here it is the reason nothing on this board is happening.
		const state = hostState($p.host, $h);
		if (!state.ok) drawStrip(state.color, `${$h.name}: ${state.text}`, () => drawAction(state));
		// What the host says about the project itself, which it can only say
		// while it is reachable — and which a daemon due an update does not
		// stand in the way of.
		if ($h.status === 'connected' && $p.error) drawStrip('danger', `${shortDir($p.host, $p.dir)}: ${$p.error}`);
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
 * Everything about the project that is set rather than done: what it is
 * called, and — kept apart, being about the tasks to come rather than the
 * project itself — what its new tasks start out with. Every field saves
 * itself as it is changed, so there is nothing here to confirm and nothing
 * lost by closing the dialog.
 */
export function projectSettingsDialog(pid: string, $p: any): void {
	const save = (patch: object) => void cmd('setProject', { pid, ...patch });
	void S.dialog({ header: 'Project settings', attrs: 'w:36rem', contentAttrs: 'display:flex flex-direction:column gap:$3', content: () => {
		// The values are read once and written back by the fields themselves: a
		// redraw while typing would take the cursor with it.
		S.textline({
			label: 'Name', value: A.peek($p, 'name') ?? '',
			input: debounce(600, (e: Event) => {
				const name = (e.target as HTMLInputElement).value.trim();
				if (name) save({ name });
			}),
		});
		drawColorField(pid, $p);
		// A section of its own under a heading, so what it is about is not
		// mistaken for more of the project's own settings.
		A('div display:flex flex-direction:column gap:$2', () => {
			A('h3 m:0 mt:$2 font-size:1em #Default task settings');
			A('p.s-help m:0 #Copied into every new task of this project; the tasks that exist keep what they have.');
			A(() => {
				// The defaults land with the project itself; each is patched on its
				// own, so typing in one is not interrupted by another being saved.
				const $d = $p.defaults;
				if (!$d) return;
				drawTaskFields(pid, undefined, $d, patch => save({ defaults: patch }));
			});
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
		A('span.s-help #What the sidebar shows the project in.');
	});
}

/**
 * VS Code on the project's own checkout, filling the window beside the
 * sidebar. Being on screen watches the checkout, which is what starts its
 * code-server. Nothing is drawn over the frame: whatever corner an icon took,
 * it took from VS Code's own.
 */
export function drawProjectCode(pid: string, $p: any, left: string): void {
	watch(pid);
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
	});
}
