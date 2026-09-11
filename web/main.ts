import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { plus } from 'staffa/icons.js';
import { showAsk } from './ask.ts';
import { $state } from './conn.ts';
import './holds.ts';
import { watchPhases } from './notify.ts';
import { bindPalette } from './palette.ts';
import { addProjectDialog, drawProjectCode, drawProjectPage, sortedProjects } from './projects.ts';
import { $ui, drawSidebar, SIDEBAR_CLOSED, SIDEBAR_OPEN } from './sidebar.ts';
import { drawSplit, TASK_CODE_LEFT, WIDE_CODE_LEFT } from './split.ts';
import { draftFor, drawAgent, drawDonePanel, drawPlanEditor, drawPlanSettings, drawTaskCode, useTask } from './task.ts';
import { hasWorkspace, isFinished, isOpenable, pathTo, selection, taskTitle } from './util.ts';

S.setDarkMode(true);
route.interceptLinks();
bindPalette(); // the keyboard's way around: go to any project or task by name
watchPhases(); // the tasks that asked to announce themselves (see notify.ts)

/**
 * KEYS — the shortcuts for the handful of things done often enough to be worth
 * one. Each is a `key` on the button it presses, so the button's tooltip says
 * it, `?` lists it, and it works exactly while that button is on screen:
 *
 * - `ctrl-shift-S` start a task, `ctrl-shift-F` the project's own files in VS
 *   Code — both on the project page, beside the board.
 * - `ctrl-shift-G` merge the task on screen, while it offers to be merged.
 * - `ctrl-shift-X` close it: VS Code stops and the task is put away.
 * - `ctrl-shift-L` fold the sidebar away, and back out.
 * - `ctrl-L` the palette (see palette.ts), `ctrl-enter` send a message or
 *   assign a plan to the agent.
 *
 * The letters are what the browsers leave: ctrl-shift-N/T/W/Q never arrive at
 * all, A, B, D, O, P and R are bookmarks and windows, C/I/J/K/M open developer
 * tools, and E and U are the desktop's unicode and emoji input. What is left
 * has to stand for the action rather than spell it — S for start, F for files,
 * G for merGe, X for closing, L for the left-hand column.
 *
 * A task in Agent or Human is mostly VS Code, which owns the keyboard while it
 * has focus and never passes a keystroke out: shortcuts are for the pages
 * around it. Taking one back is possible but costs VS Code the key everywhere
 * (see claimKeyInCode in code.ts), which only the palette is worth.
 */

// The sidebar (see sidebar.ts) and, beside it, whatever the address names.
// Most of those are the two columns of split.ts. The sidebar's width is a
// variable, as the frames that VS Code lives in are positioned from it (see
// code.ts), just like the split is.
A.insertGlobalCss({
	body: 'p:0 h:100dvh min-height:0 overflow:hidden',
});
A(() => { A.cssVars.sidew = $ui.collapsed ? SIDEBAR_CLOSED : SIDEBAR_OPEN; });

A('div display:flex h:100dvh align-items:stretch', () => {
	A('div flex:none w:var(--sidew) min-width:0 transition: width 0.15s;', drawSidebar);
	A('div flex:1 min-width:0 display:flex align-items:stretch', drawMain);
});

// VS Code coming on screen folds the sidebar up: the code wants the room, and
// the strip that is left still says where you are. It stays wherever it was
// put otherwise, so the icon is the way back out.
A(() => {
	const { pid, tid, base } = selection();
	const $t = pid && tid ? $state.projects[pid]?.tasks?.[tid] : undefined;
	if (base || ($t && isOpenable($t))) $ui.collapsed = true;
});

// The tab's title says where you are, now that nothing on the page does.
A(() => {
	const { pid, tid, base, draft } = selection();
	const $p = pid ? $state.projects[pid] : undefined;
	const $t = tid ? $p?.tasks?.[tid] : undefined;
	const what = !$p ? '' : $t ? taskTitle($t) : base ? `${$p.defaultBranch ?? 'main'} branch` : draft ? 'New task' : '';
	document.title = [what, $p?.name, 'TPS'].filter(Boolean).join(' · ');
});

function drawMain(): void {
	A(() => {
		if (!$state.ready) { A('progress w:100% m:$3'); return; }
		const { pid, tid, base, draft } = selection();
		if (!pid) return drawHome();
		const $p = $state.projects[pid];
		if (!$p) return drawWide(() => S.box({ header: 'Unknown project', content: 'This project is not in the list (anymore).' }));
		if (base) return drawProjectCode(pid, $p, WIDE_CODE_LEFT);
		if (draft) {
			const $d = draftFor(pid);
			return drawSplit(() => drawPlanSettings(pid, undefined, $d), () => drawPlanEditor(pid, undefined, $d));
		}
		if (!tid) return drawWide(() => drawProjectPage(pid, $p));
		const $t = $p.tasks?.[tid];
		if (!$t) return drawWide(() => S.box({ header: 'Unknown task', content: 'This task is not in the project (anymore).' }));
		drawTaskView(pid, tid, $t);
	});
}

/** One column, the width of the window minus the sidebar. */
function drawWide(content: () => void): void {
	A('div flex:1 min-width:0 overflow:auto p:$3', content);
}

/**
 * A task. Its settings and its description while it is in Plan; its chat
 * beside VS Code once it has a workspace; its chat beside a note of the merge
 * once it is Done, the workspace being gone then.
 */
function drawTaskView(pid: string, tid: string, $t: any): void {
	// Its own scope: watching restarts the chat stream, so it must not be torn
	// down and set up again every time the task changes phase.
	A(() => useTask(pid, tid, $t));
	drawSplit(() => {
		A(() => {
			if ($t.phase === 'plan') return drawPlanSettings(pid, tid, $t);
			drawAgent(pid, tid, $t);
		});
	}, () => {
		A(() => {
			if ($t.phase === 'plan') return drawPlanEditor(pid, tid, $t);
			// Done and merged, there is nothing left to open; done without
			// merging, the worktree is still there and still worth showing.
			if (isFinished($t) && !hasWorkspace($t)) return drawDonePanel(pid, tid, $t);
			drawTaskCode(pid, tid, $t, TASK_CODE_LEFT);
		});
	});
}

/**
 * There is no front page: the address bar's "/" lands on the first project
 * in the sidebar. Only a board without projects has nothing to land on, and
 * says how to get one.
 */
function drawHome(): void {
	const first = sortedProjects()[0];
	if (first) {
		setTimeout(() => { if (!A.peek(selection).pid) void route.go(pathTo(first[0]), 'replace'); });
		return;
	}
	drawWide(() => {
		S.box({ header: 'Welcome to TPS', contentAttrs: 'display:flex flex-direction:column align-items:flex-start gap:$2', content: () => {
			A('p rich=', 'No projects yet. *Add project* takes a directory holding a git repository, on this machine or on any host you reach over SSH.');
			S.button({ content: 'Add project', icon: plus, click: () => addProjectDialog() });
		}});
	});
}

// --- the rest of the app ---

A(() => {
	if (!$state.connected) A.clean(S.toast({ message: 'Reconnecting to the TPS server…', type: 'danger', duration: 0, dismissible: false }));
});

// Questions from the server (SSH logins, host keys) wait in the sidebar's
// strip for the host they are about, so they don't interrupt. One about a
// host that is not on the board has nowhere to wait, and opens by itself.
A(() => {
	for (const [id, $a] of Object.entries($state.ask ?? {}) as [string, any][]) {
		if (!$a.host || !$state.hosts?.[$a.host]) showAsk(id);
	}
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
