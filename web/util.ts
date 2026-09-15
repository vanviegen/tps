import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { bellOff, check, circle, circleSlash, gitMerge, globe, hourglass, listTodo, monitor, scanEye, server, user } from 'staffa/icons.js';
import { bot } from './bot.ts';
import { $state, send } from './conn.ts';

/**
 * The phases a task can be in. Review is a second agent reading the work over
 * before it is handed on. Muted is Human with the task put away: it waits
 * for you too, but quietly — at the foot of the Human column, and out of the
 * sidebar. Closed is Done without the merge: the task is over, but its work
 * is kept as a patch rather than landing on the branch.
 */
export const PHASES = ['plan', 'agent', 'review', 'human', 'muted', 'merge', 'done', 'closed'] as const;
export type Phase = typeof PHASES[number];

/**
 * The columns of the board. Three phases have none of their own: Muted sits
 * under the Human column, and Merging and Closed share the Done one — merging
 * is a moment on the way there, closing is arriving without it (see board.ts).
 */
export const COLUMNS: Phase[] = ['plan', 'agent', 'review', 'human', 'done'];

export const PHASE_LABELS: Record<Phase, string> = {
	plan: 'Plan', agent: 'Agent', review: 'Review', human: 'Human', muted: 'Muted', merge: 'Merging', done: 'Done', closed: 'Not merged',
};

export const PHASE_ICONS: Record<Phase, typeof bot> = { plan: listTodo, agent: bot, review: scanEye, human: user, muted: bellOff, merge: gitMerge, done: check, closed: circleSlash };

/** Whether the task has a workspace right now: what the daemon says it sees on disk. */
export function hasWorkspace($t: any): boolean {
	return !!$t.worktree;
}

/**
 * Whether the task is one to keep open: it has a workspace, or is about to get
 * one. A task in Plan has none yet and a finished one no longer. This goes by
 * the phase rather than by the workspace, which lags a moment behind it.
 */
export function isOpenable($t: any): boolean {
	return $t.phase !== 'plan' && !isFinished($t);
}

/** Whether the task is over: merged, or closed without merging. Nothing waits for it. */
export function isFinished($t: any): boolean {
	return $t.phase === 'done' || $t.phase === 'closed';
}

/** Whether the task is waiting for a human: the Human phase, or that with the task muted. */
export function waitsForHuman($t: any): boolean {
	return $t.phase === 'human' || $t.phase === 'muted';
}

/** Attrs for text that must stay on one line, cut off with an ellipsis. */
export const ELLIPSIS = 'white-space:nowrap overflow:hidden text-overflow:ellipsis';

/**
 * The accents a project may wear (the daemon hands a new project one of them
 * at random; the list here is the palette to change it from): hues that sit
 * well on the dark surfaces and apart from one another, so a colour tells
 * projects apart where a name would not fit.
 */
export const PROJECT_COLORS = [
	'#5b9cf5', '#9b7bf0', '#e07bd6', '#f06b8a', '#d9a441',
	'#c8d35a', '#7bd36f', '#45c4d6', '#f2d35b', '#b98a6a',
];

