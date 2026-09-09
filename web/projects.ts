import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { download, ellipsisVertical, keyRound, monitor, pencil, plug, plus, power, server, settings, trash2 } from 'staffa/icons.js';
import { drawCode } from './code.ts';
import { $state } from './conn.ts';
import { humanTasks } from './task.ts';
import { cmd, ELLIPSIS, hostName, pathTo, PHASE_ICONS, PHASE_LABELS, PHASES, shortDir } from './util.ts';

/** Projects: the ones with a task waiting for a human first, then most recently active. */
export function sortedProjects(): [string, any][] {
	const projects = Object.entries($state.projects ?? {}) as [string, any][];
	return projects.sort((a, b) =>
		(humanTasks(b[0]) ? 1 : 0) - (humanTasks(a[0]) ? 1 : 0) ||
		(b[1].activity ?? 0) - (a[1].activity ?? 0) ||
		(a[1].name < b[1].name ? -1 : 1));
}

// A link that does something instead of going somewhere: a button, styled as
// the link it reads as, so the keyboard reaches it like any other button.
const linkButton = A.insertCss({
	'&': 'display:inline-flex align-items:center gap:$1 cursor:pointer bg:transparent border:none p:0 font:inherit color: $s-link-fg; text-decoration:underline text-underline-offset:2px;',
	'&:hover': 'filter: brightness(1.15)',
});

/** The projects on one host, by name. */
function projectsOn(hid: string): [string, any][] {
	return (Object.entries($state.projects ?? {}) as [string, any][])
		.filter(([, $p]) => $p.host === hid)
		.sort((a, b) => a[1].name.localeCompare(b[1].name));
}

/**
 * Remove a project from its host, once confirmed; used from its box and from
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

/** The right column at "/": a column per host, holding the projects listed there. */
export function drawProjectList(): void {
	A('div display:grid gap:$3 align-items:start grid-template-columns: repeat(auto-fill, minmax(17rem, 1fr));', () => {
		// This machine leads; the rest follow by name, and the way to add one closes the row.
		A.onEach($state.hosts, ($h: any, hid: string) => drawHostBox(hid, $h), (_$h: any, hid: string) => hid === 'local' ? '' : '1' + hostName(hid).toLowerCase());
		drawAddHost();
	});
}

/** One host: what it is up to, its projects, and the way to add one there. */
function drawHostBox(hid: string, $h: any): void {
	A('div min-width:0', () => {
		S.addContextMenu({ get items(): S.MenuEntry[] { return hostItems(hid, $h); } });
		S.box({
			attrs: 'min-width:0',
			headerAttrs: 'display:flex align-items:center gap:$2 min-width:0',
			contentAttrs: 'display:flex flex-direction:column gap:$2 p:$2',
			header: () => {
				A('span display:flex flex-shrink:0', () => (hid === 'local' ? monitor : server)({ size: '1.1em' }));
				A(`span flex:1 ${ELLIPSIS} text=`, hostName(hid));
				A(() => drawHostDot($h));
				S.iconButton({
					icon: ellipsisVertical, ariaLabel: 'Host menu', attrs: '.small',
					click: e => void S.showFloatingMenu({ items: hostItems(hid, $h), anchor: e.currentTarget as HTMLElement }),
				});
			},
			content: () => {
				A(() => drawHostState(hid, $h));
				A(() => {
					const projects = projectsOn(hid);
					if (!projects.length) A('small fg:$s-muted #No projects here yet.');
					for (const [pid, $p] of projects) drawProjectBox(pid, $p);
				});
				A('button', linkButton, 'click=', () => addProjectDialog(hid), () => {
					plus({ size: '1em' });
					A('span#Add project');
				});
			},
		});
	});
}

