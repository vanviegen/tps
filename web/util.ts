import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { bot, check, circle, gitMerge, globe, hourglass, listTodo, loaderCircle, monitor, server, user } from 'staffa/icons.js';
import { $state, send } from './conn.ts';

export const PHASES = ['plan', 'agent', 'human', 'merge', 'done'] as const;
export type Phase = typeof PHASES[number];

export const PHASE_LABELS: Record<Phase, string> = {
	plan: 'Plan', agent: 'Agent', human: 'Human', merge: 'Merge', done: 'Done',
};

export const PHASE_ICONS: Record<Phase, typeof bot> = { plan: listTodo, agent: bot, human: user, merge: gitMerge, done: check };

/** Attrs for text that must stay on one line, cut off with an ellipsis. */
export const ELLIPSIS = 'white-space:nowrap overflow:hidden text-overflow:ellipsis';

// A strip that is a button all over: it lights up like one under the pointer.
const clickableStrip = A.insertCss({
	'&': 'cursor:pointer transition: filter 0.12s;',
	'&:hover, &:focus-visible': 'filter: brightness(1.25)',
});

/**
 * A line about something that wants attention, with what to do about it beside
 * it. Give `click` and the strip is the button itself, for the cases where the
 * whole line means one thing — a login waiting to be given.
 */
export function drawStrip(color: string, text: string, action?: () => void, click?: () => void): void {
	A(`div.s-s.${color}.tonal p:$2 r:$s-radius-sm display:flex flex-wrap:wrap align-items:center gap:$2`,
		click ? clickableStrip : null, () => {
			if (click) {
				A('role=button tabindex=0 click=', click, 'keydown=', (e: KeyboardEvent) => {
					if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); click(); }
				});
			}
			// Whatever the server says may hold something unbreakable (an ssh
			// fingerprint): let it break rather than push what follows off the box.
			A('span flex:1 min-width:0 overflow-wrap:anywhere rich=', text);
			action?.();
		});
}

A.insertGlobalCss({ '@keyframes spin': { to: 'transform:rotate(360deg)' } });

/**
 * What the URL selects: a project, and within it a task, its base worktree, a
 * task being drafted, or none of those. Everything on screen follows from
 * this, so navigating is a link.
 */
export function selection(): { pid?: string; tid?: string; base?: boolean; draft?: boolean } {
	const p = route.current.p;
	if (p[0] !== 'p' || !p[1]) return {};
	return { pid: p[1], tid: p[2] === 't' ? p[3] : undefined, base: p[2] === 'base', draft: p[2] === 'draft' };
}

/**
 * The path selecting a project, and in it a task — `'base'` for the base
 * worktree, `'draft'` for the task being written but not yet created. Task ids
 * are numbers, so neither name can collide with one.
 */
export function pathTo(pid: string, tid?: string): string {
	return `/p/${pid}` + (tid === 'base' || tid === 'draft' ? `/${tid}` : tid ? `/t/${tid}` : '');
}

/** How many things wait for a human, as a pill. Nothing waiting draws nothing. */
export function drawBadge(count: number): void {
	if (!count) return;
	A('span.s-s.warning font-size:0.7em font-weight:700 line-height:1 ph:0.45em pv:0.25em r:1em flex-shrink:0',
		'text=', String(count));
}

/** A task in Plan set to follow others: it leaves Plan by itself once they are done. */
export function autoStarts($t: any): boolean {
	return $t.phase === 'plan' && !!$t.startAfter?.length;
}

/** The tasks `$t` follows that are not there yet: existing tasks not yet done. */
export function waitingFor(pid: string, $t: any): string[] {
	const $tasks = $state.projects[pid]?.tasks ?? {};
	return ($t.startAfter ?? []).filter((d: string) => $tasks[d] && $tasks[d].phase !== 'done');
}

/** The icon a host goes by: this machine, or another one. */
export function hostIcon(hid: string): typeof monitor {
	return hid === 'local' ? monitor : server;
}

/** A host's name as shown to the user. */
export function hostName(hid: string): string {
	return hid === 'local' ? 'localhost' : $state.hosts?.[hid]?.name ?? hid;
}

