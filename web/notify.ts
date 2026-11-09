import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { $state } from './conn.ts';
import { canMerge, isJob, pathTo, projectName, taskTitle, type Phase } from './util.ts';

/**
 * Notifications about a task being back with you: the desktop's own, so a
 * dashboard behind other windows — or an installed TPS, which is this app in
 * a window of its own (see web/manifest.webmanifest) — still says when an
 * agent is done or a merge has landed.
 *
 * Which tasks these are asked for is per task and per browser: the permission
 * is the browser's, and so is the window that would show them. The set lives
 * in localStorage, and the tasks that are gone
 * are swept out of it as they are seen to be gone. A project can have them
 * switched on for the tasks made from now on — its own default, kept the same
 * way, since what it sets is this browser's business too.
 *
 * These are the page's own notifications rather than a service worker's, which
 * is as far as this goes and no further: nothing is delivered by a push
 * service, so a task that comes ready while every TPS window is closed goes
 * unannounced — and a notification belongs to the window that made it, which
 * is the one a click brings forward. Closing that window takes its notifications
 * with it.
 */
const NOTIFY_KEY = 'tps.notify';
/** The projects whose new tasks are switched on the moment they are made. */
const DEFAULT_KEY = 'tps.notifyDefaults';
/** That this browser has been shown what a notification looks like, once. */
const TESTED_KEY = 'tps.notifyTested';

const $on: Record<string, true> = A.proxy({} as Record<string, true>);
const $byDefault: Record<string, true> = A.proxy({} as Record<string, true>);

function key(pid: string, tid: string): string {
	return `${pid}/${tid}`;
}

/**
 * Whether this browser announces this task being ready — or, without a task,
 * whether new tasks of the project start out doing so. Reactive.
 */
export function notifies(pid: string, tid?: string): boolean {
	return tid ? !!$on[key(pid, tid)] : !!$byDefault[pid];
}

/**
 * Turn the announcements for a task on or off, or without one the project's
 * default. Turning either on is where the browser is asked for permission —
 * there is nothing to ask about until something wants to notify, and the tasks
 * a default is handed to later have nothing left to ask — and, the first time
 * it is given, one notification is sent right away: that the permission took
 * is worth seeing rather than assuming. Only tasks made from here on get a
 * default; the ones that exist keep what they have, as with every other one.
 */
export async function toggleNotifies(pid: string, tid?: string): Promise<void> {
	const [$set, k] = tid ? [$on, key(pid, tid)] : [$byDefault, pid];
	if (A.peek(() => $set[k])) delete $set[k];
	else if (await allowed()) $set[k] = true;
	else return;
	persist();
}

/**
 * Give a task just made here its project's default, without a word: permission
 * was asked for when the default was switched on, and where it is gone there
 * is nothing to switch on now.
 */
export function applyNotifyDefault(pid: string, tid: string): void {
	if (!A.peek(() => $byDefault[pid]) || !granted()) return;
	$on[key(pid, tid)] = true;
	persist();
}

/** Whether this page may show notifications. */
function granted(): boolean {
	return typeof Notification !== 'undefined' && Notification.permission === 'granted';
}

async function allowed(): Promise<boolean> {
	if (typeof Notification === 'undefined') {
		S.toast({ message: 'This browser has no notifications to give (they need a secure page: localhost, or https).', type: 'danger', duration: 8000 });
		return false;
	}
	if (Notification.permission === 'default') {
		let answer = 'denied';
		try {
			answer = await Notification.requestPermission();
		} catch {} // an older browser with the callback form only
		if (answer !== 'granted') {
			S.toast({ message: 'Notifications were not allowed, so this task cannot announce itself.', type: 'danger', duration: 8000 });
			return false;
		}
	} else if (Notification.permission !== 'granted') {
		S.toast({ message: 'Notifications are blocked for this page; allow them in the browser’s site settings to switch this on.', type: 'danger', duration: 10_000 });
		return false;
	}
	if (!localStorage.getItem(TESTED_KEY)) {
		try {
			localStorage.setItem(TESTED_KEY, '1');
		} catch {} // storage off: the test notification is shown once per switch-on then
		show('TPS', 'This is what a task saying it is ready will look like.', '/');
	}
	return true;
}

function persist(): void {
	try {
		localStorage.setItem(NOTIFY_KEY, JSON.stringify(A.peek(() => Object.keys($on))));
		localStorage.setItem(DEFAULT_KEY, JSON.stringify(A.peek(() => Object.keys($byDefault))));
	} catch {} // storage off or full: the setting lasts as long as the tab does
}

try {
	const saved = JSON.parse(localStorage.getItem(NOTIFY_KEY) || '[]');
	if (Array.isArray(saved)) for (const k of saved) if (typeof k === 'string' && k.includes('/')) $on[k] = true;
	const defaults = JSON.parse(localStorage.getItem(DEFAULT_KEY) || '[]');
	if (Array.isArray(defaults)) for (const pid of defaults) if (typeof pid === 'string') $byDefault[pid] = true;
} catch {}

