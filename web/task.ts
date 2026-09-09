import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { bot, circleStop, gitMerge, sendHorizontal, trash2, user } from 'staffa/icons.js';
import { drawChat } from './chat.ts';
import { drawCode } from './code.ts';
import { $state, watchTask } from './conn.ts';
import { autoStarts, cmd, debounce, pathTo, PHASES, PHASE_ICONS, PHASE_LABELS, taskActivity, taskName, tidOrder, waitingFor, type Phase } from './util.ts';

/** Tasks of a project waiting for a human, not counting `skip`. */
export function humanTasks(pid: string, skip?: string): number {
	const tasks = Object.entries($state.projects[pid]?.tasks ?? {}) as [string, any][];
	return tasks.filter(([tid, $t]) => tid !== skip && $t.phase === 'human').length;
}

/** Keep the task's workspace up and its chat streaming, for as long as the calling scope lives. */
export function useTask(pid: string, tid: string, $t: any): void {
	watchTask(pid, tid);
	A(() => { if ($t.phase !== 'plan') void cmd('openTask', { pid, tid }); });
}

/** The phases the task can be moved to; the one it is in is the disabled one. */
export function phaseItems(pid: string, tid: string, $t: any): S.MenuEntry[] {
	return PHASES.map(phase => ({
		label: PHASE_LABELS[phase],
		icon: PHASE_ICONS[phase],
		disabled: phase === $t.phase,
		attrs: phase === 'plan' ? 'fg:$s-danger' : '',
		click: () => void moveTask(pid, tid, $t, phase),
	}));
}

/** Move a task to a phase, confirming when that discards work, and confirming the commit message when it merges. */
export async function moveTask(pid: string, tid: string, $t: any, phase: string): Promise<void> {
	if (phase === $t.phase) return;
	if (phase === 'plan') {
		const busy = $t.working ? ' The agent is still working; it is stopped.' : '';
		if (!(await S.confirm(`Move this task back to **Plan**? All work is discarded: the workspace, the chat, and every unmerged change.${busy}`))) return;
	} else if ((phase === 'merge' || phase === 'done') && $t.phase !== 'plan') {
		return mergeDialog(pid, tid, $t);
	}
	void cmd('moveTask', { pid, tid, phase });
}

/** The proposed commit message plus the button that actually merges. */
export function mergeDialog(pid: string, tid: string, $t: any): void {
	const $merge = A.proxy({ message: (A.peek($t, 'commitMessage') || A.peek($t, 'title') || '') as string });
	void S.dialog({ header: 'Merge this task', attrs: 'w:44rem', content: close => {
		S.form({
			submit: () => {
				close();
				void cmd('mergeTask', { pid, tid, message: $merge.message });
			},
			content: () => {
				A(() => { if ($t.working) A('div.s-s.warning.tonal p:$2 #The agent is still working; merging stops it first.'); });
				S.textarea({
					label: 'Commit message', help: 'The whole working tree becomes one commit on the main branch.',
					rows: 12, autoGrow: false, inputAttrs: 'font-family:monospace', bind: A.ref($merge, 'message'),
				});
			},
			actions: () => S.button({ content: 'Merge', type: 'submit' }),
		});
	}});
}

/**
 * Start a task: no dialog, and nothing created yet either — the draft below is
 * opened, and only becomes a task once it is assigned.
 */
export function addTask(pid: string): void {
	route.go(pathTo(pid, 'draft'));
}

/**
 * The task being written for a project: it lives in the browser only until its
 * description has something in it, at which point it is written down for real
 * (see saveDraft). It looks enough like a task for the editor and the settings
 * to work on it unchanged.
 */
const drafts = new Map<string, any>();

/** The creation a draft is in the middle of, so nothing starts a second one. */
const creating = new Map<string, Promise<string | undefined>>();

export function draftFor(pid: string): any {
	let $d = drafts.get(pid);
	if (!$d) {
		creating.delete(pid); // a fresh draft is nobody's task-in-waiting
		drafts.set(pid, $d = A.proxy({ phase: 'plan', title: '', description: '', model: 'default' }));
	}
	return $d;
}

