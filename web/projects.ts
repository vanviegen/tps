import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { arrowDown, arrowUp, calendarClock, check, copy, folder, gitBranch, plus, settings, trash2 } from 'staffa/icons.js';
import { showAsk } from './ask.ts';
import { drawBoard } from './board.ts';
import { drawCode } from './code.ts';
import { $state, watch } from './conn.ts';
import { drawHostNotice, hostInk, hostOrder, hostState } from './hosts.ts';
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

/**
 * The same, in the argument order onEach hands out, for the projects proper:
 * scratch ones get none (onEach takes no Infinity), which leaves them out.
 */
export function projectSortKey($p: any, pid: string): (number | string)[] | undefined {
	return $p.scratch ? undefined : projectOrder(pid, $p);
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
		{ label: 'Create scheduled task', icon: calendarClock, click: () => addTask(pid, { schedule: {} }) },
		{ label: 'Open project directory', icon: folder, click: () => void route.go(pathTo(pid, 'base')) },
		{ label: 'Settings…', icon: settings, click: () => projectSettingsDialog(pid, $p) },
		{ label: 'Copy/Move project…', icon: copy, click: () => copyProjectDialog(pid, $p) },
		{ separator: true },
		{ label: 'Move up', icon: arrowUp, click: () => moveProject(pid, -1) },
		{ label: 'Move down', icon: arrowDown, click: () => moveProject(pid, 1) },
		{ separator: true },
		{ label: 'Remove project…', icon: trash2, attrs: 'fg:$s-danger', click: () => void removeProject(pid, $p) },
	];
}

/**
 * Copying a project, or moving it, to a directory on a host: its checkout and
 * its tasks, workspaces and all. A moved project takes the original's place
 * in the list.
 */
