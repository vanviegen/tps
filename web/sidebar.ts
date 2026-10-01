import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { chevronDown, chevronLeft, chevronRight, folder, panelLeftClose, panelLeftOpen, plus, settings, x } from 'staffa/icons.js';
import { bot } from './bot.ts';
import { $state } from './conn.ts';
import { addHostDialog, hostInk, hostMenuItems, hostOrder, hostState } from './hosts.ts';
import { closeBase, projectMenuItems, projectSortKey, reorderProjects } from './projects.ts';
import { taskMenuItems, taskSettingsDialog } from './task.ts';
import { drawBadge, drawTaskIcon, hostName, hostPath, pathTo, phaseOrder, projectColor, selection, taskTip, taskTitle } from './util.ts';

/**
 * The sidebar: the way around the dashboard, and the list of what is open.
 *
 * Under the logo, every host, and on each the projects it holds in the order
 * the user put them in; under each project, the tasks that are *open*: those
 * waiting for a human, those an agent is working on, and the one on screen.
 *
 * What belongs together is told by the space between it rather than by lines.
 * A host is a small label over a block with rounded outer corners; its
 * projects are the tiles that block is made of, held apart by a sliver of the
 * sidebar showing through, each on a wash of the project's colour, which its
 * name is in too. Nothing is indented: that would cost the width the
 * collapsed strip has none of. A host folds away by its chevron, and folded
 * says how many of its tasks wait for you.
 *
 * Nothing here moves: what is going on is the board's story and the task's
 * own, and a list one glances at is the last place for an animation.
 *
 * Collapsed, it is a narrow strip of names and nothing else, each cut off at
 * its edge, which the `subject: change` form of task titles makes readable;
 * what is said there beyond the name is said by its ink, and spelled out by
 * the tooltip. It folds by the icon in the corner, which is in the same spot
 * either way so that one spot folds it both ways, and stays wherever it was put.
 */
export const $ui = A.proxy({ collapsed: false, folded: {} as Record<string, boolean> });

export const SIDEBAR_OPEN = '16rem';
export const SIDEBAR_CLOSED = '4.5rem';

