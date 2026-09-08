import A from 'aberdeen';
import * as S from 'staffa';
import { bot, chevronLeft, pencil, plus, x } from 'staffa/icons.js';
import { drawBoard, drawProjectCode } from './board.ts';
import { drawLogDetail } from './chat.ts';
import { $state } from './conn.ts';
import { addProjectDialog, drawHome, removeProject, renameProject, sortedProjects } from './projects.ts';
import { drawAgentPanel, drawCodePanel, drawTask } from './task.ts';
import { cmd, ELLIPSIS } from './util.ts';

S.setDarkMode(true);

/** The shell, once `S.main()` below has handed it over; the nav's "up" reads the stack from it. */
const $shell = A.proxy<{ stack?: S.PanelStack }>({});

/** The nav is the project list, most recently active first, with an "Add" row closing it. */
function navItems(): S.MenuEntry[] {
	const items: S.MenuEntry[] = [
		() => A('div display:flex align-items:center gap:$2 p:$2 font-size:1.25em', () => {
			bot({ size: '1.7em', color: 'var(--s-accent)' });
			A('b#TPS');
			// The top bar (with its breadcrumbs) is hidden, so this is the way back
			// up the stack. Nothing to go up to on a project page or the welcome.
			A(() => {
				const stack = $shell.stack;
				if (!stack?.currentPanelIndex) return;
				S.iconButton({ icon: chevronLeft, ariaLabel: 'Back to the previous page', attrs: '.small .neutral ml:auto',
					click: () => void stack.closePanel() });
			});
		}),
	];
	for (const [pid, $p] of sortedProjects()) items.push(projectItem(pid, $p));
	items.push({ label: 'Add project', icon: plus, attrs: 'fg:$s-muted', click: () => addProjectDialog() });
	return items;
}

/** A project: its name, dimly its host, a sign when something is wrong, and how many tasks wait for a human. */
function projectItem(pid: string, $p: any): S.MenuItem {
	const href = `/p/${pid}`;
	return {
		href, match: href, // also claims the task pages under it
		label: () => {
			S.addContextMenu({ link: href, items: [
				{ label: 'Rename…', icon: pencil, click: () => void renameProject(pid, $p) },
				{ label: 'Remove from list', icon: x, attrs: 'fg:$s-danger', click: () => void removeProject(pid, $p) },
			]});
			A(`span ${ELLIPSIS} text=`, A.ref($p, 'name'));
			A(() => {
				const $h = $state.hosts?.[$p.host];
				if ($p.host !== 'local' && $h?.name) A(`small ${ELLIPSIS} fg:$s-muted text=`, $h.name);
				const problem = $p.error || ($h && $h.status !== 'connected' ? `${$h.name}: ${$h.status}${$h.error ? ' · ' + $h.error : ''}` : '');
				if (problem) A('span fg:$s-danger #⚠', () => S.addTooltip({ tip: problem }));
			});
			A(() => {
				const waiting = (Object.values($p.tasks ?? {}) as any[]).filter($t => $t.phase === 'human').length;
				if (waiting) A('span.s-s ml:auto font-size:0.75em ph:0.5em text=', String(waiting), () => {
					S.addTooltip({ tip: 'Tasks in the Human column' });
				});
			});
		},
	};
}

$shell.stack = S.main({
	title: 'TPS',
	topbarAttrs: 'display:none', // every vertical pixel goes to the content; the nav says where you are
	navWidth: 300,
	nav: { get items() { return navItems(); } },
	routes: {
		'/': drawHome,
		'/p/[pid]': drawBoard,
		'/p/[pid]/code': drawProjectCode,
		'/p/[pid]/t/[tid]': drawTask,
		'/p/[pid]/t/[tid]/agent': drawAgentPanel,
		'/p/[pid]/t/[tid]/log/[i=integer]': drawLogDetail,
		'/p/[pid]/t/[tid]/code': drawCodePanel,
		'/p/[pid]/t/[tid]/code/[...file]': drawCodePanel,
	},
	ancestors: {
		// Not the parent-path walk: it would open a Code panel per path segment of the file.
		'/p/[pid]/t/[tid]/code/[...file]': ({ pid, tid }) => [`/p/${pid}`, `/p/${pid}/t/${tid}`],
	},
	notFound: $panel => S.box({ header: 'Not found', content: $panel.path }),
});

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
