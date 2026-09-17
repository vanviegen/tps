import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { gitBranch, panelLeftClose, panelLeftOpen, plus, server, settings, x } from 'staffa/icons.js';
import { bot } from './bot.ts';
import { $state } from './conn.ts';
import { $holds, holdKey, isHeld, release } from './holds.ts';
import { hostIssue, manageHostsDialog } from './hosts.ts';
import { addProjectDialog, projectMenuItems, projectSortKey, reorderProjects } from './projects.ts';
import { closeTask, taskMenuItems, taskSettingsDialog } from './task.ts';
import { branchLabel, drawStrip, drawTaskIcon, hostName, pathTo, phaseOrder, projectColor, selection, taskTip, taskTitle } from './util.ts';

/**
 * The sidebar: the way around the dashboard, and the list of what is open.
 *
 * Under the logo, every project in the order the user put them in, on a wash
 * of its colour; under each, the tasks that are *open*: those waiting for a
 * human (the bright ones among them), those an agent is working on, those this
 * dashboard holds VS Code on (see holds.ts), and the one on screen. Then Add project and Manage hosts, and
 * at the bottom only the hosts that want something — a login, an update, a
 * connection — as the ones that work have nothing to say.
 *
 * Collapsed, it is a narrow strip: every row keeps its name, cut off where the
 * strip ends, which the `subject: change` form of task titles makes readable,
 * and its phase icon. It collapses by itself when VS Code comes on screen (see
 * main.ts), and by the icon in the corner, which is in the same spot either
 * way so that one spot folds it both ways.
 */
export const $ui = A.proxy({ collapsed: false });

export const SIDEBAR_OPEN = '16rem';
export const SIDEBAR_CLOSED = '4.5rem';

A.insertGlobalCss({
	'.tps-side': 'display:flex flex-direction:column h:100% min-width:0 overflow:hidden border-right: 1px solid $s-faint;',
	// A project and its tasks are one block because they sit on one wash of the
	// project's colour, rather than beside a bar in it: colour behind the rows
	// costs no width, which is what the collapsed strip has none of to spare.
	'.tps-side .tps-group': 'margin: 0 $1 $1 $1; r:$s-radius-sm overflow:hidden background: color-mix(in oklab, var(--tps-color), transparent 90%);',
	// The border every row carries is the room the current one's edge takes, so
	// that marking it moves nothing.
	'.tps-side .tps-row': 'display:flex align-items:center gap:$2 min-width:0 text-decoration:none fg:$s-text border-right: 3px solid transparent; transition: background 0.12s, border-color 0.12s;',
	'.tps-side .tps-row:hover': 'background: color-mix(in oklab, $s-text, transparent 90%);',
	// What you are looking at is an edge in the project's colour, rather than a
	// lighter background: a row lit up reads as one lifted out of the group,
	// while the edge belongs to the group as much as the wash under it does.
	'.tps-side .tps-row.tps-current': 'border-right-color: var(--tps-color);',
	// The project's colour is on its name, rather than on a chip beside it:
	// letters are recognised as well as initials are, and cost the strip no width.
	'.tps-side .tps-project': 'ph:$1 pv:0.3em font-weight:600 fg:var(--tps-color)',
	'.tps-side .tps-task': 'ph:$1 pv:0.15em font-size:0.9em fg:$s-muted',
	// Waiting for you is said by coming out of the dim the other rows are in:
	// the colours here belong to the projects, and the phase icon says the rest.
	'.tps-side .tps-task.tps-human, .tps-side .tps-task.tps-current': 'fg:$s-text',
	'.tps-side .tps-clip': 'flex:1 min-width:0 white-space:nowrap overflow:hidden text-overflow:ellipsis',
	'.tps-side.tps-collapsed .tps-group': 'margin: 0 0.3rem $1 0.3rem;',
	'.tps-side.tps-collapsed .tps-project': 'ph:0.3em pv:0.35em',
	'.tps-side.tps-collapsed .tps-task': 'ph:0.3em gap:0.3em',
});