A.insertGlobalCss({
	'.tps-side': 'display:flex flex-direction:column h:100% min-width:0 overflow:hidden border-right: 1px solid $s-faint;',
	'.tps-side .tps-host': 'margin: 0 $1 $3 $1;',
	'.tps-side .tps-tiles': 'display:flex flex-direction:column gap:2px r:$s-radius-sm overflow:hidden',
	'.tps-side .tps-tile': 'pb:0.15em background: color-mix(in oklab, var(--tps-color), transparent 90%);',
	'.tps-side .tps-row': 'display:flex align-items:center gap:$2 min-width:0 text-decoration:none fg:$s-text border-right: 4px solid transparent; transition: background 0.12s, border-color 0.12s;',
	'.tps-side .tps-row:hover': 'background: color-mix(in oklab, $s-text, transparent 92%);',
	// What you are looking at is marked by a bar of its project's colour at the edge.
	'.tps-side .tps-row.tps-current': 'border-right-color: var(--tps-color);',
	'.tps-side .tps-hosthead': 'ph:$1 pt:0.2em pb:0.35em gap:0.3em font-size:0.75em font-weight:600 letter-spacing:0.06em text-transform:uppercase fg:$s-muted r:$s-radius-sm',
	// A label has no project colour: its bar is in its own ink.
	'.tps-side .tps-hosthead.tps-current': 'border-right-color:currentColor',
	'.tps-side .tps-project': 'ph:$1 pv:0.3em font-weight:600 fg:var(--tps-color)',
	// A task is in its project's colour, its icon in full and its title dimmed
	// towards the muted text; the icon's shape says the phase.
	'.tps-side .tps-task': 'ph:$1 pv:0.15em font-size:0.9em fg: color-mix(in oklab, var(--tps-color), $s-muted 55%);',
	// Waiting for you is said by coming out of the dim the other rows are in.
	'.tps-side .tps-task.tps-yours': 'fg:$s-text',
	'.tps-side .tps-clip': 'flex:1 min-width:0 white-space:nowrap overflow:hidden text-overflow:ellipsis',
	// The chevron keeps out of sight until the label is pointed at, unless it
	// is folded, which it is there to say.
	'.tps-side .tps-fold': 'display:inline-flex flex-shrink:0 border:0 p:0 bg:transparent fg:inherit cursor:pointer opacity:0 transition: opacity 0.12s;',
	'.tps-side .tps-hosthead:hover .tps-fold, .tps-side .tps-fold.tps-folded': 'opacity:0.8',
	'.tps-side .tps-fold:hover': 'opacity:1',
	'.tps-side.tps-collapsed .tps-host': 'margin: 0 0.3rem $3 0.3rem;',
	// Collapsed, the label gives its chevron and its capitals to the name.
	'.tps-side.tps-collapsed .tps-hosthead': 'ph:0.3em text-transform:none letter-spacing:0',
	'.tps-side.tps-collapsed .tps-fold': 'display:none',
	'.tps-side.tps-collapsed .tps-project': 'ph:0.3em pv:0.35em',
	'.tps-side.tps-collapsed .tps-task': 'ph:0.3em',
	// Every character the strip holds is a character of the name, so a title
	// runs off its edge rather than spending three of them on saying that it does.
	'.tps-side.tps-collapsed .tps-clip': 'text-overflow:clip',
	'.tps-side .tps-hostissue': 'ph:$1 pb:$1 font-size:0.85em cursor:pointer',
	'.tps-side .tps-hostissue:hover': 'background:none text-decoration:underline',
	'.tps-side.tps-collapsed .tps-hostissue': 'ph:0.3em',
	'.tps-side .tps-addhost': 'ph:$1 pv:0.3em font-size:0.85em fg:$s-muted r:$s-radius-sm gap:0.3em cursor:pointer',
});

export function drawSidebar(): void {
	A('nav.tps-side', () => {
		A(() => A('.tps-collapsed=', $ui.collapsed));
		A(() => drawHeader($ui.collapsed));
		A('div flex:1 min-height:0 overflow-y:auto overflow-x:hidden pv:$1', () => {
			A.onEach($state.hosts, ($h: any, hid: string) => drawHost(hid, $h), (_$h: any, hid: string) => hostOrder(hid));
			A('a.tps-row.tps-addhost margin: 0 $1;', 'click=', () => void addHostDialog(), () => {
				A('span display:inline-flex flex-shrink:0', () => plus({ size: '1em' }));
				A(() => {
					if ($ui.collapsed) S.addTooltip({ tip: 'Add host', placement: 'right' });
					else A('span.tps-clip #Add host');
				});
			});
		});
	});
}

/**
 * The way to fold the sidebar, in the corner it keeps either way, and the logo
 * beside it while there is room for it, leading to the front page. Anywhere
 * else, the other corner leads back a level: from a task or the project
 * directory to its project's board, and from there to the front page.
 */
function drawHeader(collapsed: boolean): void {
	A('div display:flex align-items:center gap:$2 ph:$1 pv:$2', () => {
		S.iconButton({
			icon: collapsed ? panelLeftOpen : panelLeftClose,
			ariaLabel: collapsed ? 'Expand the sidebar' : 'Collapse the sidebar',
			key: 'mod+b', attrs: '.small',
			click: () => { $ui.collapsed = !collapsed; },
		});
		if (!collapsed) {
			A('a href=/ display:flex align-items:center gap:$2 flex:1 text-decoration:none fg:$s-text', () => {
				bot({ size: '1.4em', color: 'var(--s-accent)' });
				A('b #TPS');
			});
		}
		A(() => {
			const { pid, tid } = selection();
			if (route.current.path === '/') return;
			S.iconButton({
				icon: chevronLeft, ariaLabel: pid && tid ? 'Back to the board' : 'Back to all projects', attrs: '.small margin-left:auto',
				click: () => void route.back(pid && tid ? pathTo(pid) : '/'),
			});
		});
	});
}