function show(title: string, body: string, url: string): void {
	if (!granted()) return;
	try {
		// One notification per task: a task that reaches the human through a
		// merge replaces its own earlier line rather than stacking up beside it.
		const note = new Notification(title, { body, icon: '/icon-192.png', tag: `tps:${url}` });
		note.onclick = () => {
			window.focus(); // this window, installed app or tab, comes forward
			note.close();
			void route.go(url);
		};
	} catch {} // a browser that shows notifications through a service worker only (phones do)
}

/**
 * What a task leaving the Agent or the Merge phase has to say, or nothing at
 * all when it has nothing: the agent handing a task to the merge is on its
 * way out rather than out, and the merge it is on its way to speaks for both.
 */
function announcement(pid: string, $t: any, from: Phase): string | undefined {
	const phase = $t.phase as Phase;
	if (isJob($t)) return phase === 'human' ? 'failed' : $t.job === 'deploy' && phase === 'merged' ? 'deployed ✔' : undefined;
	switch (phase) {
		case 'merging': case 'review': return;
		case 'merged': return 'merged ✔';
		case 'closed': return 'closed without merging';
		case 'plan': return 'put back in Plan';
		case 'agent': return from === 'merging' ? 'the agent is resolving the merge conflicts' : undefined;
		default: // human, muted: the ones worth having asked for
			if ($t.review) return 'the automated review asks for changes';
			if (canMerge(pid, $t)) return 'the agent reports it ready to merge';
			return from === 'merging' ? 'back with you' : 'the agent is done; it is yours';
	}
}

/** Whether a project may only not be here yet: missing, with its host still connecting. */
function unheard(pid: string): boolean {
	if (A.peek(() => $state.projects[pid])) return false;
	const $h = A.peek(() => $state.hosts?.[pid.split(':')[0]]);
	return !!$h && $h.status !== 'connected';
}

/** Drop the settings that are not to be `kept`. */
function sweep($set: Record<string, true>, kept: (k: string) => boolean): void {
	for (const k of A.peek(() => Object.keys($set))) {
		if (kept(k)) continue;
		delete $set[k];
		persist();
	}
}

/** The phase each task was last seen in: what a change is told from. */
const seen = new Map<string, Phase>();
/** The tasks last seen with a request waiting for the user: for OpenRouter budget, or project settings. */
const asking = new Set<string>();

/**
 * Watch every task's phase for the two transitions worth announcing: out of
 * Agent (the agent is done with it) and out of Merge (the merge landed, or
 * didn't) — and for an agent asking for OpenRouter budget, or suggesting
 * project settings, which wait on the user without leaving their phase. A project's
 * jobs (see isJob) have nobody to switch this on for them, and announce
 * themselves to every browser that allows notifications. Tasks are seen before they are watched — the
 * board knows them all — so this works for any task, not only the one on screen.
 *
 * A task is only announced once it has been seen in some phase before: the
 * ones that arrive with the connection are the state of things, not news.
 */
export function watchPhases(): void {
	A(() => {
		if (!$state.ready) return;
		const live = new Set<string>();
		for (const [pid, $p] of Object.entries($state.projects ?? {}) as [string, any][]) {
			for (const [tid, $t] of Object.entries($p.tasks ?? {}) as [string, any][]) {
				const k = key(pid, tid);
				live.add(k);
				const ask = !!$t.openrouterAsk || !!$t.suggestion;
				const on = A.peek(() => $on[k] || isJob($t));
				if (ask && !asking.has(k) && seen.has(k) && on) {
					const what = A.peek(() => $t.suggestion) ? 'the agent suggests project settings' : 'the agent asks for OpenRouter budget';
					show(A.peek(() => taskTitle($t)), `${A.peek(() => projectName($p))}: ${what}`, pathTo(pid, tid));
				}
				if (ask) asking.add(k);
				else asking.delete(k);
				const phase = $t.phase as Phase;
				const was = seen.get(k);
				seen.set(k, phase);
				if (was === undefined || was === phase || (was !== 'agent' && was !== 'merging' && was !== 'review')) continue;
				if (!on) continue;
				// Everything about the task but its phase is read past the
				// subscription: this scope is about phases, and a commit message
				// arriving should not run it again.
				const note = A.peek(() => announcement(pid, $t, was));
				if (note) show(A.peek(() => taskTitle($t)), `${A.peek(() => projectName($p))}: ${note}`, pathTo(pid, tid));
			}
		}
		for (const k of seen.keys()) if (!live.has(k)) seen.delete(k), asking.delete(k);
		// A deleted task takes its setting with it, and a deleted project its
		// default. Those of a project that is not here *yet* are not gone, only
		// quiet, and are kept until its host has spoken (as a hold does).
		sweep($on, k => live.has(k) || unheard(k.split('/')[0]));
		sweep($byDefault, pid => !!A.peek(() => $state.projects[pid]) || unheard(pid));
	});
}