/** The host's state at a glance: a dot in its header, colored by what it is doing. */
function drawHostDot($h: any): void {
	const status = $h.status ?? 'connecting';
	const color = status === 'connected' ? (($h.warning || $h.updatable) ? 'warning' : 'success')
		: status === 'connecting' || status === 'updating' ? 'warning'
			: status === 'stopped' ? 'muted' : 'danger';
	A(`span flex-shrink:0 w:0.6em h:0.6em r:50% bg:$s-${color}`, () => S.addTooltip({ tip: status + ($h.error ? ' · ' + $h.error : '') }));
}

/** Anything about the host that asks for a look, or a click. */
function drawHostState(hid: string, $h: any): void {
	if ($h.status !== 'connected') {
		const waiting = $h.status === 'connecting' || $h.status === 'updating';
		drawStrip(waiting ? 'neutral' : $h.status === 'stopped' ? 'neutral' : 'danger',
			`${$h.status}${$h.error ? ' · ' + $h.error : ''}`,
			waiting ? undefined : () => S.button({ content: 'Connect', attrs: '.small', click: () => void cmd('connectHost', { hid }) }));
	} else if ($h.warning) {
		drawStrip('warning', $h.warning);
	} else if ($h.restarting) {
		drawStrip('neutral', 'The daemon restarts into this build as soon as nothing is running.');
	} else if ($h.updatable) {
		drawStrip('neutral', 'The daemon runs another build of TPS.',
			() => S.button({ content: 'Update', attrs: '.small', click: () => void cmd('updateDaemon', { hid }) }));
	}
}

/** What the host's menu offers, from its header button and its context menu. */
function hostItems(hid: string, $h: any): S.MenuEntry[] {
	const items: S.MenuEntry[] = [{ label: 'Add project…', icon: plus, click: () => addProjectDialog(hid) }, { separator: true }];
	if ($h.status !== 'connected') items.push({ label: 'Connect', icon: plug, click: () => void cmd('connectHost', { hid }) });
	if ($h.updatable) items.push({ label: 'Update daemon', icon: download, click: () => void cmd('updateDaemon', { hid }) });
	if (hid !== 'local') {
		items.push({ label: 'Copy claude login to host', icon: keyRound, click: async () => {
			if (await cmd('copyCredentials', { hid })) S.toast({ message: `Your claude login is now on ${hostName(hid)}`, type: 'success' });
		}});
	}
	items.push({ label: 'Stop daemon', icon: power, click: async () => {
		if (await S.confirm(`Stop the TPS daemon on ${hostName(hid)}? Its running workspaces are shut down; Connect starts it again.`)) void cmd('stopDaemon', { hid });
	}});
	if (hid !== 'local') {
		items.push({ separator: true }, { label: 'Remove host', icon: trash2, click: () => void removeHost(hid) });
	}
	return items;
}

async function removeHost(hid: string): Promise<void> {
	const projects = A.peek(() => projectsOn(hid).length);
	const also = projects ? ` Its ${projects} project${projects > 1 ? 's' : ''} leave${projects > 1 ? '' : 's'} the list with it.` : '';
	if (!(await S.confirm(`Remove "${A.peek(() => hostName(hid))}" from the list?${also} Nothing changes on the host itself: its daemon and tasks keep running, and come back when you add it again.`))) return;
	void cmd('removeHost', { hid });
}