export function drawSidebar(): void {
	A('nav.tps-side', () => {
		A(() => A('.tps-collapsed=', $ui.collapsed));
		A(() => drawHeader($ui.collapsed));
		A('div flex:1 min-height:0 overflow-y:auto overflow-x:hidden pv:$1', () => {
			A.onEach($state.projects, ($p: any, pid: string) => drawProject(pid, $p), projectSortKey);
		});
		A(() => drawButtons($ui.collapsed));
		A('div display:flex flex-direction:column gap:$1 p:$1', () => {
			A.onEach($state.hosts, ($h: any, hid: string) => drawHostIssue(hid, $h), (_$h: any, hid: string) => hid === 'local' ? '' : hostName(hid));
		});
	});
}

/** The way to fold the sidebar, in the corner it keeps either way, and the logo beside it while there is room for it. */
function drawHeader(collapsed: boolean): void {
	A('div display:flex align-items:center gap:$2 ph:$1 pv:$2', () => {
		S.iconButton({
			icon: collapsed ? panelLeftOpen : panelLeftClose,
			ariaLabel: collapsed ? 'Expand the sidebar' : 'Collapse the sidebar',
			key: 'mod+b', attrs: '.small',
			click: () => { $ui.collapsed = !collapsed; },
		});
		if (collapsed) return;
		bot({ size: '1.4em', color: 'var(--s-accent)' });
		A('b flex:1 #TPS');
	});
}

// Browsers can fire a click on the row a drag started from once that drag
// ends, which would open the project the user just moved. Any real click
// starts with a pointerdown, so that is where the flag is cleared.
let dragged = false;

/** One project, with the tasks open in it under it: a group in the project's colour. */
function drawProject(pid: string, $p: any): void {
	A('div.tps-group', () => {
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
				const { pid: shown, tid, base } = selection();
				A('.tps-current=', shown === pid && !tid && !base);
			});
			A(() => {
				if ($ui.collapsed) S.addTooltip({ tip: () => A('text=', $p.name), placement: 'right' });
			});
			A('span.tps-clip text=', A.ref($p, 'name'));
			A(() => {
				if ($p.error) A('span flex-shrink:0 fg:$s-danger #⚠', () => S.addTooltip({ tip: $p.error }));
			});
		});
		A('div', () => {
			A.onEach($p.tasks, ($t: any, tid: string) => {
				if (!isOpen(pid, tid, $t)) return;
				drawTask(pid, tid, $t);
			}, phaseOrder);
			A(() => {
				const { pid: shown, base } = selection();
				if (isHeld(pid) || (shown === pid && base)) drawBase(pid, $p);
			});
		});
	});
}

/**
 * A task is listed while it waits for a human, while an agent is on it (its
 * own, or the one reviewing its work), while this dashboard holds it, and while
 * it is on screen: everything under way, in other words. A muted one is none of those: putting it away is what muting is
 * for, and opening it takes it back out (see useTask).
 */
function isOpen(pid: string, tid: string, $t: any): boolean {
	const { pid: shown, tid: shownTid } = selection();
	if (shown === pid && shownTid === tid) return true;
	if ($t.phase === 'muted') return false;
	return $t.phase === 'human' || $t.phase === 'agent' || $t.phase === 'review' || !!$holds[holdKey(pid, tid)];
}

