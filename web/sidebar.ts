import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { bot, gitBranch, panelLeftClose, panelLeftOpen, plus, server, settings, x } from 'staffa/icons.js';
import { $state } from './conn.ts';
import { $holds, holdKey, isHeld, release } from './holds.ts';
import { hostIssue, manageHostsDialog } from './hosts.ts';
import { addProjectDialog, drawProjectChip, projectMenuItems, projectSortKey, reorderProjects } from './projects.ts';
import { closeTask, taskMenuItems, taskSettingsDialog } from './task.ts';
import { drawStrip, drawTaskIcon, hostName, pathTo, phaseOrder, projectColor, selection, taskTitle } from './util.ts';

/**
 * The sidebar: the way around the dashboard, and the list of what is open.
 *
 * Under the logo, every project in the order the user put them in, wearing
 * its colour; under each, the tasks that are *open*: those waiting for a
 * human (in warning colour), those this dashboard holds VS Code on (see
 * holds.ts), and the one on screen. Then Add project and Manage hosts, and
 * at the bottom only the hosts that want something — a login, an update, a
 * connection — as the ones that work have nothing to say.
 *
 * Collapsed, it is a narrow strip: a coloured chip with the project's code
 * where its name was, and the first letters of each task's title, which the
 * `subject: change` form of titles makes readable. It collapses by itself
 * when VS Code comes on screen (see main.ts), and by the icon beside the logo.
 */
export const $ui = A.proxy({ collapsed: false });

export const SIDEBAR_OPEN = '16rem';
export const SIDEBAR_CLOSED = '3rem';

A.insertGlobalCss({
	'.tps-side': 'display:flex flex-direction:column h:100% min-width:0 overflow:hidden border-right: 1px solid $s-faint;',
	'.tps-side .tps-group': 'border-left: 3px solid var(--tps-color); margin: 0 $1 $1 0;',
	'.tps-side .tps-row': 'display:flex align-items:center gap:$2 min-width:0 text-decoration:none fg:$s-text r: 0 $s-radius-sm $s-radius-sm 0; transition: background 0.12s;',
	'.tps-side .tps-row:hover': 'background: color-mix(in oklab, $s-text, transparent 92%);',
	'.tps-side .tps-row.tps-current': 'background: color-mix(in oklab, $s-text, transparent 85%);',
	'.tps-side .tps-project': 'ph:$1 pv:0.3em font-weight:600',
	'.tps-side .tps-task': 'pl:1.6em pr:$1 pv:0.15em font-size:0.9em fg:$s-muted',
	'.tps-side .tps-task.tps-human': 'fg:$s-warning',
	'.tps-side .tps-task.tps-current': 'fg:$s-text',
	'.tps-side .tps-clip': 'flex:1 min-width:0 white-space:nowrap overflow:hidden text-overflow:ellipsis',
	'.tps-side.tps-collapsed .tps-project': 'justify-content:center ph:0',
	'.tps-side.tps-collapsed .tps-task': 'pl:0.4em pr:0 font-size:0.8em',
	'.tps-side.tps-collapsed .tps-clip': 'text-overflow:clip',
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

/** The logo and the way to fold the sidebar up; collapsed, only the way out. */
function drawHeader(collapsed: boolean): void {
	if (collapsed) {
		A('div display:flex justify-content:center pv:$2', () => {
			S.iconButton({ icon: panelLeftOpen, ariaLabel: 'Expand the sidebar', attrs: '.small', click: () => { $ui.collapsed = false; } });
		});
	} else {
		A('div display:flex align-items:center gap:$2 pl:$2 pr:$1 pv:$2', () => {
			bot({ size: '1.4em', color: 'var(--s-accent)' });
			A('b flex:1 #TPS');
			S.iconButton({ icon: panelLeftClose, ariaLabel: 'Collapse the sidebar', attrs: '.small', click: () => { $ui.collapsed = true; } });
		});
	}
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
				const { pid: shown, tid, base, draft } = selection();
				A('.tps-current=', shown === pid && !tid && !base && !draft);
			});
			A(() => {
				if ($ui.collapsed) S.addTooltip({ tip: () => A('text=', $p.name), placement: 'right' });
			});
			drawProjectChip($p);
			A(() => {
				if ($ui.collapsed) return;
				A('span.tps-clip text=', A.ref($p, 'name'));
				A(() => {
					if ($p.error) A('span flex-shrink:0 fg:$s-danger #⚠', () => S.addTooltip({ tip: $p.error }));
				});
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

/** A task is listed while it waits for a human, while this dashboard holds it, and while it is on screen. */
function isOpen(pid: string, tid: string, $t: any): boolean {
	if ($t.phase === 'human' || $holds[holdKey(pid, tid)]) return true;
	const { pid: shown, tid: shownTid } = selection();
	return shown === pid && shownTid === tid;
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
			if ($ui.collapsed) S.addTooltip({ tip: () => A('text=', taskTitle($t)), placement: 'right' });
			else drawTaskIcon(pid, $t);
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
			const label = `${$p.defaultBranch ?? 'main'} branch`;
			if ($ui.collapsed) S.addTooltip({ tip: label, placement: 'right' });
			else A('span display:inline-flex flex-shrink:0', () => gitBranch({ size: '1.1em' }));
			A('span.tps-clip text=', label);
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
		A('div display:flex flex-direction:column align-items:center gap:$1 pv:$1', () => {
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