/** One project in its host's column: its name and where it lives, over its tasks. */
function drawProjectBox(pid: string, $p: any): void {
	// Its own context menu, which must not also open the host's.
	A('div', 'contextmenu=', (e: Event) => e.stopPropagation(), () => {
		S.addContextMenu({ link: pathTo(pid), get items(): S.MenuEntry[] {
			return [
				{ label: 'Rename…', icon: pencil, click: () => void renameProject(pid, $p) },
				{ label: 'Settings…', icon: settings, click: () => projectSettingsDialog(pid, $p) },
				{ separator: true },
				{ label: 'Remove project…', icon: trash2, click: () => void removeProject(pid, $p) },
			];
		}});
		A('a display:block fg:$s-text text-decoration:none', 'href=', pathTo(pid), () => {
			S.box({ attrs: 'cursor:pointer', contentAttrs: 'display:flex flex-direction:column gap:$1 min-width:0 p:$2', content: () => {
				A('div display:flex align-items:baseline gap:$2 min-width:0', () => {
					// The directory takes what the name leaves, and is cut off before it.
					A(`span min-width:0 ${ELLIPSIS} font-weight:600 text=`, A.ref($p, 'name'));
					// flex:1 gives it a zero base size, so it takes the space the
					// name leaves and is the one to be cut off when there is none.
					A(`small flex:1 min-width:0 ${ELLIPSIS} fg:$s-muted`, () => A('text=', shortDir($p.host, $p.dir)));
					A(() => {
						if ($p.error) A('span flex-shrink:0 fg:$s-danger #⚠', () => S.addTooltip({ tip: $p.error }));
					});
				});
				A(() => drawPhaseCounts(pid));
			}});
		});
	});
}

/** What the project's tasks are up to: a phase icon per phase that has any, and how many. */
function drawPhaseCounts(pid: string): void {
	const counts = {} as Record<string, number>;
	for (const $t of Object.values($state.projects[pid]?.tasks ?? {}) as any[]) counts[$t.phase] = (counts[$t.phase] ?? 0) + 1;
	A('div display:flex flex-wrap:wrap align-items:center gap:$2', () => {
		let any = false;
		for (const phase of PHASES) {
			const n = counts[phase];
			if (!n) continue;
			any = true;
			const icon = PHASE_ICONS[phase];
			// Waiting for a human is the one thing here worth looking at twice.
			A(`span display:inline-flex align-items:center gap:0.25em fg:$s-${phase === 'human' ? 'warning' : 'muted'}`, () => {
				S.addTooltip({ tip: `${n} task${n > 1 ? 's' : ''} in ${PHASE_LABELS[phase]}` });
				icon({ size: '0.95em' });
				A('small text=', String(n));
			});
		}
		if (!any) A('small fg:$s-muted #No tasks yet');
	});
}

/**
 * Adding a project: a directory on the host whose column it was started from.
 * The name follows the directory as it is typed, until the name is typed in
 * itself — from then on it is the user's.
 */