/** What the draft would be written down as. */
function draftPatch($d: any): Record<string, unknown> {
	const patch: Record<string, unknown> = {
		description: A.peek($d, 'description') ?? '',
		model: A.peek($d, 'model'),
	};
	for (const field of ['startAfter', 'budget', 'autoMerge']) {
		const value = A.peek($d, field);
		if (value != null) patch[field] = A.unproxy(value);
	}
	return patch;
}

/**
 * Write the draft down, once there is something worth keeping — a description
 * may be a long spec, and it should not hang on the tab staying open. The task
 * takes over from there: the URL is replaced (the draft being gone, not worth
 * going back to) and the editor saves to the server from then on. Everyone who
 * asks meanwhile joins the same creation rather than starting another.
 */
function saveDraft(pid: string, $d: any): Promise<string | undefined> {
	let pending = creating.get(pid);
	if (!pending) {
		creating.set(pid, pending = (async () => {
			const sent = draftPatch($d);
			const created = await cmd('createTask', { pid, ...sent });
			if (!created) {
				creating.delete(pid); // let the next keystroke try again
				return undefined;
			}
			const tid = created.tid as string;
			// Whatever was typed or set while that was in flight follows it.
			const late = Object.entries(draftPatch($d)).filter(([k, v]) => JSON.stringify(v) !== JSON.stringify(sent[k]));
			if (late.length) void cmd('updateTask', { pid, tid, ...Object.fromEntries(late) });
			void route.go(pathTo(pid, tid), 'replace');
			drafts.delete(pid);
			return tid;
		})());
	}
	return pending;
}

/** Rename a task, from its card's context menu — the only place a title is ever typed by hand. */
export async function renameTask(pid: string, tid: string, $t: any): Promise<void> {
	const name = await S.prompt('Rename task', A.peek($t, 'title') ?? '');
	const title = name?.trim();
	if (title) void cmd('updateTask', { pid, tid, title });
}

/**
 * The models to choose from: what the claude on the project's host offers,
 * plus whatever the task is set to, so a model that host no longer lists
 * still shows.
 */
function modelOptions(pid: string, $t: any): string[] {
	const host = $state.hosts[$state.projects[pid]?.host];
	const models: string[] = [...(host?.models ?? $state.models ?? [])];
	const current = $t.model;
	if (current && !models.includes(current)) models.push(current);
	return models;
}

/**
 * Model, what the task follows, budget, merge behaviour: everything about it
 * except its phase and title. Changes go through `save`, which either tells
 * the server or fills in a draft that has yet to be created.
 */
function drawTaskFields(pid: string, tid: string | undefined, $t: any, save: (patch: object) => void): void {
	S.select({
		label: 'Model', options: () => modelOptions(pid, $t),
		bind: {
			get value() { return $t.model ?? 'default'; },
			set value(model: string) { if (model) save({ model }); },
		},
	});
	// Only while the task is in Plan: following others is how it leaves Plan,
	// so once it has, there is nothing left to set here.
	A(() => {
		if ($t.phase !== 'plan') return;
		S.autocomplete({
			label: 'Start after', multi: true, allowCustom: false,
			placeholder: 'Tasks to wait for…',
			help: 'The task hands itself to the agent once each of these is done or deleted, and nobody has its plan open — a description still being written is never sent off. Its workspace is made at that moment, so it includes their merged work.',
			// The tasks it already follows stay listed even when done, so their
			// chips read as names rather than as numbers.
			options: () => {
				const after: string[] = $t.startAfter ?? [];
				const $tasks = $state.projects[pid]?.tasks ?? {};
				return Object.keys($tasks)
					.filter(o => o !== tid && (after.includes(o) || $tasks[o].phase !== 'done'))
					.sort((a, b) => tidOrder(a) < tidOrder(b) ? -1 : 1)
					.map(o => ({ value: o, label: taskName(pid, o) }));
			},
			bind: {
				get value() { return $t.startAfter ?? []; },
				set value(after: string[]) { save({ startAfter: [...after] }); },
			},
		});
	});
	S.textline({
		label: 'Budget limit (USD)', type: 'number',
		help: 'The task is parked for you when spending reaches the limit; empty means no limit.',
		value: A.peek($t, 'budget') != null ? String(A.peek($t, 'budget')) : '',
		input: debounce(600, (e: Event) => save({ budget: (e.target as HTMLInputElement).value })),
	});
	S.checkbox({
		label: 'Merge when ready',
		help: 'Merge as soon as the agent reports the task ready, without confirming the commit message. Defaults to the project setting.',
		checked: A.peek($t, 'autoMerge') ?? !!A.peek(() => $state.projects[pid]?.autoMerge),
		change: (e: Event) => save({ autoMerge: (e.target as HTMLInputElement).checked }),
	});
}

