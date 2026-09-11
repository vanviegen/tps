import A from 'aberdeen';
import { $state, addWatch, dropWatch } from './conn.ts';

/**
 * What this dashboard holds open: tasks whose VS Code it keeps running, and
 * projects whose own checkout it has in VS Code. A hold is a watch at the
 * server (see conn.ts), which the daemon answers by keeping the workspace up
 * and code-server running in it; letting go — Close, or this tab going away,
 * connection and all — is what stops VS Code. Nothing else does.
 *
 * The set survives a reload through localStorage, so a browser that comes
 * back finds its sessions where it left them (the watches are placed again
 * as the connection comes up); one that never comes back has dropped them
 * along with its connection, and the daemon has acted on that already.
 */
const HOLDS_KEY = 'tps.holds';

export const $holds: Record<string, true> = A.proxy({} as Record<string, true>);

/** The key a hold goes by: the task's, or `pid/-` for the project's own checkout. */
export function holdKey(pid: string, tid?: string): string {
	return `${pid}/${tid ?? '-'}`;
}

/** Whether this dashboard holds the task (or, without a tid, the project's checkout) open. Reactive. */
export function isHeld(pid: string, tid?: string): boolean {
	return !!$holds[holdKey(pid, tid)];
}

export function hold(pid: string, tid?: string): void {
	const key = holdKey(pid, tid);
	if (A.peek(() => $holds[key])) return;
	$holds[key] = true;
	addWatch(key);
	persist();
}

export function release(pid: string, tid?: string): void {
	const key = holdKey(pid, tid);
	if (!A.peek(() => $holds[key])) return;
	delete $holds[key];
	dropWatch(key);
	persist();
}

function persist(): void {
	try {
		localStorage.setItem(HOLDS_KEY, JSON.stringify(A.peek(() => Object.keys($holds))));
	} catch {} // storage off or full: the daemon still has the watches for as long as this tab lives
}

// What was held before the reload.
try {
	const saved = JSON.parse(localStorage.getItem(HOLDS_KEY) || '[]');
	if (Array.isArray(saved)) {
		for (const key of saved) {
			if (typeof key === 'string' && key.includes('/')) {
				$holds[key] = true;
				addWatch(key);
			}
		}
	}
} catch {}

// A hold on what has nothing to show is let go of: a task that merged (its
// workspace is gone), went back to Plan, or was deleted, and a project that
// left the board. A project that is merely not here *yet* — its host still
// connecting — keeps its holds until the host has spoken.
A(() => {
	if (!$state.ready) return;
	for (const key of Object.keys($holds)) {
		const [pid, tid] = key.split('/');
		const $p = $state.projects[pid];
		if (!$p) {
			const $h = $state.hosts?.[pid.split(':')[0]];
			if (!$h || $h.status === 'connected') release(pid, tid === '-' ? undefined : tid);
			continue;
		}
		if (tid === '-') continue;
		const phase = $p.tasks?.[tid]?.phase;
		if (!phase || phase === 'plan' || phase === 'done') release(pid, tid);
	}
});
