import A from 'aberdeen';
import * as S from 'staffa';
import { bot, circleStop, gitMerge, listTodo, maximize2, sendHorizontal, squareCode, trash2, user, x } from 'staffa/icons.js';
import { drawChat, md } from './chat.ts';
import { $state, watchTask } from './conn.ts';
import { cmd, costText, debounce, drawLiveLink, drawTaskIcon, ELLIPSIS, PHASE_LABELS, taskActivity, taskName, tidOrder, type Phase } from './util.ts';

type TaskPanel = S.Panel<{ pid: string; tid: string }>;

/** Runs draw with the task's state once it is in (keeping its workspace up meanwhile), or says why not. */
function withTask($panel: TaskPanel, draw: ($t: any) => void): void {
	const { pid, tid } = $panel.params;
	watchTask(pid, tid);
	A(() => {
		$panel.loading = !$state.ready;
		const $t = $state.projects[pid]?.tasks?.[tid];
		if (!$t) {
			if ($state.ready) S.box({ header: 'Task not found', content: 'This task does not exist (anymore).' });
			return;
		}
		A(() => { if ($t.phase !== 'plan') void cmd('openTask', { pid, tid }); });
		draw($t);
	});
}

/**
 * The task's page: the title, then the chat and the changed files at a
 * glance, the description, what to do with it next, and its settings.
 */
export function drawTask($panel: TaskPanel): void {
	const { pid, tid } = $panel.params;
	$panel.maxWidth = 'small';
	A('display:grid align-content:start gap:$3'); // not flex: that would shrink the boxes instead of scrolling
	withTask($panel, $t => {
		A(() => { $panel.title = $t.title || 'New task'; });
		const $draft = A.proxy({ title: (A.peek($t, 'title') ?? '') as string, description: (A.peek($t, 'description') ?? '') as string });
		/** The edits not yet on the server. An empty title is never sent. */
		const changes = () => {
			const partial: Record<string, string> = {};
			const title = $draft.title.trim();
			if (title && title !== A.peek($t, 'title')) partial.title = title;
			if ($draft.description !== (A.peek($t, 'description') ?? '')) partial.description = $draft.description;
			return partial;
		};
		const flush = async () => {
			const partial = changes();
			return !Object.keys(partial).length || !!await cmd('updateTask', { pid, tid, ...partial });
		};
		const save = debounce(600, () => void flush());

		S.textline({ placeholder: 'Task title', inputAttrs: 'font-size:1.25em font-weight:600', bind: A.ref($draft, 'title'), input: save });
		drawStatus(pid, $t);
		A(() => {
			if ($t.phase === 'plan') return;
			drawChatBox(pid, tid, $t, $panel);
			drawChanges(pid, tid, $t, $panel);
		});
		A(() => {
			if ($t.phase === 'plan') S.textarea({ label: 'Description', inputAttrs: 'min-height:10rem', bind: A.ref($draft, 'description'), input: save });
			else S.box({ header: 'Description', content: () => A('div', 'html=', md.parse($t.description || '*No description.*') as string) });
		});
		A('div display:flex flex-wrap:wrap gap:$2', () => A(() => {
			for (const a of taskActions(pid, tid, $t, $panel, { flush, title: $draft.title })) {
				S.button({ content: a.label, icon: a.icon, disabled: a.disabled, click: a.click,
					attrs: `.small ${a.danger ? '.danger .outlined' : a.primary ? '' : '.neutral'}` });
			}
		}));
		S.box({ header: 'Settings', contentAttrs: 'display:flex flex-direction:column', content: () => drawSettingsFields(pid, tid, $t) });
	});
}

/** The phase, what the workspace is up to, and the cost, on one line. */
function drawStatus(pid: string, $t: any): void {
	A('div display:flex align-items:center gap:$2 fg:$s-muted font-size:0.9em', () => {
		drawTaskIcon(pid, $t);
		A(() => {
			const bits = [PHASE_LABELS[$t.phase as Phase] + ($t.working ? ', claude is working' : '')];
			if ($t.phase !== 'plan') bits.push(taskActivity(pid, $t).text);
			const cost = costText($t);
			if (cost) bits.push(cost);
			if ($t.phase === 'human' && $t.commitMessage) bits.push('ready to merge ✔');
			A('span flex:1 text=', bits.join(' · '));
		});
		drawLiveLink($t);
	});
}

interface TaskAction {
	label: string;
	icon?: S.Slot;
	click: () => void;
	danger?: boolean;
	primary?: boolean;
	disabled?: boolean;
}

/**
 * What can be done with the task from its current phase, the destructive
 * things last. `flush` pushes pending edits before an assignment, `title` is
 * the title as being edited.
 */