function drawDeleteTask(pid: string, tid: string, $t: any, close?: () => void): void {
	S.button({
		content: 'Delete task', icon: trash2, attrs: '.small .danger .outlined',
		click: async () => {
			const busy = A.peek($t, 'working') ? ' The agent is still working; it is stopped.' : '';
			if (!(await S.confirm(`Delete **${A.peek($t, 'title') || 'this task'}**? This removes the task, its workspace and its container; merged work stays merged.${busy}`))) return;
			close?.();
			if (await cmd('deleteTask', { pid, tid })) route.go(pathTo(pid));
		},
	});
}

/** Task settings behind a dialog: how every phase but Plan offers them. */
export function taskSettingsDialog(pid: string, tid: string, $t: any): void {
	void S.dialog({ header: 'Task settings', attrs: 'w:36rem', content: close => {
		drawTaskFields(pid, tid, $t, patch => void cmd('updateTask', { pid, tid, ...patch }));
		A('div display:flex mt:$2', () => drawDeleteTask(pid, tid, $t, close));
	}});
}

/**
 * A task still in Plan, in the left column: its settings — laid out rather than
 * hidden behind an icon, there being nothing else to do with the space yet —
 * and the two ways out of Plan. A draft is created on its way out, not before.
 */
export function drawPlanSettings(pid: string, tid: string | undefined, $t: any): void {
	const save = tid
		? (patch: object) => void cmd('updateTask', { pid, tid, ...patch })
		: (patch: object) => Object.assign($t, patch);
	A('div display:flex flex-direction:column gap:$2 flex:1 min-height:0 overflow-y:auto', () => {
		drawTaskFields(pid, tid, $t, save);
		A(() => {
			const note = autoStartNote(pid, $t);
			if (note) A('div.s-s.warning.tonal p:$2 font-size:0.9em text=', note);
		});
		if (tid) A('div display:flex', () => drawDeleteTask(pid, tid, $t)); // a draft has nothing to delete
	});
	// There is nothing to assign until something has been written. The emptiness
	// is derived into a flag of its own, so the buttons are redrawn when it
	// flips rather than on every keystroke.
	const $ready = A.proxy({ value: false });
	A(() => { $ready.value = !!($t.description ?? '').trim(); });
	A('div display:flex gap:$2', () => {
		const off = !$ready.value;
		S.button({ content: 'Assign to agent', icon: bot, key: 'mod+enter', disabled: off, click: () => void assignTask(pid, tid, $t, 'agent') });
		S.button({ content: 'Assign to human', icon: user, attrs: '.neutral', disabled: off, click: () => void assignTask(pid, tid, $t, 'human') });
	});
}

/** What the task is waiting for, the open plan included — that holds it back too. */
function autoStartNote(pid: string, $t: any): string | undefined {
	if (!autoStarts($t)) return;
	const also = waitingFor(pid, $t).length ? ', and once you close this plan' : '';
	return `⏳ This task ${taskActivity(pid, $t).text}${also}.`;
}

/** Hand the task to the agent or a human, writing a draft down first if the typing beat the debounce to it. */
async function assignTask(pid: string, tid: string | undefined, $t: any, phase: Phase): Promise<void> {
	if (!(A.peek($t, 'description') ?? '').trim()) return;
	if (!tid && !(tid = await saveDraft(pid, $t))) return;
	// The editor's debounce may still owe the server the last keystroke, and
	// the agent is about to be handed whatever the server has.
	if (!(await cmd('updateTask', { pid, tid, description: (A.peek($t, 'description') ?? '').trim() }))) return;
	void cmd('moveTask', { pid, tid, phase });
	// Handed off to the agent, there is nothing left to do here: the board is
	// more use than watching the workspace come up in the right column.
	if (phase === 'agent') route.go(pathTo(pid));
}

