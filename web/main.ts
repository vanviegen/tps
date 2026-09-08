import A from 'aberdeen';
import * as S from 'staffa';
import { bot, circleArrowUp, loaderCircle, settings, squareCode } from 'staffa/icons.js';
import { drawBoard } from './board.ts';
import { drawLogDetail } from './chat.ts';
import { $state } from './conn.ts';
import { addHostDialog, addProjectDialog, drawHome } from './home.ts';
import { drawTask } from './task.ts';
import { cmd, drawLiveLink, ELLIPSIS, tidOrder } from './util.ts';

S.setDarkMode(true);

/** Hosts, their projects, the tasks: the nav is the whole tree, with an "Add" row closing each list. */
function navItems(): S.MenuEntry[] {
	const items: S.MenuEntry[] = [
		() => A('div display:flex align-items:center gap:$2 p:$2 font-size:1.25em', () => {
			bot({ size: '1.7em', color: 'var(--s-accent)' });
			A('b#TPS');
		}),
	];
	const hosts = Object.entries($state.hosts ?? {}) as [string, any][];
	hosts.sort((a, b) => a[0] === 'local' ? -1 : b[0] === 'local' ? 1 : a[1].dest < b[1].dest ? -1 : 1);
	const projects = Object.entries($state.projects ?? {}) as [string, any][];
	projects.sort((a, b) => (a[1].name < b[1].name ? -1 : 1));
	const add = (label: string, click: () => void): S.MenuItem => ({ label, attrs: 'fg:$s-muted', click });
	for (const [hid, $h] of hosts) {
		items.push({
			label: () => drawHostLabel(hid, $h),
			items: [
				...projects.filter(([, $p]) => $p.host === hid).map(([pid, $p]) => projectItem(pid, $p)),
				add('Add project', () => addProjectDialog(hid)),
			],
		});
	}
	items.push(add('Add host', () => addHostDialog()));
	return items;
}

/** The host's name, a sign when it needs attention, and its actions in a context menu. */
function drawHostLabel(hid: string, $h: any): void {
	S.addContextMenu({ get items() { return hostMenu(hid, $h); } });
	A(`span ${ELLIPSIS} text=`, A.ref($h, 'dest'));
	A(() => {
		const down = $h.status !== 'connected';
		if (down || $h.warning) {
			const tip = down ? `${$h.status}${$h.error ? ' · ' + $h.error : ''}` : $h.warning;
			A(`span ml:$1 fg:$s-${down ? 'danger' : 'warning'} #⚠`, () => S.addTooltip({ tip }));
		} else if ($h.restarting) {
			A('span ml:$1 display:inline-flex fg:$s-muted', () => {
				S.addTooltip({ tip: 'The daemon restarts as soon as nothing is running' });
				loaderCircle({ size: '1em', attrs: 'animation: spin 1.2s linear infinite;' });
			});
		} else if ($h.updatable) {
			S.iconButton({ icon: circleArrowUp, ariaLabel: 'Update the daemon on this host to this build', attrs: '.small ml:$1',
				click: (e: Event) => { e.preventDefault(); e.stopPropagation(); void cmd('updateDaemon', { hid }); } });
		}
	});
}

function hostMenu(hid: string, $h: any): S.MenuEntry[] {
	const remote = hid !== 'local';
	const items: S.MenuEntry[] = [];
	if ($h.status !== 'connected') items.push({ label: 'Connect', click: () => void cmd('connectHost', { hid }) });
	else if ($h.updatable && !$h.restarting) items.push({ label: 'Update daemon to this build', icon: circleArrowUp, click: () => void cmd('updateDaemon', { hid }) });
	if (remote) items.push({ label: 'Copy claude login to this host', click: async () => {
		if (await cmd('copyCredentials', { hid })) S.toast({ message: `Your claude login is now on ${$h.dest}`, type: 'success' });
	}});
	items.push({ separator: true }, { label: 'Stop daemon', attrs: 'fg:$s-danger', click: async () => {
		if (await S.confirm(`Stop the TPS daemon on **${$h.dest}**? Its running workspaces are shut down; Connect starts it again.`)) void cmd('stopDaemon', { hid });
	}});
	if (remote) items.push({ label: 'Remove host', attrs: 'fg:$s-danger', click: async () => {
		if (await S.confirm(`Remove **${$h.dest}**? Its projects disappear from this TPS; nothing changes on the host itself.`)) void cmd('removeHost', { hid });
	}});
	return items;
}

function projectItem(pid: string, $p: any): S.MenuItem {
	return {
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
	};
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