function drawTask(pid: string, tid: string, $t: any): void {
	A('a.tps-row.tps-task', 'href=', pathTo(pid, tid), () => {
		S.addContextMenu({ link: pathTo(pid, tid), get items(): S.MenuEntry[] {
			const extra: S.MenuEntry[] = [{ label: 'Settings…', icon: settings, click: () => taskSettingsDialog(pid, tid, $t) }];
			if (A.peek(() => isHeld(pid, tid))) extra.push({ label: 'Close VS Code', icon: x, click: () => closeTask(pid, tid) });
			return taskMenuItems(pid, tid, $t, extra);
		}});
		A(() => A('.tps-human=', $t.phase === 'human'));
		A(() => {
			const { pid: shown, tid: shownTid } = selection();
			A('.tps-current=', shown === pid && shownTid === tid);
		});
		A(() => {
			const collapsed = $ui.collapsed;
			// Collapsed, the row shows the phase icon and as much of the title as
			// the strip holds; the rest of it, and what the icon's own tooltip
			// would have said, is what hovering the row is for.
			if (collapsed) {
				S.addTooltip({ placement: 'right', tip: () => {
					A('div text=', taskTitle($t));
					A('div font-size:0.85em fg:$s-muted text=', taskTip(pid, $t));
				} });
			}
			drawTaskIcon(pid, $t, { tip: !collapsed, color: '$s-text' });
		});
		A('span.tps-clip', () => A('text=', taskTitle($t)));
	});
}

/** The project's own checkout, listed like a task while it is open in VS Code. */
function drawBase(pid: string, $p: any): void {
	A('a.tps-row.tps-task', 'href=', pathTo(pid, 'base'), () => {
		S.addContextMenu({ link: pathTo(pid, 'base'), get items(): S.MenuEntry[] {
			return [{ label: 'Close VS Code', icon: x, click: () => closeBase(pid) }];
		}});
		A(() => {
			const { pid: shown, base } = selection();
			A('.tps-current=', shown === pid && !!base);
		});
		A(() => {
			const branch = $p.defaultBranch ?? 'main';
			const label = branchLabel($p);
			// Collapsed there is room for a word: the icon beside it already says
			// which word it is, so the branch goes without "branch" behind it.
			if ($ui.collapsed) S.addTooltip({ tip: label, placement: 'right' });
			A('span display:inline-flex flex-shrink:0 fg:var(--tps-color)', () => gitBranch({ size: '1.1em' }));
			A('span.tps-clip text=', $ui.collapsed ? branch : label);
		});
	});
}

/** Let go of the project's checkout: VS Code on it is stopped, and its row goes. */
export function closeBase(pid: string): void {
	release(pid);
	const shown = A.peek(selection);
	if (shown.pid === pid && shown.base) void route.go(pathTo(pid));
}

function drawButtons(collapsed: boolean): void {
	if (collapsed) {
		A('div display:flex flex-wrap:wrap justify-content:center gap:$1 pv:$1', () => {
			S.iconButton({ icon: plus, ariaLabel: 'Add project', attrs: '.small', click: () => addProjectDialog() });
			S.iconButton({ icon: server, ariaLabel: 'Manage hosts', attrs: '.small', click: () => manageHostsDialog() });
		});
	} else {
		A('div display:flex gap:$1 p:$1', () => {
			S.button({ content: 'Add project', icon: plus, attrs: '.small .neutral flex:1', click: () => addProjectDialog() });
			S.button({ content: 'Manage hosts', icon: server, attrs: '.small .neutral flex:1', click: () => manageHostsDialog() });
		});
	}
}

/** A host that wants something, as a strip (or, collapsed, an icon) that leads to it. Working hosts draw nothing. */
function drawHostIssue(hid: string, $h: any): void {
	A(() => {
		const issue = hostIssue(hid, $h);
		if (!issue) return;
		const name = hostName(hid);
		if ($ui.collapsed) {
			A('div display:flex justify-content:center', () => {
				S.iconButton({ icon: issue.icon, ariaLabel: `${name}: ${issue.text}`, attrs: `.small fg:$s-${issue.color}`, click: issue.click });
			});
		} else {
			// A line or two: the whole story (an ssh error can be a paragraph) is in the hosts dialog the strip leads to.
			const text = issue.text.length > 70 ? issue.text.slice(0, 70).trimEnd() + '…' : issue.text;
			A('div font-size:0.85em', () => {
				S.addTooltip({ tip: () => A('text=', `${name}: ${issue.text}`), placement: 'right' });
				drawStrip(issue.color, `**${name}** · ${text}`, () => A('span display:inline-flex flex-shrink:0', () => issue.icon({ size: '1em' })), issue.click);
			});
		}
	});
}