function copyProjectDialog(pid: string, $p: any): void {
	const $form = A.proxy({ hid: A.peek($p, 'host'), dir: A.peek(() => shortDir($p.host, $p.dir)), how: 'copy' });
	const $busy = A.proxy({ copying: false });
	void S.dialog({ header: () => A('text=', `Copy or move ${projectName($p)}`), attrs: 'w:32rem', content: close => {
		S.form({
			submit: async () => {
				if (!$form.dir.trim() || $busy.copying) return;
				const move = $form.how === 'move';
				const list = A.peek(sortedProjects);
				const next = list[list.findIndex(([id]) => id === pid) + 1]?.[0];
				$busy.copying = true;
				const result = await cmd('copyProject', { pid, hid: $form.hid, dir: $form.dir.trim(), move });
				$busy.copying = false;
				if (!result) return;
				if (move) reorderProjects(result.pid, next);
				close();
				route.go(pathTo(result.pid));
			},
			content: () => {
				S.select({
					label: 'Host', bind: A.ref($form, 'hid'),
					options: () => Object.keys($state.hosts ?? {}).sort((a, b) => hostOrder(a).localeCompare(hostOrder(b))).map(hid => ({ value: hid, label: hostName(hid) })),
				});
				S.textline({ label: 'Directory', help: 'Made for it; it must not hold anything yet.', required: true, bind: A.ref($form, 'dir') });
				A('div.s-field', () => {
					S.buttonChooser({ attrs: 'align-self:flex-start', options: { copy: 'Copy', move: 'Move' }, bind: A.ref($form, 'how') });
					A(() => A('small fg:$s-muted text=', $form.how === 'move'
						? 'The project, its tasks and its directory are removed here once the copy is in place.'
						: 'The project stays as it is here, and its copy starts out with the same tasks.'));
				});
				// Like adding a project, this waits for the host, which may want a login first.
				A(() => {
					if (!$busy.copying) return;
					A('p.s-help #Copying…');
					if ($state.hosts?.[$form.hid]?.ask) showAsk($form.hid);
				});
			},
			actions: () => S.button({ content: $form.how === 'move' ? 'Move' : 'Copy', icon: copy, type: 'submit' }),
		});
	}});
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
	A(() => drawHostNotice($p.host));
	A(() => {
		// What the host says about the project itself, which it can only say
		// while it is reachable — and which a daemon due an update does not
		// stand in the way of.
		if ($state.hosts?.[$p.host]?.status === 'connected' && $p.error) drawStrip('danger', `${shortDir($p.host, $p.dir)}: ${$p.error}`);
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
		A('div display:flex flex-direction:column gap:$2', () => {
			A('h3 m:0 mt:$2 font-size:1em #Merge');
			S.checkbox({
				label: 'Restart the merge train for a new merge',
				help: 'A task merged while the train tests others stops that test, so the next one takes it along. Otherwise it waits for the test to end.',
				checked: A.peek($p, 'restartTrain'),
				change: (e: Event) => save({ restartTrain: (e.target as HTMLInputElement).checked }),
			});
		});
		drawDeployFields($p, save);
		drawMounts($p, save);
		drawSeeds($p, save);
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

/**
 * How the project is deployed: shown before Containerfile.dev says how, so it
 * can be settled before an agent is asked to set deploys up.
 */
function drawDeployFields($p: any, save: (patch: object) => void): void {
	A('div display:flex flex-direction:column gap:$2', () => {
		A('h3 m:0 mt:$2 font-size:1em #Deploy');
		A('p.s-help m:0 #A LABEL tps.deploy line in Containerfile.dev declares how this project is deployed (ask an agent to set it up). TPS runs that on the default branch, in a container of its own.');
		S.checkbox({
			label: 'Deploy after every merge',
			help: 'Otherwise when you press Deploy. Merged work then runs with the files below without anyone pressing anything, including work merged by an agent.',
			checked: A.peek($p, 'autoDeploy'),
			change: (e: Event) => save({ autoDeploy: (e.target as HTMLInputElement).checked }),
		});
	});
}

/**
 * The files and directories of this host the project's containers get, a row
 * each: whether the deploy alone gets it or every task, whether it may write
 * there, and the path. An empty row at the end takes the next one; rows left
 * empty are not kept.
 */
function drawMounts($p: any, save: (patch: object) => void): void {
	const $rows = A.proxy(A.peek(() => A.clone($p.mounts ?? [])));
	A(() => {
		const last = $rows[$rows.length - 1];
		if (!last || last.path) $rows.push({ path: '', write: false, allTasks: false });
	});
	const store = debounce(600, () => save({ mounts: A.peek(() => A.clone($rows)) }));
	const choice = ($row: any, key: string, off: string, on: string) => S.select({
		attrs: 'w:9rem', options: [off, on],
		bind: {
			get value() { return $row[key] ? on : off; },
			set value(label: string) { $row[key] = label === on; store(); },
		},
	});
	A('div display:flex flex-direction:column gap:$2', () => {
		A('h3 m:0 mt:$2 font-size:1em #Host file permissions');
		A('p.s-help m:0 #Files and directories of this host mounted into the containers at the same path, ~/ being your home here and the container user’s there. A relative path is one in the project’s checkout here, mounted at its place in the container’s own checkout (/work).');
		A.onEach($rows, ($row: any, i: number) => {
			A('div display:flex align-items:center gap:$2', () => {
				choice($row, 'allTasks', 'Deploy', 'All tasks');
				choice($row, 'write', 'Read-only', 'Read/write');
				S.textline({ attrs: 'flex:1', placeholder: '~/.npmrc', bind: A.ref($row, 'path'), input: store });
				S.iconButton({ icon: trash2, tooltip: 'Remove', attrs: 'fg:$s-muted', click: () => { $rows.splice(i, 1); store(); } });
			});
		});
		A('p.s-help m:0 #What all tasks get is in the hands of their agents, which work without asking. A running task gets changes here once its container is made anew, as it is after idling.');
	});
}

/**
 * What of the project's checkout a new workspace starts with, a row each, as
 * the host file permissions are; rows left empty are not kept.
 */
function drawSeeds($p: any, save: (patch: object) => void): void {
	const $rows = A.proxy(A.peek(() => ($p.seeds ?? []).map((path: string) => ({ path }))));
	A(() => {
		const last = $rows[$rows.length - 1];
		if (!last || last.path) $rows.push({ path: '' });
	});
	const store = debounce(600, () => save({ seeds: A.peek(() => $rows.map((r: any) => r.path)) }));
	A('div display:flex flex-direction:column gap:$2', () => {
		A('h3 m:0 mt:$2 font-size:1em #Seeds');
		A('p.s-help m:0 #Paths in the project’s checkout here that git ignores, such as node_modules or a build’s output, which every new workspace starts with, so that its first install or build has less to do. Copied as reflinks where the filesystem has them (btrfs, XFS), which cost next to nothing; elsewhere, such as on ext4, as copies in full.');
		A.onEach($rows, ($row: any, i: number) => {
			A('div display:flex align-items:center gap:$2', () => {
				S.textline({ attrs: 'flex:1', placeholder: 'node_modules', bind: A.ref($row, 'path'), input: store });
				S.iconButton({ icon: trash2, tooltip: 'Remove', attrs: 'fg:$s-muted', click: () => { $rows.splice(i, 1); store(); } });
			});
		});
		A('p.s-help m:0 #Every agent reads what its workspace holds: seed nothing secret.');
	});
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
