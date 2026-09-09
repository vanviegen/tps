import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { bot, chevronRight, gitBranch, play, settings } from 'staffa/icons.js';
import { drawBoard } from './board.ts';
import { $state } from './conn.ts';
import { bindPalette } from './palette.ts';
import { drawNotices, drawProjectCode, drawProjectList, projectSettingsDialog, sortedProjects } from './projects.ts';
import { runDialog } from './run.ts';
import { draftFor, drawAgent, drawPlanEditor, drawPlanSettings, drawTaskCode, humanTasks, phaseItems, taskSettingsDialog, useTask } from './task.ts';
import { cmd, drawBadge, drawTaskIcon, ELLIPSIS, pathTo, selection, taskTitle } from './util.ts';

S.setDarkMode(true);
route.interceptLinks();
bindPalette(); // the keyboard's way around: go to any project or task by name

// Two columns, edge to edge: the left one says where you are and holds what to
// do about it (the conversation included), the right one is the thing itself.
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
	drawCrumbs();
	// This scope deliberately doesn't read the task's phase: a task changing
	// phase must not tear down its chat stream (see useTask below).
	A(() => {
		const { pid, tid, draft } = selection();
		const $p = pid ? $state.projects[pid] : undefined;
		if (!pid || !$p) return; // at the root, the project list in the right column is all there is
		if (draft) return drawPlanSettings(pid, undefined, draftFor(pid));
		const $t = tid ? $p.tasks?.[tid] : undefined;
		if ($t) drawTaskPanel(pid, tid!, $t);
		else drawProjectPanel(pid, $p);
	});
}

/**
 * The trail to what is on screen: TPS / project / task, each but the last a
 * link one level up. The pills count what waits for a human *elsewhere*: in
 * the other projects, and in the project's other tasks.
 */
function drawCrumbs(): void {
	A('nav display:flex align-items:center gap:$1 min-width:0', () => {
		const { pid, tid, base, draft } = selection();
		const $p = pid ? $state.projects[pid] : undefined;
		// No pill at the root: what waits for a human is on screen there.
		drawCrumb(pid ? '/' : undefined, () => {
			bot({ size: '1.3em', color: 'var(--s-accent)' });
			A('b#TPS');
		}, pid ? () => sortedProjects().filter(([id]) => id !== pid && humanTasks(id)).length : undefined);
		if (!$p) return;
		drawSeparator();
		drawCrumb(tid || base || draft ? pathTo(pid!) : undefined,
			() => A(`span ${ELLIPSIS} text=`, A.ref($p, 'name')),
			() => humanTasks(pid!, tid));
		if (base) {
			drawSeparator();
			drawCrumb(undefined, () => drawBaseLabel($p));
		} else if (draft) {
			drawSeparator();
			drawCrumb(undefined, () => A(`span ${ELLIPSIS}`, () => A('text=', taskTitle(draftFor(pid!), 'New task'))));
		} else if (tid) {
			const $t = $p.tasks?.[tid];
			drawSeparator();
			drawCrumb(undefined, () => A(`span ${ELLIPSIS}`, () => A('text=', taskTitle($t))));
		}
	});
}

/** One crumb: a link to what it names, unless it is the page you are on. */
function drawCrumb(href: string | undefined, content: () => void, badge?: () => number): void {
	const attrs = `display:flex align-items:center gap:$1 min-width:0 text-decoration:none ${ELLIPSIS}`;
	const draw = () => {
		content();
		if (badge) A(() => drawBadge(badge()));
	};
	if (href) A('a', attrs, 'href=', href, draw);
	else A('span', attrs, 'font-weight:700', draw);
}

function drawSeparator(): void {
	A('span display:flex flex-shrink:0 fg:$s-muted', () => chevronRight({ size: '1em' }));
}

/** The base worktree, by the name of the branch it is on. */
function drawBaseLabel($p: any): void {
	A('span', () => A('text=', `"${$p.defaultBranch ?? 'main'}" branch`));
}

/** Under the crumbs of a project: its checkout, its settings, and what stands in its way. */
function drawProjectPanel(pid: string, $p: any): void {
	A('div display:flex align-items:center gap:$2', () => {
		S.button({
			content: () => drawBaseLabel($p), icon: gitBranch, attrs: '.small .neutral',
			click: () => void route.go(pathTo(pid, 'base')),
		});
		S.iconButton({ icon: settings, ariaLabel: 'Project settings', attrs: 'ml:auto', click: () => projectSettingsDialog(pid, $p) });
	});
	drawNotices(pid, $p);
}

/** Under the crumbs of a task: its settings while in Plan, else its controls and the chat. */
function drawTaskPanel(pid: string, tid: string, $t: any): void {
	// Its own scope: watching restarts the chat stream, so it must not be torn
	// down and set up again every time the task changes phase.
	A(() => useTask(pid, tid, $t));
	A(() => {
		if ($t.phase === 'plan') return drawPlanSettings(pid, tid, $t);
		drawTaskControls(pid, tid, $t);
		drawAgent(pid, tid, $t);
	});
}

/** The phase, the settings and the run, as a row of icons; the description editor has no need of them. */
function drawTaskControls(pid: string, tid: string, $t: any): void {
	A('div display:flex align-items:center gap:$2', () => {
		S.iconButton({
			icon: () => drawTaskIcon(pid, $t), ariaLabel: 'Change phase',
			click: e => void S.showFloatingMenu({ items: phaseItems(pid, tid, $t), anchor: e.currentTarget as HTMLElement }),
		});
		S.iconButton({ icon: settings, ariaLabel: 'Task settings', attrs: 'ml:auto', click: () => taskSettingsDialog(pid, tid, $t) });
		A(() => { // its own scope: the CMD arriving must not redraw the row
			S.iconButton({
				icon: play, disabled: !$t.runCmd, click: () => runDialog(pid, tid, $t),
				ariaLabel: $t.runCmd ? 'Run the project' : 'Run: give Containerfile.dev a CMD line first',
			});
		});
	});
}

// --- the right column ---

function drawRight(): void {
	A(() => {
		if (!$state.ready) { A('progress w:100%'); return; }
		const { pid, tid, base, draft } = selection();
		if (!pid) return drawProjectList();
		const $p = $state.projects[pid];
		if (!$p) {
			S.box({ header: 'Unknown project', content: 'This project is not in the list (anymore).' });
			return;
		}
		if (base) return drawProjectCode(pid, $p);
		// A task still in Plan (a draft is nothing else) has no workspace to
		// show, so its description takes the column.
		if (draft) return drawPlanEditor(pid, undefined, draftFor(pid));
		const $t = tid ? $p.tasks?.[tid] : undefined;
		if (!$t) return drawBoard(pid, $p);
		if ($t.phase === 'plan') return drawPlanEditor(pid, tid!, $t);
		drawTaskCode(pid, tid!, $t);
	});
}

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