/**
 * One host: its label, leading to its page, and under it the block of its
 * projects. A host that wants something now (a password, a host key, a
 * connection to make) says so above them, in a word or two of its ink that do
 * it; the tooltip, and the host's page, spell out what it is. Until it is
 * connected there are no projects to show, so what it is doing takes their place.
 */
function drawHost(hid: string, $h: any): void {
	A('div.tps-host', () => {
		const state = hostState(hid, $h);
		const folded = !!$ui.folded[hid];
		A('a.tps-row.tps-hosthead', 'href=', hostPath(hid), () => {
			S.addContextMenu({ link: hostPath(hid), get items(): S.MenuEntry[] { return hostMenuItems(hid, $h); } });
			A(() => A('.tps-current=', selection().hid === hid));
			S.addTooltip({ tip: () => A('text=', `${hostName(hid)}: ${state.text}`), placement: 'right' });
			A('span.tps-clip text=', hostName(hid));
			if (folded) drawBadge(waiting(hid));
			A('button.tps-fold type=button', 'aria-label=', folded ? 'Show its projects' : 'Hide its projects',
				'.tps-folded=', folded,
				'click=', (e: Event) => { e.preventDefault(); $ui.folded[hid] = !folded; },
				() => (folded ? chevronRight : chevronDown)({ size: '1.2em' }));
		});
		if (folded) return;
		if (state.color === 'warning' || state.color === 'danger' || $h.status !== 'connected') {
			A(`a.tps-row.tps-hostissue fg:${hostInk(state.color)}`, () => {
				if (state.action) A('click=', state.action.run);
				else A('href=', hostPath(hid));
				S.addTooltip({ tip: state.text, placement: 'right' });
				A('span.tps-clip text=', state.action?.label ?? (state.color === 'neutral' ? state.text : 'Needs attention'));
			});
		}
		A('div.tps-tiles', () => {
			A.onEach($state.projects, ($p: any, pid: string) => {
				if ($p.host === hid) drawProject(pid, $p);
			}, projectSortKey);
		});
	});
}

/** How many of the host's tasks wait for a human: what folding it must not hide. */
function waiting(hid: string): number {
	let count = 0;
	for (const $p of Object.values($state.projects) as any[]) {
		if ($p.host !== hid) continue;
		for (const $t of Object.values($p.tasks ?? {}) as any[]) if ($t.phase === 'human') count++;
	}
	return count;
}

// Browsers can fire a click on the row a drag started from once that drag
// ends, which would open the project the user just moved. Any real click
// starts with a pointerdown, so that is where the flag is cleared.
let dragged = false;

/** One project, with the tasks open in it under it: a tile of its host's block, in the project's colour. */
function drawProject(pid: string, $p: any): void {
	A('div.tps-tile', () => {
		A(() => A('--tps-color:' + projectColor($p)));
		A('a.tps-row.tps-project draggable=true', 'href=', pathTo(pid), () => {
			S.addContextMenu({ link: pathTo(pid), get items(): S.MenuEntry[] { return projectMenuItems(pid, $p); } });
			A('pointerdown=', () => { dragged = false; },
				'dragstart=', (e: DragEvent) => { dragged = true; e.dataTransfer?.setData('text/tps-project', pid); },
				'dragover=', (e: DragEvent) => { if (e.dataTransfer?.types.includes('text/tps-project')) e.preventDefault(); },
				'drop=', (e: DragEvent) => {
					const moved = e.dataTransfer?.getData('text/tps-project');
					if (!moved || moved === pid) return;
					e.preventDefault();
					reorderProjects(moved, pid);
				},
				'click=', (e: MouseEvent) => { if (dragged) e.preventDefault(); });
			A(() => {
				const { pid: shown, tid } = selection();
				A('.tps-current=', shown === pid && !tid);
			});
			A(() => {
				if ($ui.collapsed) S.addTooltip({ tip: () => A('text=', $p.name), placement: 'right' });
			});
			A('span.tps-clip text=', A.ref($p, 'name'));
			A(() => {
				if ($p.error) A('span flex-shrink:0 fg:$s-danger #⚠', () => S.addTooltip({ tip: $p.error }));
			});
		});
		A.onEach($p.tasks, ($t: any, tid: string) => {
			if (!isOpen(pid, tid, $t)) return;
			drawTask(pid, tid, $t);
		}, phaseOrder);
		A(() => {
			const { pid: shown, tid } = selection();
			if ($p.codePort || (shown === pid && tid === 'base')) drawBase(pid);
		});
	});
}