export function taskActions(pid: string, tid: string, $t: any, $panel: S.Panel, o: { flush?: () => Promise<boolean>; title?: string } = {}): TaskAction[] {
	const assign = async (to: string) => {
		if (o.flush && !await o.flush()) return;
		if (await cmd('assignTask', { pid, tid, to })) void $panel.open(`/p/${pid}/t/${tid}/agent`);
	};
	const move = (phase: Phase) => () => void moveTask(pid, tid, $t, phase);
	const actions: TaskAction[] = [];
	switch ($t.phase) {
		case 'plan': {
			const disabled = !(o.title ?? $t.title ?? '').trim();
			actions.push({ label: 'Assign to agent', icon: bot, primary: true, disabled, click: () => void assign('agent') });
			actions.push({ label: 'Assign to human', icon: user, disabled, click: () => void assign('human') });
			break;
		}
		case 'agent':
			actions.push({ label: $t.waiting ? 'Assign to human' : 'Hand to human', icon: user, click: move('human') });
			actions.push({ label: 'Merge…', icon: gitMerge, click: move('merge') });
			actions.push({ label: 'Back to plan', icon: listTodo, danger: true, click: move('plan') });
			break;
		case 'human':
			actions.push({ label: 'Send the agent back in', icon: bot, click: move('agent') });
			actions.push({ label: 'Merge…', icon: gitMerge, primary: !!$t.commitMessage, click: move('merge') });
			actions.push({ label: 'Back to plan', icon: listTodo, danger: true, click: move('plan') });
			break;
		case 'merge':
			actions.push({ label: 'Stop merging', icon: circleStop, click: move('human') });
			break;
	}
	actions.push({ label: 'Delete task', icon: trash2, danger: true, click: () => void deleteTask(pid, tid, $t) });
	return actions;
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

export async function deleteTask(pid: string, tid: string, $t: any): Promise<void> {
	const busy = $t.working ? ' The agent is still working; it is stopped.' : '';
	if (!(await S.confirm(`Delete **${$t.title || 'this task'}**? This removes the task, its workspace and its container; merged work stays merged.${busy}`))) return;
	void cmd('deleteTask', { pid, tid });
}

/** The chat, small, with a way to give it a panel of its own. */
function drawChatBox(pid: string, tid: string, $t: any, $panel: TaskPanel): void {
	S.box({
		header: () => {
			A('#Agent');
			S.iconButton({ icon: maximize2, ariaLabel: 'Open the chat in a panel of its own', attrs: '.small ml:auto',
				click: () => void $panel.open(`/p/${pid}/t/${tid}/agent`) });
		},
		contentAttrs: 'display:flex flex-direction:column gap:$2',
		content: () => drawAgent(pid, tid, $t, $panel, 'max-height:40dvh'),
	});
}

/** The chat in a panel of its own, filling the column. */
export function drawAgentPanel($panel: TaskPanel): void {
	const { pid, tid } = $panel.params;
	$panel.maxWidth = 'medium';
	$panel.title = 'Agent';
	A('display:flex flex-direction:column');
	withTask($panel, $t => {
		if ($t.phase === 'plan') {
			S.box({ content: 'Assign the task first; the chat starts then.' });
			return;
		}
		S.box({ attrs: 'flex:1 min-height:0', contentAttrs: 'flex:1 min-height:0 display:flex flex-direction:column gap:$2',
			content: () => drawAgent(pid, tid, $t, $panel, 'flex:1 min-height:0') });
	});
}

/** The chat log (sized by `logAttrs`), a strip for things worth knowing (or doing) right now, and the input. */
function drawAgent(pid: string, tid: string, $t: any, $panel: TaskPanel, logAttrs: string): void {
	drawChat(pid, tid, $panel, logAttrs);
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

const hoverable = A.insertCss({ '&:hover': 'fg:$s-text' });

/** The files changed so far, each opening VS Code on itself; the box as a whole opens it on the workspace. */
function drawChanges(pid: string, tid: string, $t: any, $panel: TaskPanel): void {
	const base = `/p/${pid}/t/${tid}/code`;
	S.box({ attrs: 'cursor:pointer', contentAttrs: 'display:flex gap:$3 align-items:center', content: () => {
		A('click=', () => void $panel.open(base));
		S.addTooltip({ tip: 'Open VS Code in the container' });
		squareCode({ size: '3em', attrs: 'flex-shrink:0 fg:$s-accent' });
		A('div flex:1 min-width:0 display:flex flex-direction:column font-family:monospace font-size:0.85em', () => {
			A(() => {
				const changes: any[] | undefined = $t.changes;
				if (!changes) A('span fg:$s-muted #Looking for changes…');
				else if (!changes.length) A('span fg:$s-muted #No changes yet');
				for (const c of changes ?? []) {
					A(`div display:flex gap:$2 fg:$s-muted`, hoverable,
						'click=', (e: Event) => { e.stopPropagation(); void $panel.open(`${base}/${c.p.split('/').map(encodeURIComponent).join('/')}`); },
						() => {
							A(`span flex:1 ${ELLIPSIS} text=`, c.p);
							if (c.bin) A('span #binary');
							else {
								A('span fg:$s-success text=', '+' + c.a);
								A('span fg:$s-danger text=', '−' + c.d);
							}
						});
				}
			});
		});
	}});
}

/** VS Code, served from the container, on the workspace or on one file of it. */
export function drawCodePanel($panel: S.Panel<{ pid: string; tid: string; file?: string }>): void {
	const { pid, tid, file } = $panel.params;
	const path = file?.split('/').map(decodeURIComponent).join('/');
	$panel.maxWidth = 'none';
	$panel.title = path?.split('/').pop() || 'Code';
	A('display:flex flex-direction:column');
	withTask($panel, $t => {
		A(() => {
			if ($t.status === 'up') {
				A('p:0'); // edge to edge
				// code-server's remote authority is the Host header, which the proxy passes on unchanged.
				let src = `/code/${pid}/${tid}/?folder=/work`;
				if (path) src += '&payload=' + encodeURIComponent(JSON.stringify([['openFile', `vscode-remote://${location.host}/work/${path}`]]));
				const iframe = A('iframe flex:1 w:100% border:0', { allow: 'clipboard-read; clipboard-write' }, 'src=', src) as HTMLIFrameElement;
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
}
