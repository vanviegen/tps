import A from 'aberdeen';
import * as S from 'staffa';
import { bot, plus, settings, squareCode } from 'staffa/icons.js';
import { drawBoard } from './board.ts';
import { drawLogDetail } from './chat.ts';
import { $state } from './conn.ts';
import { addProjectDialog, drawHome } from './home.ts';
import { drawTask } from './task.ts';
import { drawLiveLink, ELLIPSIS, tidOrder } from './util.ts';

S.setDarkMode(true);

function navItems(): S.MenuEntry[] {
	const items: S.MenuEntry[] = [
		() => A('div display:flex align-items:center gap:$2 p:$2 font-size:1.25em', () => {
			bot({ size: '1.7em', color: 'var(--s-accent)' });
			A('b#TPS');
		}),
	];
	const projects = Object.entries($state.projects ?? {}) as [string, any][];
	projects.sort((a, b) => (a[1].name < b[1].name ? -1 : 1));
	for (const [pid, $p] of projects) {
		items.push({
			href: `/p/${pid}`,
			label: () => {
				A(`span ${ELLIPSIS} text=`, A.ref($p, 'name'));
				A(() => {
					const waiting = (Object.values($p.tasks ?? {}) as any[]).filter($t => $t.phase === 'human').length;
					if (waiting) A('span.s-s ml:auto font-size:0.75em ph:0.5em text=', String(waiting), () => {
						S.addTooltip({ tip: 'Tasks in the Human column' });
					});
				});
			},
			items: Object.keys($p.tasks ?? {}).sort((a, b) => tidOrder(a) < tidOrder(b) ? -1 : 1)
				.map(tid => taskItem(pid, tid, $p.tasks[tid])),
		});
	}
	items.push({ separator: true }, { label: 'Add a project…', icon: plus, click: () => addProjectDialog() });
	return items;
}

/**
 * A task, with its three pages below it once it is assigned. The pages are
 * siblings in the URL too, so that staffa shows one of them at a time rather
 * than stacking them side by side.
 */
function taskItem(pid: string, tid: string, $t: any): S.MenuItem {
	const base = `/p/${pid}/t/${tid}`;
	if ($t.phase === 'plan') {
		return { href: base + '/settings', label: () => A(`span ${ELLIPSIS} text=`, $t.title || 'New task') };
	}
	const page = (label: string, icon: S.Slot, path: string): S.MenuItem => ({ label, icon, href: base + path });
	return {
		label: () => {
			A(`span ${ELLIPSIS} text=`, A.ref($t, 'title'));
			drawLiveLink($t);
		},
		items: [
			{ ...page('Agent', bot, '/agent'), match: base + '/agent' }, // also claims the log details
			page('Code', squareCode, '/code'),
			page('Settings', settings, '/settings'),
		],
	};
}

S.main({
	title: 'TPS',
	topbarAttrs: 'display:none', // every vertical pixel goes to the content; the nav says where you are
	navWidth: 300,
	nav: { get items() { return navItems(); } },
	routes: {
		'/': drawHome,
		'/p/[pid]': drawBoard,
		'/p/[pid]/t/[tid]/agent': $panel => drawTask($panel, 'agent'),
		'/p/[pid]/t/[tid]/code': $panel => drawTask($panel, 'code'),
		'/p/[pid]/t/[tid]/settings': $panel => drawTask($panel, 'settings'),
		'/p/[pid]/t/[tid]/agent/log/[i=integer]': drawLogDetail,
	},
	notFound: $panel => S.box({ header: 'Not found', content: $panel.path }),
});

A(() => {
	if (!$state.connected) A.clean(S.toast({ message: 'Reconnecting to the TPS server…', type: 'danger', duration: 0, dismissible: false }));
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
