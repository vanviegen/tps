import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { askLabel, askSummary, hostAsk, showAsk } from './ask.ts';
import { folder, gitBranch, keyRound, pencil, plus, trash2 } from 'staffa/icons.js';
import { drawCode } from './code.ts';
import { $state } from './conn.ts';
import { addHostDialog, hostColor, sortedHosts } from './hosts.ts';
import { drawTaskFields, humanTasks } from './task.ts';
import { cmd, drawStrip, ELLIPSIS, hostIcon, hostName, pathTo, PHASE_ICONS, PHASE_LABELS, PHASES, shortDir } from './util.ts';

/**
 * Projects: the right column of the front page, one line per project. A task
 * waiting for a human puts a project on top; after that, the one that moved
 * last leads.
 */

/** Where a project sits in the list, as a sort key. */
export function projectOrder(pid: string, $p: any): (number | string)[] {
	return [humanTasks(pid) ? 0 : 1, -($p.activity ?? 0), ($p.name ?? '').toLowerCase()];
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

/**
 * Remove a project from its host, once confirmed; used from its row and from
 * its settings. The list belongs to the host, so this is not just a matter of
 * this dashboard: the tasks go with it, on every dashboard.
 */
async function removeProject(pid: string, $p: any): Promise<boolean> {
	const host = A.peek(() => hostName($p.host));
	const tasks = A.peek(() => Object.keys($state.projects[pid]?.tasks ?? {}).length);
	const also = tasks ? ` Its ${tasks} task${tasks > 1 ? 's are' : ' is'} deleted, workspaces and all.` : '';
	if (!(await S.confirm(`Remove "${A.peek($p, 'name')}" from ${host}?${also} The repository itself is left alone, and every dashboard using ${host} stops showing the project.`))) return false;
	if (!(await cmd('removeProject', { pid }))) return false;
	route.go('/');
	return true;
}

async function renameProject(pid: string, $p: any): Promise<void> {
	const name = await S.prompt('Name for this project:', A.peek($p, 'name') ?? '');
	if (name?.trim()) void cmd('setProject', { pid, name: name.trim() });
}

// Quiet lines on the page's own surface: hairlines between them, a muted
// heading row, and the row under the pointer lit just enough to follow.
const projectTable = A.insertCss({
	'&': 'w:100% min-width:34rem table-layout:fixed border-collapse:collapse;',
	'th': 'text-align:left font-weight:600 font-size:0.85em fg:$s-muted padding: $1 $2; border-bottom: 1px solid $s-faint;',
	'td': `padding: $1 $2; border-bottom: 1px solid $s-faint; ${ELLIPSIS}`,
	'tbody tr': 'cursor:pointer',
	'tbody tr:hover': 'background: color-mix(in oklab, $s-text, transparent 92%);',
});

/** The right column at "/": every project as a line, and the way to add one. */
export function drawProjectTable(): void {
	A('div display:flex flex-direction:column gap:$3 h:100% min-width:0', () => {
		A('div display:flex align-items:center gap:$2 min-width:0', () => {
			A('h2 flex:1 m:0 font-size:1em fg:$s-muted #Projects');
			S.button({ content: 'Add project', icon: plus, attrs: '.small .neutral', click: () => addProjectDialog() });
		});
		A('div flex:1 min-height:0 overflow:auto', () => {
			A(() => {
				if (A.isEmpty($state.projects)) {
					A('p fg:$s-muted rich=', 'No projects yet. *Add project* takes a directory holding a git repository, on any of the hosts beside this.');
					return;
				}
				A('table', projectTable, () => {
					A('thead tr', () => {
						A('th #Project');
						for (const phase of PHASES) {
							// The icon is the column's name; the label it stands for is
							// in the tooltip, and in what a screen reader announces.
							A('th text-align:center w:2.6rem aria-label=', PHASE_LABELS[phase], () => {
								S.addTooltip({ tip: `Tasks in ${PHASE_LABELS[phase]}` });
								A('span display:inline-flex', () => PHASE_ICONS[phase]({ size: '1em' }));
							});
						}
						A('th w:9rem #Host');
						A('th #Path');
					});
					A('tbody', () => {
						A.onEach($state.projects, ($p: any, pid: string) => drawProjectRow(pid, $p), projectOrderOf);
					});
				});
			});
		});
	});
}

function projectOrderOf($p: any, pid: string): (number | string)[] {
	return projectOrder(pid, $p);
}

/** One project: its name, what its tasks are up to, and where it lives. */
function drawProjectRow(pid: string, $p: any): void {
	A('tr', () => {
		S.addContextMenu({ link: pathTo(pid), get items(): S.MenuEntry[] {
			return [
				{ label: 'Rename…', icon: pencil, click: () => void renameProject(pid, $p) },
				{ separator: true },
				{ label: 'Remove project…', icon: trash2, click: () => void removeProject(pid, $p) },
			];
		}});
		// The whole line opens the project; the name is a real link, so it is
		// also what the keyboard, the middle button and a copied address get.
		A('click=', (e: MouseEvent) => {
			if (!(e.target as HTMLElement).closest('a')) route.go(pathTo(pid));
		});
		A('td', () => {
			A('div display:flex align-items:center gap:$2 min-width:0', () => {
				A(`a ${ELLIPSIS} font-weight:600 fg:$s-text text-decoration:none`, 'href=', pathTo(pid), 'text=', A.ref($p, 'name'));
				A(() => {
					if ($p.error) A('span flex-shrink:0 fg:$s-danger #⚠', () => S.addTooltip({ tip: $p.error }));
				});
			});
		});
		A(() => drawPhaseCounts(pid));
		A('td', () => A(() => drawHostCell($p.host)));
		A('td', () => {
			S.addTooltip({ tip: () => A('text=', $p.dir) });
			A('text=', shortDir($p.host, $p.dir));
		});
	});
}

/** A cell per phase, holding how many of the project's tasks are in it. */
function drawPhaseCounts(pid: string): void {
	const counts = {} as Record<string, number>;
	for (const $t of Object.values($state.projects[pid]?.tasks ?? {}) as any[]) counts[$t.phase] = (counts[$t.phase] ?? 0) + 1;
	for (const phase of PHASES) {
		const n = counts[phase] ?? 0;
		// Waiting for a human is the one thing here worth looking at twice.
		A(`td text-align:center ${n && phase === 'human' ? 'fg:$s-warning font-weight:700' : n ? '' : 'fg:$s-faint'}`, () => {
			if (n) S.addTooltip({ tip: `${n} task${n > 1 ? 's' : ''} in ${PHASE_LABELS[phase]}` });
			A('text=', n ? String(n) : '·');
		});
	}
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
				// first time may want a login. That question waits in the host's box,
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
		// front page has its box, and here it is the reason nothing happens.
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
 * is left to say what wants a second look. Renaming and removing the project
 * belong to its line on the front page.
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
 * What the project's new tasks start out with. Rarely changed, so it waits
 * behind a button instead of taking the column.
 */
export function projectDefaultsDialog(pid: string, $p: any): void {
	void S.dialog({ header: 'Default task settings', attrs: 'w:36rem', content: () => {
		A('p.s-help #Copied into every new task of this project; the tasks that exist keep what they have.');
		A(() => {
			// The defaults land with the project itself; each is patched on its
			// own, so typing in one is not interrupted by another being saved.
			const $d = $p.defaults;
			if (!$d) return;
			drawTaskFields(pid, undefined, $d, patch => void cmd('setProject', { pid, defaults: patch }));
		});
	}});
}

/** The right column for the base worktree: VS Code on the project's own checkout. */
export function drawProjectCode(pid: string, $p: any): void {
	const start = () => void cmd('openProjectCode', { pid });
	A(start);
	A(() => {
		if ($p.codePort) {
			// code-server's remote authority is the Host header, which the proxy passes on unchanged.
			drawCode(`${pid}/-`, `/code/${pid}/-/?folder=${encodeURIComponent($p.dir)}`, $p.codePort);
			return;
		}
		S.box({ contentAttrs: 'display:flex flex-direction:column align-items:flex-start', content: () => {
			if ($p.codeError) A('p fg:$s-danger text=', $p.codeError);
			else { A('p#Starting VS Code on the checkout…'); A('progress w:100%'); }
			S.button({ content: 'Retry', attrs: '.small .neutral', click: start });
		}});
	});
}