function addProjectDialog(hid: string): void {
	const $form = A.proxy({ dir: '', name: '' });
	let named = false; // the name is the user's own now
	void S.dialog({ header: `Add project on ${hostName(hid)}`, attrs: 'w:32rem', content: close => {
		S.form({
			submit: async () => {
				if (!$form.dir.trim()) return;
				const result = await cmd('addProject', { hid, dir: $form.dir.trim(), name: $form.name.trim() });
				if (!result) return;
				close();
				route.go(pathTo(result.pid));
			},
			content: () => {
				A('p rich=', `A directory on ${hostName(hid)} holding a git repository. Work there goes on while this dashboard is closed.`);
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

// A column that is not a host yet: it keeps a host's shape, quietly, until
// pointed at.
const addHostColumn = A.insertCss({
	'&': 'cursor:pointer opacity:0.6 transition: opacity 0.12s;',
	'&:hover, &:focus-visible': 'opacity:1',
});

/** The last column: the way to add a host, shaped like the ones it stands next to. */
function drawAddHost(): void {
	A('div min-width:0 role=button tabindex=0', addHostColumn,
		'click=', addHostDialog,
		'keydown=', (e: KeyboardEvent) => {
			if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); addHostDialog(); }
		},
		() => {
			S.box({
				attrs: '.no-shadow',
				headerAttrs: 'display:flex align-items:center gap:$2 min-width:0',
				contentAttrs: 'p:$2',
				header: () => {
					A('span display:flex flex-shrink:0', () => plus({ size: '1.1em' }));
					A(`span flex:1 ${ELLIPSIS} #Add host`);
				},
				content: () => A('small fg:$s-muted #Another machine to run projects on, over SSH.'),
			});
		});
}

/** Adding a host: an ssh destination. Connecting to it happens in its column. */
function addHostDialog(): void {
	const $form = A.proxy({ dest: '' });
	void S.dialog({ header: 'Add host', attrs: 'w:32rem', content: close => {
		S.form({
			submit: async () => {
				if (!$form.dest.trim()) return;
				if (!(await cmd('addHost', { dest: $form.dest.trim() }))) return;
				close();
			},
			content: () => {
				A('p rich=', 'A host you reach over SSH: `user@host`, or an alias from `~/.ssh/config` (options like `-p 2222` go in front). TPS installs its daemon there and adopts the projects it already has.');
				S.textline({ label: 'Host', placeholder: 'user@host', required: true, bind: A.ref($form, 'dest') });
			},
			actions: () => S.button({ content: 'Add', icon: plus, type: 'submit' }),
		});
	}});
}

/** A line about something that wants attention, with what to do about it beside it. */
function drawStrip(color: string, text: string, action?: () => void): void {
	A(`div.s-s.${color}.tonal p:$2 r:$s-radius-sm display:flex flex-wrap:wrap align-items:center gap:$2`, () => {
		A('span flex:1 rich=', text);
		action?.();
	});
}

/** A strip per thing worth knowing: the host's state, the project, its checkout. */
export function drawNotices(pid: string, $p: any): void {
	A(() => {
		const $h = $state.hosts?.[$p.host];
		if (!$h) return;
		if ($h.status !== 'connected') {
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

/** The name, the merge behaviour, the host it lives on, and the way to remove it. */
export function projectSettingsDialog(pid: string, $p: any): void {
	void S.dialog({ header: 'Project settings', attrs: 'w:36rem', content: close => {
		S.textline({
			label: 'Name', value: A.peek($p, 'name') ?? '',
			change: (e: Event) => {
				const name = (e.target as HTMLInputElement).value.trim();
				if (name) void cmd('setProject', { pid, name });
			},
		});
		S.checkbox({
			label: 'Merge without confirmation',
			help: 'When the agent reports a task ready, merge it right away instead of waiting for you. Individual tasks can override this in their settings.',
			checked: !!A.peek($p, 'autoMerge'),
			change: (e: Event) => void cmd('setProject', { pid, autoMerge: (e.target as HTMLInputElement).checked }),
		});
		// Where it lives, for reference: the host itself is managed from its
		// column on the front page.
		A('div.s-field', () => {
			A('label #Host');
			A(() => {
				const $h = $state.hosts?.[$p.host];
				A('div text=', `${$p.host === 'local' ? 'This machine' : $h?.dest ?? $p.host} · ${$h?.status ?? 'unknown'}`);
				A('div.s-help text=', shortDir($p.host, $p.dir));
			});
		});
		A('div display:flex mt:$2', () => S.button({
			content: 'Remove project', attrs: '.small .danger .outlined',
			click: async () => { if (await removeProject(pid, $p)) close(); },
		}));
	}});
}

/** The right column for the base worktree: VS Code on the project's own checkout. */
export function drawProjectCode(pid: string, $p: any): void {
	const start = () => void cmd('openProjectCode', { pid });
	A(start);
	A(() => {
		if ($p.codePort) {
			// code-server's remote authority is the Host header, which the proxy passes on unchanged.
			drawCode(`${pid}/-`, `/code/${pid}/-/?folder=${encodeURIComponent($p.dir)}`);
			return;
		}
		S.box({ contentAttrs: 'display:flex flex-direction:column align-items:flex-start', content: () => {
			if ($p.codeError) A('p fg:$s-danger text=', $p.codeError);
			else { A('p#Starting VS Code on the checkout…'); A('progress w:100%'); }
			S.button({ content: 'Retry', attrs: '.small .neutral', click: start });
		}});
	});
}
