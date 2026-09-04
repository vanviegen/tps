import A from 'aberdeen';
import * as S from 'staffa';
import { bot, circleStop, gitMerge, sendHorizontal, user, x } from 'staffa/icons.js';
import { deleteTask, moveTask } from './board.ts';
import { drawChat } from './chat.ts';
import { $state, watchTask } from './conn.ts';
import { cmd, costText, debounce, ELLIPSIS, PHASE_LABELS, taskActivity, taskName, tidOrder, type Phase } from './util.ts';

type TaskPanel = S.Panel<{ pid: string; tid: string }>;

/**
 * One page of a task, picked in the nav: the agent chat, VS Code, or the
 * settings. In the plan phase there is only the settings page, which then
 * also holds the title and description.
 */
export function drawTask($panel: TaskPanel, page: 'agent' | 'code' | 'settings' = 'agent'): void {
	const { pid, tid } = $panel.params;
	watchTask(pid, tid);
	$panel.maxWidth = page === 'code' ? 'none' : 'medium';
	A(() => {
		$panel.loading = !$state.ready;
		const $t = $state.projects[pid]?.tasks?.[tid];
		if (!$t) {
			if ($state.ready) S.box({ header: 'Task not found', content: 'This task does not exist (anymore).' });
			return;
		}
		A(() => { $panel.title = $t.title || 'New task'; });
		A(() => { if ($t.phase !== 'plan') void cmd('openTask', { pid, tid }); });
		A(() => {
			if ($t.phase === 'plan') drawPlanSettings(pid, tid, $t, $panel);
			else if (page === 'settings') drawRunSettings(pid, tid, $t);
			else {
				// The chat and VS Code fill the column instead of scrolling it.
				A('display:flex flex-direction:column gap:$2');
				if (page === 'code') drawCode(pid, tid, $t, $panel);
				else drawAgent(pid, tid, $t, $panel);
			}
		});
	});
}

