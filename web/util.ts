import A from 'aberdeen';
import * as S from 'staffa';
import { bot, check, circle, gitMerge, globe, hourglass, listTodo, loaderCircle, user } from 'staffa/icons.js';
import { $state, send } from './conn.ts';

export const PHASES = ['plan', 'agent', 'human', 'merge', 'done'] as const;
export type Phase = typeof PHASES[number];

export const PHASE_LABELS: Record<Phase, string> = {
	plan: 'Plan', agent: 'Agent', human: 'Human', merge: 'Merge', done: 'Done',
};

const PHASE_ICONS: Record<Phase, typeof bot> = { plan: listTodo, agent: bot, human: user, merge: gitMerge, done: check };

/** Attrs for text that must stay on one line, cut off with an ellipsis. */
export const ELLIPSIS = 'white-space:nowrap overflow:hidden text-overflow:ellipsis';

A.insertGlobalCss({ '@keyframes spin': { to: 'transform:rotate(360deg)' } });

/** Dependencies of `$t` that still block it: existing, unmerged tasks. */
export function blockingDeps(pid: string, $t: any): string[] {
	const $tasks = $state.projects[pid]?.tasks ?? {};
	return ($t.dependencies ?? []).filter((d: string) => $tasks[d] && $tasks[d].phase !== 'done');
}

/** A host's name as shown to the user. */
export function hostName(hid: string): string {
	return hid === 'local' ? 'this machine' : $state.hosts?.[hid]?.name ?? hid;
}

/** A task by name, for anywhere one task points at another. */
export function taskName(pid: string, tid: string): string {
	const $t = $state.projects[pid]?.tasks?.[tid];
	return !$t ? '(deleted)' : $t.title || '(untitled)';
}

/** What the workspace is up to (or what the task waits for), as text plus a color role. */
export function taskActivity(pid: string, $t: any): { text: string; color: string } {
	if ($t.waiting) {
		const deps = blockingDeps(pid, $t);
		const what = deps.length > 1 ? `${deps.length} tasks` : deps.length ? taskName(pid, deps[0]) : 'dependencies';
		return { text: `waiting for ${what}`, color: 'warning' };
	}
	switch ($t.status) {
		case 'building': return { text: 'building the container image', color: 'warning' };
		case 'starting': return { text: 'starting the container', color: 'warning' };
		case 'stopping': return { text: 'shutting down', color: 'warning' };
		case 'up': return { text: 'container up', color: 'success' };
		case 'error': return { text: `workspace error${$t.statusDetail ? ': ' + $t.statusDetail : ''}`, color: 'danger' };
		default: return { text: 'container down', color: 'muted' };
	}
}

/**
 * The task at a glance: its phase as an icon (a spinner while claude works, an
 * hourglass while it waits for dependencies), colored by the container status.
 */
export function drawTaskIcon(pid: string, $t: any): void {
	A(() => {
		const activity = taskActivity(pid, $t);
		const icon = $t.working ? loaderCircle : $t.waiting ? hourglass : PHASE_ICONS[$t.phase as Phase] ?? circle;
		const phase = PHASE_LABELS[$t.phase as Phase] ?? $t.phase;
		A(`span display:inline-flex flex-shrink:0 fg:$s-${activity.color}`, () => {
			S.addTooltip({ tip: () => A('text=', `${phase}${$t.working ? ', claude is working' : ''} · ${activity.text}`) });
			icon({ size: '1.1em', attrs: $t.working ? 'animation: spin 1.2s linear infinite;' : undefined });
		});
	});
}

/** "$0.42 / $2.00": what the task spent, and its budget if it has one. */
export function costText($t: any): string | undefined {
	if (!$t.spent && !$t.budget) return;
	return `$${($t.spent ?? 0).toFixed(2)}${$t.budget ? ` / $${$t.budget.toFixed(2)}` : ''}`;
}

/** A globe link, shown while something inside the container answers on its $PORT. */
export function drawLiveLink($t: any): void {
	A(() => {
		if (!$t.live || !$t.appPort) return;
		A('a display:inline-flex flex-shrink:0', 'href=', `http://${location.hostname}:${$t.appPort}/`, 'target=_blank',
			'click=', (e: Event) => e.stopPropagation(),
			() => {
				S.addTooltip({ tip: 'Open the page served by this task' });
				globe({ size: '1.1em' });
			});
	});
}

/** Run a server command; on failure show a toast and return undefined. */
export async function cmd(name: string, args?: object): Promise<any> {
	try {
		return await send(name, args) ?? true;
	} catch (e) {
		S.toast({ title: name, message: (e as Error).message, type: 'danger', duration: 10_000 });
		return undefined;
	}
}

export function debounce<T extends unknown[]>(ms: number, fn: (...args: T) => void): (...args: T) => void {
	let timer: ReturnType<typeof setTimeout>;
	return (...args: T) => {
		clearTimeout(timer);
		timer = setTimeout(() => fn(...args), ms);
	};
}

/** Numeric-aware sort key, so task '10' comes after '9'. */
export function tidOrder(tid: string): string {
	return tid.padStart(8, '0');
}
