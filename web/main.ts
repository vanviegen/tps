import A from 'aberdeen';
import * as S from 'staffa';
import { bot, eye, hammer, kanban, layoutGrid, listTodo, squareCheck } from 'staffa/icons.js';
import { drawBoard } from './board.ts';
import { $state } from './conn.ts';
import { drawHome } from './home.ts';
import { drawTask } from './task.ts';

S.setDarkMode(true);
A.setSpacingCssVars();

A.insertGlobalCss({
	'.tps-board': 'display:flex gap:$3 align-items:stretch overflow-x:auto flex:1 pb:$2',
	'.tps-col': 'flex: 1 0 250px; max-width:340px r:10px p:$2 display:flex flex-direction:column gap:$2 min-height:14rem',
	'.tps-col-head': 'display:flex align-items:center justify-content:space-between ph:$1 min-height:2rem',
	'.tps-col-head h4': 'm:0 font-size:0.8em text-transform:uppercase letter-spacing:0.07em fg:$s-muted',
	'.tps-col-cards': 'display:flex flex-direction:column gap:$2 flex:1',
	'.tps-card': 'r:8px p:$2 cursor:pointer border: 1px solid var(--s-faint); transition: box-shadow 0.15s;',
	'.tps-card:hover': 'box-shadow: 0 2px 10px rgba(0,0,0,0.3);',
	'.tps-card-title': 'font-weight:600 mb:$1 display:flex align-items:center gap:$1',
	'.tps-badges': 'display:flex flex-wrap:wrap gap:$1 align-items:center',
	'.tps-badge': 'font-size:0.72em bg:$s-faint r:4px pv:2px ph:6px fg:$s-muted white-space:nowrap',
	'.tps-badge.is-up': 'fg:$s-success font-weight:600',
	'.tps-badge.is-run': 'fg:$s-accent font-weight:600',
	'.tps-badge.is-err': 'fg:$s-danger font-weight:600',
	'.tps-badge.is-warn': 'fg:$s-warning font-weight:600',
	'.tps-warn': 'p:$2 r:6px border: 1px solid var(--s-warning);',

	// Task page: VS Code gets everything; a narrow agent column to its right.
	'.tps-task': 'display:flex gap:$2 align-items:stretch h: calc(100dvh - 1.5rem);',
	'.tps-main-col': 'flex:1 display:flex flex-direction:column min-width:0',
	'.tps-agent-col': 'flex: 0 0 26rem; max-width:40% min-width:0 display:flex flex-direction:column gap:$2 p:$2 r:10px',
	'.tps-agent-head': 'display:flex align-items:center gap:$2',
	'.tps-state': 'flex:1 min-width:0 display:flex align-items:center gap:$1 font-size:0.85em overflow:hidden white-space:nowrap',
	'.tps-state span': 'overflow:hidden text-overflow:ellipsis',
	'.tps-dot': 'display:inline-block w:0.6em h:0.6em r:50% flex-shrink:0',
	'.tps-live': 'display:inline-flex align-items:center fg:$s-success flex-shrink:0',
	'.tps-chip': 'display:inline-flex align-items:center gap:$1 font-size:0.85em bg:$s-faint r:4px pv:2px ph:6px',
	'.tps-chip-x': 'border:0 bg:transparent fg:$s-muted cursor:pointer p:0 font-size:1.1em line-height:1',
	'.tps-vscode': 'flex:1 w:100% min-height:0 border:0 r:8px bg:#1e1e1e',
	'.tps-term': 'position:relative flex:1 min-height:8rem bg:#16161e r:6px overflow:hidden',
	'.tps-term-inner': 'position:absolute; inset: 8px;',
	'.tps-offline': 'position:fixed bottom:1rem right:1rem bg:$s-danger fg:#fff pv:$2 ph:$3 r:6px z-index:9999',
});

const PHASE_ICONS: Record<string, S.Slot> = { plan: listTodo, implement: hammer, review: eye, done: squareCheck };

function navItems(): S.MenuEntry[] {
	const items: S.MenuEntry[] = [{ label: 'Projects', href: '/', icon: layoutGrid }];
	const projects = Object.entries($state.projects ?? {}) as [string, any][];
	projects.sort((a, b) => (a[1].name < b[1].name ? -1 : 1));
	for (const [pid, $p] of projects) {
		items.push({
			label: $p.name, icon: kanban, href: `/p/${pid}`,
			items: Object.keys($p.tasks ?? {}).sort().map((tid): S.MenuItem => ({
				label: $p.tasks[tid].title,
				icon: PHASE_ICONS[$p.tasks[tid].phase],
				href: `/p/${pid}/t/${tid}`,
			})),
		});
	}
	return items;
}

S.main({
	title: 'TPS',
	subtitle: 'task processing system',
	logo: () => bot({ size: '1.7em', color: 'var(--s-accent)' }),
	topbarAttrs: 'display:none', // every vertical pixel goes to the content (mainly: VS Code)
	nav: { get items() { return navItems(); } },
	routes: {
		'/': drawHome,
		'/p/[pid]': drawBoard,
		'/p/[pid]/t/[tid]': drawTask,
	},
	notFound: $panel => S.box({ header: 'Not found', content: $panel.path }),
});

A(() => {
	if (!$state.connected) A('div.tps-offline#⚠ Reconnecting to the TPS server…');
});

// Keep the computer awake while any agent is running.
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
			if ($t.agent && $t.agent !== 'idle') wantLock = true;
		}
	}
	void syncWakeLock();
});
// The lock is dropped by the browser when the tab hides; take it back on return.
document.addEventListener('visibilitychange', () => void syncWakeLock());