/**
 * A task is listed while it waits for a human, while an agent is on it (its
 * own, the one reviewing its work, or its merge), and while it is on screen: everything
 * under way, in other words. A muted one is none of those: putting it away is
 * what muting is for, and opening it takes it back out (see useTask).
 */
function isOpen(pid: string, tid: string, $t: any): boolean {
	const { pid: shown, tid: shownTid } = selection();
	if (shown === pid && shownTid === tid) return true;
	if ($t.phase === 'muted') return false;
	return $t.phase === 'human' || $t.phase === 'agent' || $t.phase === 'review' || $t.phase === 'merge';
}

function drawTask(pid: string, tid: string, $t: any): void {
	A('a.tps-row.tps-task', 'href=', pathTo(pid, tid), () => {
		S.addContextMenu({ link: pathTo(pid, tid), get items(): S.MenuEntry[] {
			return taskMenuItems(pid, tid, $t, [{ label: 'Settings…', icon: settings, click: () => taskSettingsDialog(pid, tid, $t) }]);
		}});
		// Yours to act on: it waits for you, or it is a plan you have yet to start.
		A(() => A('.tps-yours=', $t.phase === 'human' || $t.phase === 'plan'));
		A(() => {
			const { pid: shown, tid: shownTid } = selection();
			A('.tps-current=', shown === pid && shownTid === tid);
		});
		A(() => {
			// Collapsed, the row is the title and nothing else: the icon's width is
			// a word of the title, and what it says — the phase, and the work going
			// on — the tooltip spells out, along with the rest of the title itself.
			if ($ui.collapsed) {
				S.addTooltip({ placement: 'right', tip: () => {
					A('div text=', taskTitle($t));
					A('div font-size:0.85em fg:$s-muted text=', taskTip(pid, $t));
				} });
				return;
			}
			drawTaskIcon(pid, $t, { busy: false, color: 'var(--tps-color)' });
		});
		A('span.tps-clip', () => A('text=', taskTitle($t)));
	});
}

/** The project's own checkout, listed like a task while VS Code runs on it. */
function drawBase(pid: string): void {
	A('a.tps-row.tps-task', 'href=', pathTo(pid, 'base'), () => {
		S.addContextMenu({ link: pathTo(pid, 'base'), items: [{ label: 'Close project directory', icon: x, click: () => closeBase(pid) }] });
		A(() => {
			const { pid: shown, tid } = selection();
			A('.tps-current=', shown === pid && tid === 'base');
		});
		A(() => {
			// Collapsed the row is its text and nothing else, as a task's is; the
			// italic is what tells the checkout from a task either way.
			if ($ui.collapsed) S.addTooltip({ tip: 'Project directory', placement: 'right' });
			else A('span display:inline-flex flex-shrink:0 fg:var(--tps-color)', () => folder({ size: '1.1em' }));
			A('span.tps-clip font-style:italic text=', 'Project directory');
		});
	});
}
