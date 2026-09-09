import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { bot, chevronDown, gitBranch, play, plus, settings } from 'staffa/icons.js';
import { drawBoard } from './board.ts';
import { $state } from './conn.ts';
import { drawAddProject, drawProjectCode, projectItem, projectSettingsDialog, sortedProjects } from './projects.ts';
import { runDialog } from './run.ts';
import { drawAgent, drawPlan, drawTaskCode, humanTasks, newTaskDialog, phaseItems, sortedTasks, taskItem, taskSettingsDialog, useTask } from './task.ts';
import { cmd, drawBadge, drawTaskIcon, ELLIPSIS, pathTo, selection } from './util.ts';

S.setDarkMode(true);
route.interceptLinks();

// Two columns, edge to edge: the left one names what you are looking at and
// holds the conversation about it, the right one is that thing.
A.insertGlobalCss({
	':root': '--leftw: min(33.3vw, 600px)',
	body: 'p:0 h:100dvh min-height:0 overflow:hidden',
});

A('div display:flex h:100dvh align-items:stretch', () => {
	A('div display:flex flex-direction:column gap:$3 w:var(--leftw) flex:none min-width:0 p:$3 overflow:hidden', drawSidebar);
	A('div flex:1 min-width:0 overflow:auto p:$3 border-left: 1px solid $s-faint;', drawRight);
});

// --- the left column ---

function drawSidebar(): void {
	A('div display:flex align-items:center gap:$2 font-size:1.25em', () => {
		bot({ size: '1.7em', color: 'var(--s-accent)' });
		A('b#TPS');
	});
	A(() => {
		const { pid, tid, base } = selection();
		const $p = pid ? $state.projects[pid] : undefined;
		drawProjectSection(pid, $p);
		if (pid && $p) drawTaskSection(pid, $p, tid, !!base);
	});
}

/** A labelled block of the left column. */
function drawSection(label: string, attrs: string, draw: () => void): void {
	A('div display:flex flex-direction:column gap:$1 min-width:0', attrs, () => {
		A('div font-size:0.7em font-weight:700 letter-spacing:0.12em text-transform:uppercase fg:$s-muted text=', label);
		draw();
	});
}

/**
 * A full-width dropdown naming the current thing, with a pill over its corner
 * counting the items it hides that are waiting for a human.
 */
function drawSelector(o: { label: string; current: () => void; items: () => S.MenuEntry[]; badge: () => number }): void {
	A('div position:relative flex:1 min-width:0', () => {
		S.menuButton({
			dropdownAttrs: 'overflow-y:auto min-width: min(var(--leftw), 90vw); max-height: min(60dvh, 30rem);',
			button: {
				icon: undefined, ariaLabel: o.label,
				attrs: '.neutral w:100% justify-content:space-between overflow:hidden',
				content: () => {
					A(`span display:flex align-items:center gap:$2 min-width:0 ${ELLIPSIS}`, o.current);
					chevronDown({ size: '1em', attrs: 'flex-shrink:0' });
				},
			},
			get items() { return o.items(); },
		});
		A(() => drawBadge(o.badge(), 'position:absolute top:-0.5em right:-0.35em pointer-events:none'));
	});
}

function drawProjectSection(pid: string | undefined, $p: any): void {
	drawSection('Project', '', () => {
		A('div display:flex align-items:center gap:$2', () => {
			drawSelector({
				label: 'Project',
				badge: () => sortedProjects().filter(([id]) => id !== pid && humanTasks(id)).length,
				current: () => {
					if ($p) A(`span ${ELLIPSIS} text=`, A.ref($p, 'name'));
					else A('span fg:$s-muted #Add project…');
				},
				items: () => [
					...sortedProjects().map(([id, $q]) => projectItem(id, $q)),
					{ separator: true },
					{ href: '/add', icon: plus, label: 'Add project', attrs: 'fg:$s-muted' },
				],
			});
			if ($p) S.iconButton({ icon: settings, ariaLabel: 'Project settings', click: () => projectSettingsDialog(pid!, $p) });
		});
	});
}