/**
 * A path on a host as you would type it there: with ~ for the home directory
 * of the user its daemon runs as. Hosts running an older daemon don't say
 * where that is, and get their paths in full.
 */
export function shortDir(hid: string, dir: string): string {
	const home = $state.hosts?.[hid]?.home;
	if (!home || !dir) return dir ?? '';
	if (dir === home) return '~';
	return dir.startsWith(home + '/') ? '~' + dir.slice(home.length) : dir;
}

/**
 * What to call a task. A task gets its title when it leaves Plan (claude is
 * asked for one), so until then its description stands in for it.
 */
export function taskTitle($t: any, fallback = '(untitled)'): string {
	const title = ($t?.title ?? '').trim();
	if (title) return title;
	const [line] = ($t?.description ?? '').replace(/^[#\s]+/, '').split('\n');
	if (!line.trim()) return fallback;
	return line.length > 60 ? line.slice(0, 60) + '…' : line;
}

/** A task by name, for anywhere one task points at another. */
export function taskName(pid: string, tid: string): string {
	const $t = $state.projects[pid]?.tasks?.[tid];
	return !$t ? '(deleted)' : taskTitle($t);
}

/** What the workspace is up to (or what the task waits for), as text plus a color role. */
export function taskActivity(pid: string, $t: any): { text: string; color: string } {
	if (autoStarts($t)) {
		const pending = waitingFor(pid, $t);
		if (!pending.length) return { text: 'starts once its plan is closed', color: 'warning' };
		const what = pending.length > 1 ? `${pending.length} tasks` : taskName(pid, pending[0]);
		return { text: `starts after ${what}`, color: 'warning' };
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
 * hourglass while it waits for the tasks it follows), colored by the container status.
 */
export function drawTaskIcon(pid: string, $t: any): void {
	A(() => {
		const activity = taskActivity(pid, $t);
		const icon = $t.working ? loaderCircle : autoStarts($t) ? hourglass : PHASE_ICONS[$t.phase as Phase] ?? circle;
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

/**
 * Where a task sits among its peers: most recently moved first, so what just
 * happened is on top of its column. Tasks from before phase changes were
 * timestamped have no `phaseAt` and settle at the bottom, newest tid first.
 */
export function phaseOrder($t: any, tid: string): number[] {
	return [-($t.phaseAt ?? 0), -Number(tid)];
}

/**
 * Message drafts: what was typed into a task's chat input but never sent. They
 * live in localStorage, so leaving the task — or closing the tab — does not
 * throw away half a thought. One key holds them all, which makes dropping the
 * ones whose task is gone a matter of rewriting it.
 */
const DRAFTS_KEY = 'tps.chatDrafts';

function readDrafts(): Record<string, string> {
	try {
		const drafts = JSON.parse(localStorage.getItem(DRAFTS_KEY) || '{}');
		return drafts && typeof drafts === 'object' ? drafts : {};
	} catch {
		return {};
	}
}

/** What was left unsent in this task's chat input, if anything. */
export function chatDraft(pid: string, tid: string): string {
	const text = readDrafts()[`${pid}/${tid}`];
	return typeof text === 'string' ? text : '';
}

/** Remember (or, for empty text, forget) this task's unsent message. */
export function setChatDraft(pid: string, tid: string, text: string): void {
	const drafts = readDrafts();
	const key = `${pid}/${tid}`;
	if ((drafts[key] ?? '') === text) return;
	if (text) drafts[key] = text;
	else delete drafts[key];
	// Deleted and merged tasks leave their draft behind; sweep those up here,
	// but only once the task list is known — before that everything looks gone.
	if (A.peek(() => $state.ready)) {
		for (const k of Object.keys(drafts)) {
			const [p, t] = k.split('/');
			if (!A.peek(() => $state.projects[p]?.tasks?.[t])) delete drafts[k];
		}
	}
	try {
		localStorage.setItem(DRAFTS_KEY, JSON.stringify(drafts));
	} catch {} // storage off or full: a draft is a convenience, not data
}
