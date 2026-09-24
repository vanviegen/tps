import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { plus } from 'staffa/icons.js';
import { $state } from './conn.ts';
import { watchPhases } from './notify.ts';
import { bindPalette } from './palette.ts';
import { addProjectDialog, drawProjectCode, drawProjectPage, sortedProjects } from './projects.ts';
import { $ui, drawSidebar, SIDEBAR_CLOSED, SIDEBAR_OPEN } from './sidebar.ts';
import { drawSplit, TASK_CODE_LEFT, WIDE_CODE_LEFT } from './split.ts';
import { drawAgent, drawDonePanel, drawPlanEditor, drawPlanSettings, drawTaskCode, useTask } from './task.ts';
import { hasWorkspace, isFinished, pathTo, selection, taskTitle } from './util.ts';

S.setDarkMode(true);
route.interceptLinks();
bindPalette(); // the keyboard's way around: go to any project or task by name
bindLastPage(); // and the way back to the page you came from
watchPhases(); // the tasks that asked to announce themselves (see notify.ts)

/**
 * KEYS — the shortcuts for the handful of things done often enough to be worth
 * one. Each is a `key` on the button it presses, so the button's tooltip says
 * it, `?` lists it, and it works exactly while that button is on screen:
 *
 * - `ctrl-shift-S` start a task, `ctrl-shift-F` the project's own files in VS
 *   Code — both on the project page, beside the board.
 * - `ctrl-shift-G` merge the task on screen, while it offers to be merged.
 * - `ctrl-B` fold the sidebar away, and back out: Code's own key for its own
 *   sidebar, so the gesture is the same one wherever the focus is — and it is
 *   left to Code while the focus is in there, folding that sidebar instead.
 * - `ctrl-P` and `ctrl-shift-P` are VS Code's own palettes, handed to it from
 *   wherever the focus is while a workspace is on screen (see code.ts), rather
 *   than printing the page.
 * - `ctrl-L` the palette (see palette.ts), whose top entry on an empty field
 *   is the board of the project you are in — so `ctrl-L enter` is the way out
 *   of VS Code, and of the project's own checkout without the sidebar.
 *   `ctrl-enter` sends a message or assigns a plan to the agent.
 * - `ctrl-tab` the page before this one, and again to come back — the two you
 *   are working between, a keystroke apart. Only where the browser lets go of
 *   it: Chrome and Safari keep it for their own tabs, Firefox and the
 *   installed app hand it over.
 *
 * The letters are what the browsers leave: ctrl-shift-N/T/W/Q never arrive at
 * all, A, B, D, O, P and R are bookmarks and windows, C/I/J/K/M open developer
 * tools, and E and U are the desktop's unicode and emoji input. What is left
 * has to stand for the action rather than spell it — S for start, F for files,
 * G for merGe, X for closing. Extensions take some as well, wherever the user
 * put them: the password managers' ctrl-shift-L is the one to keep clear of.
 *
 * A task in Agent or Human is mostly VS Code, which owns the keyboard while it
 * has focus and never passes a keystroke out: shortcuts are for the pages
 * around it. Taking one back is possible but costs VS Code the key everywhere
 * (see claimKeyInCode in code.ts), which only the palette is worth.
 */

/**
 * `ctrl-tab`, between the last two pages: the address of the page before this
 * one is kept as the route changes, and going back to it makes this one the
 * page before — so the same key returns, and the pair is a toggle.
 */
function bindLastPage(): void {
	let previous = '';
	let here = '';
	A(() => {
		previous = here;
		here = route.current.path;
	});
	S.bindKey('mod+tab', 'Back to the previous page', () => { if (previous) void route.go(previous); });
}

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

// The tab's title says where you are.
A(() => {
	const { pid, tid, base } = selection();
	const $p = pid ? $state.projects[pid] : undefined;
	const $t = tid ? $p?.tasks?.[tid] : undefined;
	const what = !$p ? '' : $t ? taskTitle($t) : base ? 'Project directory' : '';
	document.title = [what, $p?.name, 'TPS'].filter(Boolean).join(' · ');
});

function drawMain(): void {
	A(() => {
		if (!$state.ready) { A('progress w:100% m:$3'); return; }
		const { pid, tid, base } = selection();
		if (!pid) return drawHome();
		const $p = $state.projects[pid];
		if (!$p) return drawWide(() => S.box({ header: 'Unknown project', content: 'This project is not in the list (anymore).' }));
		if (base) return drawProjectCode(pid, $p, WIDE_CODE_LEFT);
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
			// Finished, there is nothing left to open — once the workspace is
			// gone, which takes a moment; until then it is still worth showing.
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