/**
 * A task still in Plan, in the right column: the description, with room to
 * write it. Every keystroke lands in the task (or draft) it belongs to right
 * away, so the crumb and the Assign buttons keep up and nothing is lost to a
 * redraw; only the server is spared the chatter.
 */
export function drawPlanEditor(pid: string, tid: string | undefined, $t: any): void {
	const store = debounce(600, (description: string) => {
		if (tid) void cmd('updateTask', { pid, tid, description });
		else if (description.trim()) void saveDraft(pid, $t);
	});
	S.textarea({
		attrs: 'h:100%', inputAttrs: 'flex:1 min-height:0', autoGrow: false, resize: 'none',
		placeholder: 'What should the agent do?', value: A.peek($t, 'description') ?? '',
		input: (e: Event) => {
			const description = (e.target as HTMLTextAreaElement).value;
			$t.description = description;
			store(description);
		},
	});
}

/** The chat, what is worth acting on right now, and the input. */
export function drawAgent(pid: string, tid: string, $t: any): void {
	drawChat(pid, tid);
	A(() => {
		if ($t.phase !== 'human') return;
		if ($t.rebasing) {
			A('div.s-s.warning.tonal p:$2 display:flex align-items:center gap:$2', () => {
				A('span flex:1 #⚠ merge paused: the rebase onto the latest default branch is unfinished. Send the agent back in, or finish it in VS Code and merge again.');
				S.button({ content: 'Send in the agent', icon: bot, attrs: '.small', click: () => void cmd('moveTask', { pid, tid, phase: 'agent' }) });
			});
		} else if ($t.commitMessage) {
			A('div.s-s.success.tonal p:$2 display:flex align-items:center gap:$2', () => {
				A('span flex:1 #✔ the agent reports this task ready to merge');
				S.button({ content: 'Merge…', icon: gitMerge, attrs: '.small', click: () => mergeDialog(pid, tid, $t) });
			});
		}
	});
	A(() => {
		if (!['building', 'starting', 'stopping', 'error'].includes($t.status)) return;
		const { text, color } = taskActivity(pid, $t);
		A(`div.s-s.${color}.tonal p:$2 text=`, text);
	});
	drawInputBar(pid, tid, $t);
}

function drawInputBar(pid: string, tid: string, $t: any): void {
	const $has = A.proxy({ text: false });
	let area: HTMLTextAreaElement | undefined;
	const sendMsg = () => {
		const text = area?.value.trim();
		if (!text) return;
		area!.value = '';
		area!.dispatchEvent(new Event('input')); // shrink it back down, and drop $has.text
		void cmd('chat', { pid, tid, text });
	};
	A('div display:flex align-items:flex-end gap:$2', () => {
		S.textarea({
			placeholder: 'Message the agent…', attrs: 'flex:1', inputAttrs: 'max-height:40dvh overflow-y:auto',
			input: (e: Event) => {
				area = e.target as HTMLTextAreaElement;
				$has.text = !!area.value.trim();
			},
		});
		// One button beside the field: send while there is text, else stop while claude works.
		A(() => {
			if ($has.text) S.button({ icon: sendHorizontal, ariaLabel: 'Send', key: 'mod+enter', click: sendMsg });
			else if ($t.working) S.button({ icon: circleStop, ariaLabel: 'Stop the agent', attrs: '.danger',
				click: () => void cmd('stopAgent', { pid, tid }) });
		});
	});
}

/** The right column for a task: VS Code in its container, once that is up. */
export function drawTaskCode(pid: string, tid: string, $t: any): void {
	A(() => {
		if ($t.status === 'up') {
			// code-server's remote authority is the Host header, which the proxy passes on unchanged.
			drawCode(`${pid}/${tid}`, `/code/${pid}/${tid}/?folder=/work`);
		} else if ($t.status === 'error') {
			S.box({ contentAttrs: 'display:flex flex-direction:column align-items:flex-start', content: () => {
				A('p fg:$s-danger text=', `Workspace error: ${$t.statusDetail || 'unknown'}`);
				S.button({ content: 'Retry', click: () => void cmd('openTask', { pid, tid }) });
			}});
		} else {
			S.box({ contentAttrs: 'display:flex flex-direction:column', content: () => {
				A('p text=', `${$t.statusDetail || $t.status}…`);
				A('progress w:100%');
			}});
		}
	});
}