/** The chat, a strip for things worth knowing (or doing) right now, and the input. */
function drawAgent(pid: string, tid: string, $t: any, $panel: TaskPanel): void {
	drawChat(pid, tid, $panel);
	A(() => {
		if ($t.phase !== 'human' || !$t.commitMessage) return;
		A('div.s-s.success.tonal p:$2 display:flex align-items:center gap:$2', () => {
			A('span flex:1 #✔ the agent reports this task ready to merge');
			S.button({ content: 'Merge…', icon: gitMerge, attrs: '.small', click: () => mergeDialog(pid, tid, $t) });
		});
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

/** VS Code, served from the container. */
function drawCode(pid: string, tid: string, $t: any, $panel: TaskPanel): void {
	A(() => {
		if ($t.status === 'up') {
			A('p:0'); // edge to edge
			const iframe = A('iframe flex:1 w:100% border:0', { allow: 'clipboard-read; clipboard-write' },
				'src=', `/code/${pid}/${tid}/?folder=/work`) as HTMLIFrameElement;
			iframe.addEventListener('load', () => iframe.focus());
			A(() => { if ($panel.visible) setTimeout(() => iframe.focus(), 100); });
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

/** Plan phase: one box holding the whole task, with the assign buttons below it. */
function drawPlanSettings(pid: string, tid: string, $t: any, $panel: TaskPanel): void {
	const $draft = A.proxy({ title: (A.peek($t, 'title') ?? '') as string, description: (A.peek($t, 'description') ?? '') as string });
	/** The edits not yet on the server. An empty title is never sent. */
	const changes = () => {
		const partial: Record<string, string> = {};
		const title = $draft.title.trim();
		if (title && title !== A.peek($t, 'title')) partial.title = title;
		if ($draft.description !== (A.peek($t, 'description') ?? '')) partial.description = $draft.description;
		return partial;
	};
	const save = debounce(600, () => {
		const partial = changes();
		if (Object.keys(partial).length) void cmd('updateTask', { pid, tid, ...partial });
	});
	// Push unsaved edits before assigning, so the agent sees the final text; then move on to the chat.
	const assign = async (to: string) => {
		const partial = changes();
		if (Object.keys(partial).length && !await cmd('updateTask', { pid, tid, ...partial })) return;
		if (await cmd('assignTask', { pid, tid, to })) void $panel.open(`/p/${pid}/t/${tid}/agent`, 'replace');
	};
	S.box({
		header: 'Task',
		contentAttrs: 'display:flex flex-direction:column',
		content: () => {
			S.textline({ label: 'Title', required: true, bind: A.ref($draft, 'title'), input: save });
			S.textarea({ label: 'Description', inputAttrs: 'min-height:12rem', bind: A.ref($draft, 'description'), input: save });
			drawSettingsFields(pid, tid, $t);
		},
		footer: () => {
			S.button({ content: 'Delete task', attrs: '.danger .outlined .small mr:auto', click: () => void deleteTask(pid, tid, $t) });
			A(() => {
				const disabled = !$draft.title.trim();
				S.button({ content: 'Assign to human', icon: user, attrs: '.neutral', disabled, click: () => void assign('human') });
				S.button({ content: 'Assign to agent', icon: bot, disabled, click: () => void assign('agent') });
			});
		},
	});
}

/** Later phases: a box on how the run is doing, then the still tweakable settings. */
function drawRunSettings(pid: string, tid: string, $t: any): void {
	A(() => {
		S.box({
			header: 'Execution',
			content: () => A('table', () => {
				const row = (k: string, v: string, attrs = '') => A('tr', () => { A('th text=', k); A(`td ${attrs} text=`, v); });
				row('Phase', `${PHASE_LABELS[$t.phase as Phase]}${$t.working ? ', claude is working' : ''}`);
				row('Workspace', taskActivity(pid, $t).text);
				row('Spent', costText($t) ?? 'nothing yet');
				if ($t.commitMessage) row('Proposed commit', $t.commitMessage, 'white-space:pre-wrap');
			}),
			footer: $t.phase === 'merge' ? undefined : () =>
				S.button({ content: 'Merge…', icon: gitMerge, attrs: $t.commitMessage ? '.small' : '.small .outlined',
					click: () => mergeDialog(pid, tid, $t) }),
		});
	});
	S.box({
		header: 'Settings',
		contentAttrs: 'display:flex flex-direction:column',
		content: () => {
			drawSettingsFields(pid, tid, $t);
			A('small #The title and description are fixed once the conversation has started; discarding to Plan makes them editable again.');
		},
		footer: () => {
			S.button({ content: 'Delete task', attrs: '.danger .outlined .small', click: () => void deleteTask(pid, tid, $t) });
			S.button({ content: 'Discard to plan', attrs: '.danger .outlined .small', click: () => void moveTask(pid, tid, $t, 'plan') });
		},
	});
}

/** Model, dependencies, budget and merge behaviour: tweakable in every phase. */
function drawSettingsFields(pid: string, tid: string, $t: any): void {
	S.select({
		label: 'Model', options: () => $state.models,
		bind: {
			get value() { return $t.model ?? 'sonnet'; },
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
				const done = !$dep || $dep.phase === 'merge';
				A('div display:flex align-items:center gap:$1', () => {
					A(`span flex:1 ${ELLIPSIS} ${done ? 'fg:$s-muted' : ''} text=`, taskName(pid, d) + (done ? ' ✔' : ''));
					S.iconButton({ icon: x, ariaLabel: 'Remove dependency', attrs: '.small',
						click: () => void cmd('updateTask', { pid, tid, dependencies: deps.filter(o => o !== d) }) });
				});
			}
			const options = Object.keys($tasks)
				.filter(o => o !== tid && !deps.includes(o) && $tasks[o].phase !== 'merge')
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
}

/** The proposed commit message plus the button that actually merges. */
function mergeDialog(pid: string, tid: string, $t: any): void {
	const $merge = A.proxy({ message: (A.peek($t, 'commitMessage') || A.peek($t, 'title') || '') as string });
	void S.dialog({ header: 'Merge this task', attrs: 'w:44rem', content: close => {
		S.form({
			submit: () => {
				close();
				void cmd('mergeTask', { pid, tid, message: $merge.message });
			},
			content: () => S.textarea({
				label: 'Commit message', help: 'The whole working tree becomes one commit on the main branch.',
				rows: 12, autoGrow: false, inputAttrs: 'font-family:monospace', bind: A.ref($merge, 'message'),
			}),
			actions: () => S.button({ content: 'Merge', icon: gitMerge, type: 'submit' }),
		});
	}});
}