function drawTaskSection(pid: string, $p: any, tid: string | undefined, base: boolean): void {
	const $t = tid ? $p.tasks?.[tid] : undefined;
	drawSection('Task', '', () => {
		A('div display:flex align-items:center gap:$2', () => {
			drawSelector({
				label: 'Task',
				badge: () => humanTasks(pid, tid),
				current: () => {
					if ($t) A(`span ${ELLIPSIS}`, () => A('text=', $t.title || '(untitled)'));
					else if (base) drawBaseLabel($p);
					else A('span fg:$s-muted font-style:italic #Task…');
				},
				items: () => [
					{ href: pathTo(pid), label: 'Task…', attrs: 'fg:$s-muted font-style:italic' },
					{ href: pathTo(pid, 'base'), icon: gitBranch, label: () => drawBaseLabel($p) },
					{ separator: true },
					...sortedTasks(pid).map(([id, $q]) => taskItem(pid, id, $q)),
					{ separator: true },
					{ icon: plus, label: 'Add task', attrs: 'fg:$s-muted', click: () => newTaskDialog(pid) },
				],
			});
			if ($t) {
				S.iconButton({
					icon: () => drawTaskIcon(pid, $t), ariaLabel: 'Change phase',
					click: e => void S.showFloatingMenu({ items: phaseItems(pid, tid!, $t), anchor: e.currentTarget as HTMLElement }),
				});
				S.iconButton({ icon: settings, ariaLabel: 'Task settings', click: () => taskSettingsDialog(pid, tid!, $t) });
				A(() => { // its own scope: the CMD arriving must not redraw the section
					if ($t.phase === 'plan') return;
					S.iconButton({
						icon: play, disabled: !$t.runCmd, click: () => runDialog(pid, tid!, $t),
						ariaLabel: $t.runCmd ? 'Run the project' : 'Run: give Containerfile.dev a CMD line first',
					});
				});
			}
		});
	});
	// Its own scope: watching restarts the chat stream, so it must not be torn
	// down and set up again every time the task changes phase.
	A(() => {
		if (!$t) return;
		useTask(pid, tid!, $t);
	});
	A(() => {
		if (!$t) return;
		const plan = $t.phase === 'plan';
		drawSection(plan ? 'Description' : 'Agent', 'flex:1 min-height:0', () => {
			if (plan) drawPlan(pid, tid!, $t);
			else drawAgent(pid, tid!, $t);
		});
	});
}

/** The base worktree, by the name of the branch it is on. */
function drawBaseLabel($p: any): void {
	A('span', () => A('text=', `"${$p.defaultBranch ?? 'main'}" branch`));
}

// --- the right column ---

function drawRight(): void {
	A(() => {
		if (!$state.ready) { A('progress w:100%'); return; }
		const { pid, tid, base } = selection();
		if (!pid) return drawAddProject();
		const $p = $state.projects[pid];
		if (!$p) {
			S.box({ header: 'Unknown project', content: 'This project is not in the list (anymore).' });
			return;
		}
		if (base) return drawProjectCode(pid, $p);
		const $t = tid ? $p.tasks?.[tid] : undefined;
		// A task still in Plan has no workspace to show, so the board stays up.
		if ($t && $t.phase !== 'plan') return drawTaskCode(pid, tid!, $t);
		drawBoard(pid, $p);
	});
}

// '/' is the most relevant project; only with none listed does Add fill the column.
A(() => {
	if (!$state.ready || route.current.path !== '/') return;
	const first = A.peek(() => sortedProjects()[0]);
	if (first) route.current.path = pathTo(first[0]);
});

// --- the rest of the app ---

A(() => {
	if (!$state.connected) A.clean(S.toast({ message: 'Reconnecting to the TPS server…', type: 'danger', duration: 0, dismissible: false }));
});

// Questions from the server (SSH logins, host keys) show up as a dialog; the
// answer goes back as a command, and the question disappears once handled.
A(() => {
	const ids = Object.keys($state.ask ?? {});
	if (!ids.length) return;
	const id = ids[0];
	const ask = A.peek(() => ({ ...$state.ask[id] }));
	const $form = A.proxy({ value: '' });
	let answered = false;
	const answer = (args: object) => {
		if (answered) return;
		answered = true;
		void cmd('answer', { id: Number(id), ...args });
	};
	void S.dialog({ header: ask.title, content: () => {
		S.form({
			submit: () => answer({ value: $form.value }),
			content: () => {
				A('p white-space:pre-wrap text=', ask.text);
				if (ask.kind !== 'confirm') S.textline({ type: ask.kind === 'password' ? 'password' : 'text', bind: A.ref($form, 'value') });
			},
			actions: () => S.button({ content: ask.kind === 'confirm' ? 'Connect' : 'OK', type: 'submit' }),
		});
	}}).then(() => answer({ cancel: true })); // closed without answering
});

// Keep the computer awake while any agent is working.
let wantLock = false;
let wakeLock: any;
async function syncWakeLock(): Promise<void> {
	if (wantLock && !wakeLock && !document.hidden) {
		try {
			wakeLock = await (navigator as any).wakeLock?.request('screen');
			wakeLock?.addEventListener('release', () => { wakeLock = undefined; });
		} catch {}
	} else if (!wantLock && wakeLock) {
		const lock = wakeLock;
		wakeLock = undefined;
		void lock.release();
	}
}
A(() => {
	wantLock = false;
	for (const $p of Object.values($state.projects ?? {}) as any[]) {
		for (const $t of Object.values($p.tasks ?? {}) as any[]) {
			if ($t.working) wantLock = true;
		}
	}
	void syncWakeLock();
});
// The lock is dropped by the browser when the tab hides; take it back on return.
document.addEventListener('visibilitychange', () => void syncWakeLock());