/** The project's accent colour: what its daemon gave it, or one from its name for a daemon that gives none. */
export function projectColor($p: any): string {
	const color = $p?.color;
	if (typeof color === 'string' && /^#[0-9a-f]{6}$/i.test(color)) return color;
	let hash = 0;
	for (const ch of $p?.name ?? '') hash = (hash * 31 + ch.charCodeAt(0)) >>> 0;
	return PROJECT_COLORS[hash % PROJECT_COLORS.length];
}

/**
 * What stands for the project where there is no room for its name: the letters
 * the user gave it, or its initials — Aberdeen becomes A, ShoTest ST,
 * wild-mail WM. Words are split on anything that is not a letter or digit,
 * and on the case change inside a camel-cased one.
 */
export function projectInitials($p: any): string {
	const initials = ($p?.initials ?? '').trim();
	if (initials) return initials;
	const words = ($p?.name ?? '')
		.split(/[^\p{L}\p{N}]+/u)
		.flatMap((w: string) => w.split(/(?<=[\p{Ll}\p{N}])(?=\p{Lu})/u))
		.filter(Boolean);
	if (!words.length) return '?';
	return words.slice(0, 2).map((w: string) => w[0].toUpperCase()).join('');
}

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

// The one thing that says "this is going on right now", wherever it is said:
// a glyph breathing. Gentler than a turning one, which is what a task's icon
// needs — some of what it marks lasts a phase long, not the moment of an action.
A.insertGlobalCss({ '@keyframes pulse': { '50%': 'opacity:0.35' } });

/**
 * The attrs that make an icon say it: the robot has a better way than
 * breathing — it types and thinks (see bot.ts), in the primary colour so that
 * it stands out from whatever is around it — and every other glyph breathes.
 */
export function busyAttrs(icon: typeof bot): string {
	return icon === bot ? '.tps-busy fg:$s-primary' : 'animation: pulse 1.6s ease-in-out infinite;';
}

/**
 * What the URL selects: a project, and within it a task, its base worktree, or
 * neither. Everything on screen follows from this, so navigating is a link.
 */
export function selection(): { pid?: string; tid?: string; base?: boolean } {
	const p = route.current.p;
	if (p[0] !== 'p' || !p[1]) return {};
	return { pid: p[1], tid: p[2] === 't' ? p[3] : undefined, base: p[2] === 'base' };
}

/**
 * What the project's own checkout is called wherever it is listed or opened:
 * its branch, so "main branch" for most. The sidebar row, the tab title, the
 * palette and the project's menu all say the same.
 */
export function branchLabel($p: any): string {
	return `${$p?.defaultBranch ?? 'main'} branch`;
}

/**
 * The path selecting a project, and in it a task — `'base'` for the base
 * worktree. Task ids are numbers, so that name cannot collide with one.
 */
export function pathTo(pid: string, tid?: string): string {
	return `/p/${pid}` + (tid === 'base' ? '/base' : tid ? `/t/${tid}` : '');
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
	return ($t.startAfter ?? []).filter((d: string) => $tasks[d] && !isFinished($tasks[d]));
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
 * asked for one), so until then its description's first line stands in for it
 * — and a task with nothing written down yet is simply "New".
 */
export function taskTitle($t: any, fallback = 'New'): string {
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

/** When an agent parked on claude's usage limit goes back in: the clock, with the day if it is not today's. */
function limitTime(ms: number): string {
	const at = new Date(ms);
	const clock = at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
	return at.toDateString() === new Date().toDateString() ? clock : `${at.toLocaleDateString([], { month: 'short', day: 'numeric' })}, ${clock}`;
}

/** What the workspace is up to (or what the task waits for), as text plus a color role. */
export function taskActivity(pid: string, $t: any): { text: string; color: string } {
	if ($t.limitUntil) return { text: `waits for claude's usage limit, back at ${limitTime($t.limitUntil)}`, color: 'warning' };
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

/** Where the task stands, in one line: its phase, whether claude is on it, and what its workspace is doing. */
export function taskTip(pid: string, $t: any): string {
	const phase = PHASE_LABELS[$t.phase as Phase] ?? $t.phase;
	return `${phase}${$t.working ? ', claude is working' : ''} · ${taskActivity(pid, $t).text}`;
}

/**
 * The task at a glance: its phase as an icon — the robot at work while claude
 * is, the merge icon breathing while a merge runs, an hourglass while it waits
 * (for the tasks it follows, or for claude's usage limit to reset) — colored
 * by the container status.
 *
 * `tip: false` is for where something around it already tells that story — the
 * collapsed sidebar, whose rows carry it themselves. `color` is for where the
 * icon belongs to something it should look part of — the sidebar again, where
 * it sits in the row's text — and gives way to the status colour when the
 * workspace is in trouble, which no cohesion is worth hiding. A working robot
 * wears the primary colour wherever it is (see busyAttrs).
 */
export function drawTaskIcon(pid: string, $t: any, { tip = true, color }: { tip?: boolean; color?: string } = {}): void {
	A(() => {
		const activity = taskActivity(pid, $t);
		const icon = autoStarts($t) || $t.limitUntil ? hourglass : PHASE_ICONS[$t.phase as Phase] ?? circle;
		// The phase's own icon throughout, at work while something is going on:
		// the robot typing while claude does, the merge icon breathing during the
		// merge a task in Merging is in the middle of — which is what tells those
		// apart from the merged ones they sit above on the board. A spinner's
		// glyph is for a spinner; this one does not turn.
		const busy = $t.working || $t.phase === 'merge' || $t.phase === 'review';
		A(`span display:inline-flex flex-shrink:0 fg:${color && activity.color !== 'danger' ? color : `$s-${activity.color}`}`, () => {
			if (tip) S.addTooltip({ tip: () => A('text=', taskTip(pid, $t)) });
			icon({ size: '1.1em', attrs: busy ? busyAttrs(icon) : undefined });
		});
	});
}

/** "$0.42 / $2.00": what the task spent, and its budget if it has one. */
export function costText($t: any): string | undefined {
	if (!$t.spent && !$t.budget) return;
	return `$${($t.spent ?? 0).toFixed(2)}${$t.budget ? ` / $${$t.budget.toFixed(2)}` : ''}`;
}

/** A token count the way the log words it: 950, 9k, 34k, 1.2M. */
export function tokenText(n: number): string {
	const round = (v: number) => v.toFixed(1).replace(/\.0$/, '');
	if (n >= 1e6) return `${round(n / 1e6)}M`;
	if (n >= 1e4) return `${Math.floor(n / 1e3)}k`;
	if (n >= 1e3) return `${round(n / 1e3)}k`;
	return String(n);
}

/** Past this much of its window, the agent is close enough to forgetting to say so. */
const CONTEXT_FULL = 0.9;

/** The colours the fixed parts of the window wear, in the order claude lists them. */
const PART_COLORS = ['#5b9cf5', '#9b7bf0', '#45c4d6', '#7bd36f', '#d9a441'];

/** One wedge of the context ring: how much of the window, in what colour, called what. */
interface ContextSlice { tokens: number; color: string; label: string }

/**
 * The agent's context window as wedges to draw: what the daemon measured it to
 * hold, the conversation last of all — the part that grows, and the part
 * compacting takes back, so the only one worth a colour that says so.
 * Undefined before the agent has run, there being nothing measured yet.
 */
export function contextSlices($t: any): { slices: ContextSlice[]; used: number; limit: number; full: boolean } | undefined {
	const ctx = $t.context;
	if (!ctx?.limit || !ctx.used) return;
	const full = ctx.used / ctx.limit >= CONTEXT_FULL;
	const parts: { name: string; tokens: number }[] = ctx.parts ?? [];
	const slices = parts.map((p, i) => ({
		tokens: p.tokens,
		label: p.name,
		color: i === parts.length - 1
			? (full ? 'var(--s-warning)' : 'var(--s-primary)')
			: PART_COLORS[i % PART_COLORS.length],
	}));
	return { slices, used: ctx.used, limit: ctx.limit, full };
}

/**
 * The window as a ring, a wedge per part, filling clockwise from twelve
 * o'clock; what is left of it is the faint track behind them. The conversation
 * is the wedge on the end, so it is the one seen growing — and the one seen
 * vanishing when claude compacts it away.
 */
export function drawContextRing($t: any, size = '1.1em'): void {
	A(() => {
		const ctx = contextSlices($t);
		if (!ctx) return;
		const r = 9;
		const arc = 2 * Math.PI * r;
		const ring = (len: number, from: number, stroke: string, opacity = 1) =>
			`<circle cx="12" cy="12" r="${r}" fill="none" stroke="${stroke}" stroke-opacity="${opacity}" stroke-width="4"`
			+ ` transform="rotate(-90 12 12)" stroke-dasharray="${len.toFixed(2)} ${(arc - len).toFixed(2)}"`
			+ ` stroke-dashoffset="${(-from).toFixed(2)}"/>`;
		let html = ring(arc, 0, 'currentColor', 0.2); // the room there is, all the way round
		let at = 0;
		for (const slice of ctx.slices) {
			const len = Math.min(arc - at, (slice.tokens / ctx.limit) * arc);
			if (len > 0) html += ring(len, at, slice.color);
			at += len;
		}
		const el = A('svg aria-hidden=true viewBox="0 0 24 24" fill=none', 'width=', size, 'height=', size) as SVGSVGElement;
		// Children of an <svg> have to be built in its own namespace, which is
		// what setting them as markup on the element itself does.
		el.innerHTML = html;
	});
}

/**
 * The ring in words: what fills the window, wedge by wedge, and what claude
 * does when it is full. Whoever shows this offers the compacting themselves.
 */
export function drawContextTip($t: any, action = ''): void {
	const ctx = contextSlices($t);
	if (!ctx) return;
	A('div display:flex flex-direction:column gap:$1 text-align:left', () => {
		A('text=', `The agent is holding ${tokenText(ctx.used)} of the ${tokenText(ctx.limit)} it has room for:`);
		for (const slice of ctx.slices) {
			if (slice.tokens <= 0) continue;
			A('div display:flex align-items:center gap:$2', () => {
				A(`span w:0.6em h:0.6em r:99em flex-shrink:0 bg:${slice.color}`);
				A('span flex:1 text=', slice.label);
				A('span text=', tokenText(slice.tokens));
			});
		}
		A('small text=', 'Claude sums the conversation up in a paragraph and forgets the rest when the window fills up.'
			+ (action ? ' ' + action : ''));
	});
}

/** Where a forwarded port of a task is reached from this browser. */
export function portUrl(p: { host: number }): string {
	return `http://${location.hostname}:${p.host}/`;
}

/** A globe link per forwarded port that something inside the container answers HTTP on. */
export function drawLiveLink($t: any): void {
	A(() => {
		for (const p of $t.ports ?? []) {
			if (!p.live) continue;
			A('a display:inline-flex flex-shrink:0', 'href=', portUrl(p), 'target=_blank',
				'click=', (e: Event) => e.stopPropagation(),
				() => {
					S.addTooltip({ tip: `Open port ${p.port}, served by this task` });
					globe({ size: '1.1em' });
				});
		}
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

// The composers on screen, by task: a draft handed back from somewhere other
// than the field itself has to reach the field the user is looking at, and
// the textarea's value is not drawn from reactive state (see drawInputBar).
const composers: Record<string, (text: string) => void> = {};

/** Register this task's composer for as long as it is drawn. */
export function onComposer(pid: string, tid: string, fill: (text: string) => void): void {
	const key = `${pid}/${tid}`;
	composers[key] = fill;
	A.clean(() => { if (composers[key] === fill) delete composers[key]; });
}

/**
 * Put a message back in a task's composer: what the user said after a revert
 * point is theirs again once the run it set off is undone or forked away, to
 * send as it stands or to word differently. A draft they are part-way through
 * writing wins — this is a convenience, and overwriting that would not be one.
 */
export function restoreDraft(pid: string, tid: string, text: string): void {
	if (!text.trim() || chatDraft(pid, tid).trim()) return;
	setChatDraft(pid, tid, text);
	composers[`${pid}/${tid}`]?.(text);
}
