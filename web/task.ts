import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { bot, circleStop, gitMerge, sendHorizontal, trash2, user, x } from 'staffa/icons.js';
import { drawChat } from './chat.ts';
import { drawCode } from './code.ts';
import { $state, watchTask } from './conn.ts';
import { cmd, debounce, drawBadge, drawTaskIcon, ELLIPSIS, pathTo, PHASES, PHASE_ICONS, PHASE_LABELS, taskActivity, taskName, tidOrder, type Phase } from './util.ts';

/**
 * Tasks of a project: the ones waiting for a human first, then the newest
 * first — a task keeps no activity of its own, so its id stands in for it.
 */
export function sortedTasks(pid: string): [string, any][] {
	const tasks = Object.entries($state.projects[pid]?.tasks ?? {}) as [string, any][];
	return tasks.sort((a, b) =>
		(b[1].phase === 'human' ? 1 : 0) - (a[1].phase === 'human' ? 1 : 0) ||
		(tidOrder(a[0]) < tidOrder(b[0]) ? 1 : -1));
}

/** Tasks of a project waiting for a human, not counting `skip`. */
export function humanTasks(pid: string, skip?: string): number {
	const tasks = Object.entries($state.projects[pid]?.tasks ?? {}) as [string, any][];
	return tasks.filter(([tid, $t]) => tid !== skip && $t.phase === 'human').length;
}

/** One row of the task selector. */
export function taskItem(pid: string, tid: string, $t: any): S.MenuItem {
	return {
		href: pathTo(pid, tid),
		label: () => {
			drawTaskIcon(pid, $t);
			A(`span flex:1 ${ELLIPSIS}`, () => A('text=', $t.title || '(untitled)'));
			A(() => drawBadge($t.phase === 'human' ? 1 : 0));
		},
	};
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

/** A title and a description, and the task is in Plan. */
export function newTaskDialog(pid: string): void {
	const $form = A.proxy({ title: '', description: '' });
	void S.dialog({ header: 'Add task', attrs: 'w:44rem', content: close => {
		S.form({
			submit: async () => {
				const title = $form.title.trim();
				if (!title) return;
				const result = await cmd('createTask', { pid });
				if (!result) return;
				close();
				await cmd('updateTask', { pid, tid: result.tid, title, description: $form.description });
				route.go(pathTo(pid, result.tid));
			},
			content: () => {
				S.textline({ label: 'Title', required: true, bind: A.ref($form, 'title') });
				S.textarea({ label: 'Description', help: 'Markdown. It is fixed once the task leaves Plan.', rows: 8, autoGrow: false, bind: A.ref($form, 'description') });
			},
			actions: () => S.button({ content: 'Add task', type: 'submit' }),
		});
	}});
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

/** Everything about the task that isn't its phase: title, model, dependencies, budget, merge behaviour. */
export function taskSettingsDialog(pid: string, tid: string, $t: any): void {
	void S.dialog({ header: 'Task settings', attrs: 'w:36rem', content: close => {
		S.textline({
			label: 'Title', value: A.peek($t, 'title') ?? '',
			input: debounce(600, (e: Event) => {
				const title = (e.target as HTMLInputElement).value.trim();
				if (title) void cmd('updateTask', { pid, tid, title });
			}),
		});
		S.select({
			label: 'Model', options: () => modelOptions(pid, $t),
			bind: {
				get value() { return $t.model ?? 'default'; },
				set value(model: string) { if (model) void cmd('updateTask', { pid, tid, model }); },
			},
		});
		// Laid out like a staffa field, as the list of dependencies is not a control of its own.
		A('div.s-field', () => {
			A('label #Dependencies');
			A(() => {
				const deps: string[] = $t.dependencies ?? [];
				const $tasks = $state.projects[pid]?.tasks ?? {};
				for (const d of deps) {
					const $dep = $tasks[d];
					const done = !$dep || $dep.phase === 'done';
					A('div display:flex align-items:center gap:$1', () => {
						A(`span flex:1 ${ELLIPSIS} ${done ? 'fg:$s-muted' : ''} text=`, taskName(pid, d) + (done ? ' ✔' : ''));
						S.iconButton({ icon: x, ariaLabel: 'Remove dependency', attrs: '.small',
							click: () => void cmd('updateTask', { pid, tid, dependencies: deps.filter(o => o !== d) }) });
					});
				}
				const options = Object.keys($tasks)
					.filter(o => o !== tid && !deps.includes(o) && $tasks[o].phase !== 'done')
					.sort((a, b) => tidOrder(a) < tidOrder(b) ? -1 : 1)
					.map(o => ({ value: o, label: taskName(pid, o) }));
				if (options.length) S.select({
					placeholder: 'Add a task this one depends on…', options,
					bind: {
						get value() { return ''; },
						set value(d: string) { if (d) void cmd('updateTask', { pid, tid, dependencies: [...deps, d] }); },
					},
				});
				else if (!deps.length) A('div.s-help #No other open tasks in this project.');
			});
			A('div.s-help #The agent only starts once each of these is merged or deleted; the workspace then includes their work.');
		});
		S.textline({
			label: 'Budget limit (USD)', type: 'number',
			help: 'The task is parked for you when spending reaches the limit; empty means no limit.',
			value: A.peek($t, 'budget') != null ? String(A.peek($t, 'budget')) : '',
			input: debounce(600, (e: Event) =>
				void cmd('updateTask', { pid, tid, budget: (e.target as HTMLInputElement).value })),
		});
		S.checkbox({
			label: 'Merge when ready',
			help: 'Merge as soon as the agent reports the task ready, without confirming the commit message. Defaults to the project setting.',
			checked: A.peek($t, 'autoMerge') ?? !!A.peek(() => $state.projects[pid]?.autoMerge),
			change: (e: Event) => void cmd('updateTask', { pid, tid, autoMerge: (e.target as HTMLInputElement).checked }),
		});
		A('div display:flex mt:$2', () => S.button({
			content: 'Delete task', icon: trash2, attrs: '.small .danger .outlined',
			click: async () => {
				const busy = A.peek($t, 'working') ? ' The agent is still working; it is stopped.' : '';
				if (!(await S.confirm(`Delete **${A.peek($t, 'title') || 'this task'}**? This removes the task, its workspace and its container; merged work stays merged.${busy}`))) return;
				close();
				if (await cmd('deleteTask', { pid, tid })) route.go(pathTo(pid));
			},
		}));
	}});
}

/** A task still in Plan: the description, and the two ways out of it. */
export function drawPlan(pid: string, tid: string, $t: any): void {
	const $draft = A.proxy({ text: (A.peek($t, 'description') ?? '') as string });
	const store = () => cmd('updateTask', { pid, tid, description: $draft.text });
	S.textarea({
		attrs: 'flex:1 min-height:0', inputAttrs: 'flex:1', autoGrow: false, resize: 'none',
		placeholder: 'What should the agent do?', bind: A.ref($draft, 'text'), input: debounce(600, () => void store()),
	});
	const assign = async (phase: Phase) => {
		if (await store()) void cmd('moveTask', { pid, tid, phase });
	};
	A('div display:flex gap:$2', () => {
		S.button({ content: 'Assign to agent', icon: bot, attrs: '.small', click: () => void assign('agent') });
		S.button({ content: 'Assign to human', icon: user, attrs: '.small .neutral', click: () => void assign('human') });
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
		if (!$t.waiting && !['building', 'starting', 'stopping', 'error'].includes($t.status)) return;
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

