import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { arrowDownToLine, check, circleSlash, circleStop, folderGit2, gitMerge, menu, plus, refreshCw, scanEye, sendHorizontal, settings, trash2, user } from 'staffa/icons.js';
import { acceptFiles, addFiles, attachButton, attachments, dropAttachment, drawAttachments, drawRefs, removeRef, takeAttachments, uploadFiles, uploadPath } from './attach.ts';
import { bot } from './bot.ts';
import { drawChat } from './chat.ts';
import { drawCode } from './code.ts';
import { $state, watch } from './conn.ts';
import { applyNotifyDefault, notifies, notifiesByDefault, toggleDefaultNotifies, toggleNotifies } from './notify.ts';
import { sortedProjects } from './projects.ts';
import { drawPortButtons, drawServiceButtons, hasServices, portItems, serviceItems } from './services.ts';
import { autoStarts, canMerge, chatDraft, cmd, contextSlices, debounce, drawContextRing, drawContextTip, drawStrip, ELLIPSIS, hasWorkspace, hostName, isFinished, onComposer, pathTo, projectName, restoreDraft, selection, setChatDraft, PHASE_ICONS, PHASE_LABELS, PHASES, taskActivity, taskBusy, taskName, taskTitle, waitingFor, waitsForHuman, type Phase } from './util.ts';

/**
 * Watch the task for as long as the calling scope lives: its chat streams, and
 * once it has a workspace, VS Code runs in it.
 *
 * Opening a muted task is how it stops being muted: looking at it is taking it
 * back on, and a task you are working on belongs in the sidebar with the rest.
 */
export function useTask(pid: string, tid: string, $t: any): void {
	watch(pid, tid);
	A(() => { if ($t.phase === 'muted') void cmd('moveTask', { pid, tid, phase: 'human' }); });
	// Closing the page of a task that was never written down throws it away.
	// This scope is also torn down and built up again while the page stays put
	// (the task arriving anew from the server does that), so what is checked
	// for is the page really being gone: something else is showing by then.
	A.clean(() => { if (A.peek(selection).tid !== tid) dropBlankTask(pid, tid); });
}

/**
 * Delete a task nothing was ever said about: still in Plan, its description
 * empty and no title of its own — all that Start left behind (see addTask).
 * One that says anything at all, however little, stays.
 */
function dropBlankTask(pid: string, tid: string): void {
	const blank = A.peek(() => {
		const $t = $state.projects[pid]?.tasks?.[tid];
		return !!$t && $t.phase === 'plan' && !($t.description ?? '').trim() && !($t.title ?? '').trim();
	});
	if (blank) void cmd('deleteTask', { pid, tid });
}

/**
 * The phases the task can be moved to; the one it is in is the disabled one.
 * The three ways a task ends are one entry — Done asks which of them is meant
 * (see doneDialog) — and putting a task away (Started) is one of the answers
 * Plan asks for (see planDialog).
 */
function phaseItems(pid: string, tid: string, $t: any): S.MenuEntry[] {
	const items: S.MenuEntry[] = (['plan', 'agent', 'review', 'human'] as Phase[]).map(phase => ({
		label: PHASE_LABELS[phase],
		icon: PHASE_ICONS[phase],
		// There is nothing to review until there is a workspace to review it in.
		disabled: phase === $t.phase || (phase === 'review' && !hasWorkspace($t)),
		attrs: phase === 'plan' ? 'fg:$s-danger' : '',
		click: () => void moveTask(pid, tid, $t, phase),
	}));
	items.push({
		label: 'Done…', icon: check,
		disabled: isFinished($t) || $t.phase === 'merge',
		click: () => void moveTask(pid, tid, $t, 'done'),
	});
	return items;
}

/**
 * The task's whole menu: where it can go, and how it ends — plus what is not
 * a phase but belongs with them: starting a task that follows this one, and
 * throwing it away. `extra` slots in between, for the callers that have more
 * to offer.
 */
export function taskMenuItems(pid: string, tid: string, $t: any, extra: S.MenuEntry[] = []): S.MenuEntry[] {
	return [
		...phaseItems(pid, tid, $t),
		{ separator: true },
		{ label: 'Create dependent task', icon: plus, disabled: isFinished($t), click: () => void addTask(pid, { startAfter: [tid] }) },
		...extra,
		{ label: 'Delete…', icon: trash2, attrs: 'fg:$s-danger', click: () => void deleteTask(pid, tid, $t) },
	];
}

/**
 * Move a task to a phase, confirming when that discards work or overtakes a
 * wait, and asking how it should end when it ends. Every way a task is put
 * somewhere by hand goes through here, so the questions are asked once and
 * for all of them; the answer says whether the move was made, for callers
 * with something to do after it.
 */
export async function moveTask(pid: string, tid: string, $t: any, phase: string): Promise<boolean> {
	if (phase === 'plan' && $t.phase !== 'plan') {
		const meant = await planDialog($t);
		if (!meant) return false;
		phase = meant;
	}
	if (phase === $t.phase) return false;
	if (phase === 'done' || phase === 'closed' || phase === 'merge') {
		if ($t.phase === 'merge' || isFinished($t)) return false; // in Merging it is already on its way there
		// Nothing changed since the task branched off: nothing to merge or to
		// keep, so it simply ends, without asking. Only a count the daemon made
		// says so. It is merged, which commits nothing, unless asked to end
		// unmerged or it is still in Plan. A task that cannot be merged — no
		// workspace yet, or a scratch task — has no choice to ask about: it is
		// closed.
		const mergeable = hasWorkspace($t) && !$state.projects[pid]?.scratch;
		if (mergeable && $t.phase !== 'plan' && $t.changes?.length !== 0) {
			doneDialog(pid, tid, $t);
			return false;
		}
		finish(pid, tid, !mergeable || phase === 'closed' || $t.phase === 'plan' ? 'closed' : 'done');
		return true;
	}
	if (phase === 'agent' && !(await confirmOvertake(pid, $t))) return false;
	void cmd('moveTask', { pid, tid, phase });
	return true;
}

/**
 * Where a task sent back to Plan leaves its work: discarded, or kept, the task
 * waiting at the top of the Plan column as Started until it is taken up again.
 * Resolves to the phase meant, or to nothing when the task is to stay where it
 * is.
 */
async function planDialog($t: any): Promise<Phase | undefined> {
	let meant: Phase | undefined;
	await S.dialog({ header: 'Move this task back to Plan', attrs: 'w:34rem', content: close => {
		const pick = (phase: Phase) => { meant = phase; close(); };
		A('p mt:0 #Keep the work and the conversation, so you can pick up the task later.');
		A('p #Or discard all of it: the workspace, the conversation and any unmerged changes.');
		if ($t.working) A('p #The agent is still working. Either way, it is stopped.');
		A('div display:flex gap:$2 justify-content:flex-end', () => {
			S.button({ content: 'Cancel', attrs: '.neutral fg:$s-muted', click: close });
			S.button({ content: 'Discard', icon: trash2, attrs: '.danger .outlined', click: () => pick('plan') });
			S.button({ content: 'Keep', icon: PHASE_ICONS.muted, click: () => pick('muted') });
		});
	}});
	return meant;
}

/**
 * End the task, merged or not. Whoever was looking at it lands on the
 * project's board.
 */
function finish(pid: string, tid: string, phase: 'done' | 'closed'): void {
	void cmd('moveTask', { pid, tid, phase });
	if (A.peek(selection).tid === tid) void route.go(pathTo(pid));
}

/**
 * A task that follows others, handed to the agent by hand: it was going to
 * wait for them and start in a workspace with their merged work in it, so
 * getting ahead of that is worth a question. Once they are all done (or gone)
 * there is nothing to overtake and nothing is asked — closing the plan would
 * have sent it off anyway.
 */
async function confirmOvertake(pid: string, $t: any): Promise<boolean> {
	const pending = A.peek(() => autoStarts($t) ? waitingFor(pid, $t) : []);
	if (!pending.length) return true;
	const names = A.peek(() => pending.map(d => `"${taskName(pid, d)}"`));
	const list = names.length > 1 ? `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}` : names[0];
	return S.confirm(`This task is set to start after ${list}, ${names.length > 1 ? 'which are not done yet' : 'which is not done yet'}. `
		+ `Send it to the agent now? Its workspace is made at once, off the branch as it stands, so it will not have their work in it.`);
}

/**
 * Bring the workspace onto the latest main branch, without merging anything
 * into it. It is the front half of a merge, and goes the same way: the branch
 * is merged into the task's steps, and the result left uncommitted on top of
 * it — with what does not merge cleanly sent to the agent to resolve.
 */
async function rebaseTask(pid: string, tid: string, $t: any): Promise<void> {
	const branch = A.peek(() => $state.projects[pid]?.defaultBranch) ?? 'main';
	const busy = A.peek($t, 'working') ? ' The agent is still working; it is stopped first.' : '';
	if (!(await S.confirm(`Put this task's work onto the latest ${branch}? ${branch} is merged into the work — uncommitted `
		+ `changes and untracked files alike — so nothing is lost. What does not merge cleanly is left in the files with `
		+ `conflict markers, and the agent is sent in to resolve them.${busy}`))) return;
	void cmd('rebaseTask', { pid, tid });
}

/**
 * How a mergeable task ends, as one dialog with a tab per answer: merged onto
 * the branch — the usual one, and the one it opens on — or put away without
 * merging, its work kept off the branch. Which of the two is meant is worth
 * being sure of.
 */
export function doneDialog(pid: string, tid: string, $t: any): void {
	const branch = A.peek(() => $state.projects[pid]?.defaultBranch) ?? 'main';
	const $merge = A.proxy({ message: (A.peek($t, 'commitMessage') || A.peek($t, 'title') || '') as string });
	void S.dialog({ header: 'Finish this task', attrs: 'w:44rem', content: close => {
		const drawMerge = (): void => {
			S.form({
				submit: () => {
					close();
					// The merge restarts a lot of machinery; the board is the nicer place to wait it out.
					if (A.peek(selection).tid === tid) void route.go(pathTo(pid));
					void cmd('mergeTask', { pid, tid, message: $merge.message });
				},
				content: () => {
					A(() => { if ($t.working) A('div.s-s.warning.tonal p:$2 #The agent is still working; merging stops it first.'); });
					S.textarea({
						label: 'Commit message', help: `The whole working tree becomes one commit on ${branch}, and the task's workspace is cleaned up.`,
						rows: 12, autoGrow: false, inputAttrs: 'font-family:monospace', bind: A.ref($merge, 'message'),
					});
				},
				actions: () => S.button({ content: 'Merge', icon: gitMerge, type: 'submit' }),
			});
		};
		const drawDontMerge = (): void => {
			A('p mt:0 rich=', `The task ends as it stands: its work is kept, off ${branch}, and its workspace is removed. Nothing else will see the work — but for as long as the task is not deleted it can be picked up again, which puts the work back as it was, where it can still be merged.`);
			A('div display:flex gap:$2 justify-content:flex-end', () => {
				S.button({ content: 'Finish without merging', icon: circleSlash, attrs: '.danger .outlined', click: () => { close(); finish(pid, tid, 'closed'); } });
			});
		};
		S.tabs({ contentAttrs: 'pt:$3', tabs: [
			{ id: 'merge', label: `Merge into ${branch}`, icon: gitMerge, content: drawMerge },
			{ id: 'closed', label: 'Don’t merge', icon: circleSlash, content: drawDontMerge },
		]});
	}});
}

/**
 * Start a task: no dialog, and no half-existing state either — the task is
 * created right away, in Plan and without a description, so the board and the
 * sidebar have it from the first moment. It goes by "New" until its
 * description's first line names it (see taskTitle). `partial` sets what
 * differs from the project's defaults, such as the tasks it starts after.
 */
export async function addTask(pid: string, partial: object = {}): Promise<void> {
	// The server fills in the project's defaults. All but one, that is: ready
	// notifications are this browser's, so its own default is applied here
	// (see notify.ts).
	const created = await cmd('createTask', { pid, ...partial });
	if (!created) return;
	applyNotifyDefault(pid, created.tid as string);
	route.go(pathTo(pid, created.tid as string));
}

/**
 * Move a task in Plan to another project: its description is written down
 * there as a new task, and this one deleted. The rest it gets from the new
 * project's defaults — the tasks it followed are not that project's to follow.
 */
async function changeProject(pid: string, tid: string, $t: any, to: string): Promise<void> {
	if (to === pid) return;
	const created = await cmd('createTask', { pid: to, description: A.peek($t, 'description') ?? '' });
	if (!created) return;
	applyNotifyDefault(to, created.tid as string);
	await cmd('deleteTask', { pid, tid });
	route.go(pathTo(to, created.tid as string));
}

/** What the review model reads as when the task has none of its own. */
const SAME_MODEL = 'Same as agent model';

/**
 * A model as a task's settings hold it, "claude: fable", with a name that
 * points at no provider read as claude's, as the daemon does (see splitModel).
 */
function fullModel(name: string): string {
	return name.includes(': ') ? name : 'claude: ' + name;
}

/**
 * The models to choose from for one of the task's two model settings: what the
 * agents on the project's host offer, plus whatever the task is set to, so a
 * model that host no longer lists still shows. Each is labeled by its name and
 * then the provider it belongs to, "fable (claude)", and so ordered.
 */
function modelOptions(pid: string, $t: any, field = 'model'): { value: string; label: string }[] {
	const host = $state.hosts[$state.projects[pid]?.host];
	const models = new Set<string>(host?.models ?? $state.models ?? []);
	if ($t[field]) models.add(fullModel($t[field]));
	return [...models]
		.map(value => {
			const at = value.indexOf(': ');
			return { value, label: `${value.slice(at + 2)} (${value.slice(0, at)})` };
		})
		.sort((a, b) => a.label.localeCompare(b.label));
}

/** Why models are missing from the list, where one of the agents could not be asked. */
function modelsError(pid: string): string {
	const hid = $state.projects[pid]?.host;
	const host = $state.hosts[hid];
	if (!host?.modelsError) return '';
	return `on ${hostName(hid)}, ${host.modelsError} — so its models are missing here`;
}

/**
 * Model, what the task follows, budget, merge behaviour: everything about a
 * task except its phase and title. Changes go through `save`, which either
 * tells the server or (drawn on a project's defaults, which have no phase and
 * so no `Start after`) sets what the next task there starts with.
 */
export function drawTaskFields(pid: string, tid: string | undefined, $t: any, save: (patch: object) => void): void {
	// The two models stand together: a task is often worth a different mind from
	// the one that reads its work over. Under either, why an agent's models are
	// missing from the list, where they are.
	A(() => S.select({
		label: 'Agent model', options: () => modelOptions(pid, $t), help: modelsError(pid) || undefined,
		bind: {
			get value() { return fullModel($t.model || 'default'); },
			set value(model: string) { if (model) save({ model }); },
		},
	}));
	// The review model's first option is no model at all: the reviewer then runs
	// on whatever the agent does, which is what a task starts out with. It is
	// stored as no model, and set in italics apart from the ones that name one.
	A(() => S.select({
		label: 'Review model', options: () => [SAME_MODEL, ...modelOptions(pid, $t, 'reviewModel')],
		help: modelsError(pid) || undefined,
		inputAttrs: $t.reviewModel ? undefined : 'font-style:italic',
		bind: {
			get value() { return $t.reviewModel ? fullModel($t.reviewModel) : SAME_MODEL; },
			set value(model: string) { if (model) save({ reviewModel: model === SAME_MODEL ? '' : model }); },
		},
	}));
	// Only while the task is in Plan: following others is how it leaves Plan,
	// so once it has, there is nothing left to set here.
	A(() => {
		if ($t.phase !== 'plan') return;
		S.autocomplete({
			label: 'Start after', multi: true, allowCustom: false,
			placeholder: 'Tasks to wait for…',
			help: 'The task starts once these are all done or deleted and its plan is closed; its workspace is made then, holding their merged work.',
			// The tasks it already follows stay listed even when done, so their
			// chips read as names rather than as numbers. By phase, as the board
			// has them, then by name.
			options: () => {
				const after: string[] = $t.startAfter ?? [];
				const self = tid;
				const $tasks = $state.projects[pid]?.tasks ?? {};
				return Object.keys($tasks)
					.filter(o => o !== self && (after.includes(o) || !isFinished($tasks[o])))
					.map(o => ({ value: o, label: taskName(pid, o), phase: PHASES.indexOf($tasks[o].phase) }))
					.sort((a, b) => a.phase - b.phase || a.label.localeCompare(b.label));
			},
			bind: {
				get value() { return $t.startAfter ?? []; },
				set value(after: string[]) { save({ startAfter: [...after] }); },
			},
		});
	});
	S.textline({
		label: 'Task budget limit (USD)', type: 'number', placeholder: 'No limit',
		help: 'The task is parked for you when spending reaches it.',
		value: A.peek($t, 'budget') != null ? String(A.peek($t, 'budget')) : '',
		input: debounce(600, (e: Event) => save({ budget: (e.target as HTMLInputElement).value })),
	});
	// Only where the host can make OpenRouter keys at all. The flag is derived
	// on its own, so the host's state changing does not redraw the field typed in.
	const $or = A.proxy({ on: false });
	A(() => { $or.on = !!$state.hosts[$state.projects[pid]?.host]?.openrouter; });
	A(() => {
		if (!$or.on) return;
		const granted = A.peek($t, 'openrouterGranted');
		S.textline({
			label: 'OpenRouter budget limit (USD)', type: 'number', placeholder: 'None',
			help: 'Separate from the task budget: what the agent may be handed in OpenRouter API keys without asking you.' + (granted ? ` $${granted.toFixed(2)} handed out so far.` : ''),
			value: A.peek($t, 'openrouterBudget') != null ? String(A.peek($t, 'openrouterBudget')) : '',
			input: debounce(600, (e: Event) => save({ openrouterBudget: (e.target as HTMLInputElement).value })),
		});
	});
	drawReadyFields($t, save, !!$state.projects[pid]?.scratch);
	// The odd one out: this setting is not the task's but this browser's, so it
	// goes nowhere near `save` (see notify.ts). Switching it on can be refused —
	// the browser may not allow notifications — and the box then says so by
	// going back to where it was.
	S.checkbox({
		label: 'Ready notifications',
		help: tid
			? 'A desktop notification when the task comes back to you. This browser only.'
			: 'On for the tasks made here from now on. This browser only.',
		checked: A.peek(() => tid ? notifies(pid, tid) : notifiesByDefault(pid)),
		change: async (e: Event) => {
			await (tid ? toggleNotifies(pid, tid) : toggleDefaultNotifies(pid));
			(e.target as HTMLInputElement).checked = A.peek(() => tid ? notifies(pid, tid) : notifiesByDefault(pid));
		},
	});
}

const BACK_TIP = 'The agent works through what the review asked for, and the work is reviewed again. What the last review still asks for comes to you, in the task’s message box.';

/**
 * What becomes of finished work: one question per moment where it can go more
 * than one way, each answered on its own, and each with the answer a task that
 * says nothing follows already filled in (the daemon's defaults, repeated
 * here). The two about the review are asked even where no review is set to
 * happen, because a review asked for by hand (the board's Review column) ends
 * the same way and follows the same answers. A scratch task has nothing to
 * merge into, so merging is no answer there.
 */
function drawReadyFields($t: any, save: (patch: object) => void, scratch: boolean): void {
	drawChoiceField('On agent ready', {
		human: { icon: user, title: 'Assign it to me', tip: 'The task comes to you, with its work to look over yourself.' },
		review: { icon: scanEye, title: 'Have it reviewed', tip: 'A second agent reads the work over against what you asked for, and above all for size: what can be left out, and what the project already does elsewhere. It fixes the small and obvious itself, and either accepts the work or lists what to change.' },
		...scratch ? {} : { merge: { icon: gitMerge, title: 'Merge it', tip: 'The work is committed onto the project’s branch as it stands.' } },
	}, {
		get value() { return $t.onReady ?? 'review'; },
		set value(onReady: string) { if (onReady) save({ onReady }); },
	});
	drawChoiceField('On review feedback', {
		'0': { icon: user, title: 'Assign it to me', tip: 'What the review asks for comes to you, in the task’s message box.' },
		'1': { text: '1×', icon: bot, title: 'Back to the agent, once', tip: BACK_TIP },
		'2': { text: '2×', icon: bot, title: 'Back to the agent, twice', tip: BACK_TIP },
		'3': { text: '3×', icon: bot, title: 'Back to the agent, three times', tip: BACK_TIP },
		'10': { text: '10×', icon: bot, title: 'Back to the agent, ten times', tip: BACK_TIP },
	}, {
		get value() { return String($t.reviewLoops ?? 1); },
		set value(reviewLoops: string) { if (reviewLoops) save({ reviewLoops }); },
	});
	drawChoiceField('On review accept', {
		human: { icon: user, title: 'Assign it to me', tip: 'The task comes to you, reviewed and ready to merge.' },
		...scratch ? {} : { merge: { icon: gitMerge, title: 'Merge it', tip: 'The work is committed onto the project’s branch as soon as a review accepts it.' } },
	}, {
		get value() { return $t.onAccept ?? 'human'; },
		set value(onAccept: string) { if (onAccept) save({ onAccept }); },
	});
}

/**
 * One of the settings above: its choices side by side, answered in a single
 * click. Each says what it is in its tooltip — a name, and under it what it
 * comes down to — so the whole question takes one line.
 */
function drawChoiceField(label: string, choices: Record<string, { icon: typeof bot; text?: string; title: string; tip: string }>, bind: { value: string }): void {
	A('div.s-field', () => {
		A('label text=', label);
		S.buttonChooser({
			attrs: 'align-self:flex-start',
			options: Object.fromEntries(Object.entries(choices).map(([value, choice]) => [value, () => {
				S.addTooltip({ tip: () => {
					A('div text=', choice.title);
					A('div font-size:0.85em fg:$s-muted text=', choice.tip);
				} });
				if (choice.text) A('text=', choice.text);
				choice.icon();
			}])),
			bind,
		});
	});
}

/**
 * Throw a task away, after asking: everything it did that was never merged
 * goes with it. Whoever is showing it falls back to the board; the board
 * itself just loses a card.
 */
export async function deleteTask(pid: string, tid: string, $t: any): Promise<void> {
	const busy = A.peek($t, 'working') ? ' The agent is still working; it is stopped.' : '';
	const title = A.peek($t, 'title');
	if (!(await S.confirm(`Delete ${title ? `"${title}"` : 'this task'}? This removes the task, its workspace and its container; merged work stays merged.${busy}`))) return;
	if (!(await cmd('deleteTask', { pid, tid }))) return;
	if (A.peek(selection).tid === tid) void route.go(pathTo(pid));
}

/**
 * Task settings behind a dialog: how every phase but Plan offers them. The
 * title leads, this being the only place one is ever typed by hand — claude
 * writes the first one as the task leaves Plan, and emptying the field hands
 * that job back to the description's first line.
 */
export function taskSettingsDialog(pid: string, tid: string, $t: any): void {
	const save = (patch: object) => void cmd('updateTask', { pid, tid, ...patch });
	void S.dialog({ header: 'Task settings', attrs: 'w:36rem', content: () => {
		S.textline({
			label: 'Title',
			value: A.peek($t, 'title') ?? '',
			input: debounce(600, (e: Event) => save({ title: (e.target as HTMLInputElement).value.trim() })),
		});
		drawTaskFields(pid, tid, $t, save);
	}});
}

/**
 * A task still in Plan, in the left column: its settings — laid out rather than
 * hidden behind an icon, there being nothing else to do with the space yet —
 * and the two ways out of Plan.
 */
export function drawPlanSettings(pid: string, tid: string, $t: any): void {
	const save = (patch: object) => void cmd('updateTask', { pid, tid, ...patch });
	A('div display:flex flex-direction:column gap:$2 flex:1 min-height:0 overflow-y:auto', () => {
		A(() => S.select({
			label: 'Project', options: () => sortedProjects().map(([p, $p]) => ({ value: p, label: projectName($p) })),
			bind: {
				get value() { return pid; },
				set value(to: string) { void changeProject(pid, tid, $t, to); },
			},
		}));
		drawTaskFields(pid, tid, $t, save);
		A(() => {
			const note = autoStartNote(pid, $t);
			if (note) A('div.s-s.warning.tonal p:$2 font-size:0.9em text=', note);
		});
	});
	// There is nothing to assign until something has been written. The emptiness
	// is derived into a flag of its own, so the buttons are redrawn when it
	// flips rather than on every keystroke.
	const $ready = A.proxy({ value: false });
	A(() => { $ready.value = !!($t.description ?? '').trim(); });
	A('div display:flex gap:$2 justify-content:flex-end', () => {
		const off = !$ready.value;
		S.button({ content: 'Assign to human', icon: user, attrs: '.neutral', disabled: off, click: () => void assignTask(pid, tid, $t, 'human') });
		S.button({ content: 'Assign to agent', icon: bot, key: 'mod+enter', disabled: off, click: () => void assignTask(pid, tid, $t, 'agent') });
	});
}

/** What the task is waiting for, the open plan included — that holds it back too. */
function autoStartNote(pid: string, $t: any): string | undefined {
	if (!autoStarts($t)) return;
	const also = waitingFor(pid, $t).length ? ', and once you close this plan' : '';
	return `⏳ This task ${taskActivity(pid, $t).text}${also}.`;
}

/** Hand the task to the agent or a human, flushing what the editor still owes the server. */
async function assignTask(pid: string, tid: string, $t: any, phase: Phase): Promise<void> {
	if (!(A.peek($t, 'description') ?? '').trim()) return;
	// The editor's debounce may still owe the server the last keystroke, and
	// the agent is about to be handed whatever the server has.
	if (!(await cmd('updateTask', { pid, tid, description: (A.peek($t, 'description') ?? '').trim() }))) return;
	if (!(await moveTask(pid, tid, $t, phase))) return;
	// Handed off to the agent, there is nothing left to do here: the board is
	// more use than watching the workspace come up in the right column. Taken
	// on yourself, the task's own page is, and that is where you already are.
	if (phase === 'agent') route.go(pathTo(pid));
}

/**
 * A task still in Plan, in the right column: the description, with room to
 * write it. Every keystroke lands in the task right away, so its title, the
 * board and the Assign buttons keep up; only the server is spared the chatter.
 */
export function drawPlanEditor(pid: string, tid: string, $t: any): void {
	const store = debounce(600, (description: string) => void cmd('updateTask', { pid, tid, description }));
	const area = () => box.querySelector('textarea') as HTMLTextAreaElement;
	// A file dropped, pasted or picked here is stored with the task at once,
	// the description being written long before it is handed over.
	const take = (files: File[]) => uploadFiles(pid, tid, files);
	const box = A('div display:flex flex-direction:column gap:$2 h:100%', () => {
		S.textarea({
			attrs: 'flex:1 min-height:0', inputAttrs: 'flex:1 min-height:0', autoGrow: false, resize: 'none',
			placeholder: 'What should the agent do?', value: A.peek($t, 'description') ?? '',
			suffix: () => attachButton(area, take),
			input: (e: Event) => {
				const description = (e.target as HTMLTextAreaElement).value;
				$t.description = description;
				store(description);
			},
		});
		// Under the field, what the description points at: the strip takes the
		// room it needs from the field, which has the rest.
		drawRefs(pid, tid, () => $t.description ?? '', ref => removeRef(area(), ref));
	}) as HTMLElement;
	acceptFiles(box, area, take);
	// A task with nothing written down yet has nothing on its page but this
	// field, and writing is the whole of what there is to do: it takes the
	// cursor. One that says something already is arrived at to be read as often
	// as to be edited, so that one is left alone. (After the frame, or the
	// field isn't there yet.)
	if (!(A.peek($t, 'description') ?? '').trim()) requestAnimationFrame(() => (box.querySelector('textarea') as HTMLTextAreaElement | null)?.focus());
}

/**
 * What sits above the log, on one line: the project, the task's title — in
 * full, with what it was asked, in its tooltip — a button per service running
 * or recently ended and per port something listens on, the merge button while
 * the work is ready to merge, and the menu of everything else.
 */
function drawTaskHeader(pid: string, tid: string, $t: any): void {
	A('div display:flex align-items:center gap:0.4rem min-width:0', () => {
		A(`b flex-shrink:0 max-width:40% ${ELLIPSIS} text=`, projectName($state.projects[pid] ?? {}));
		A(`span flex:1 min-width:0 ${ELLIPSIS} fg:$s-muted`, () => {
			A(() => A('text=', taskTitle($t)));
			S.addTooltip({ tip: () => A('div white-space:pre-wrap max-width:32rem max-height:60vh overflow:hidden text=',
				[taskTitle($t), ($t.description ?? '').trim()].filter(Boolean).join('\n\n')) });
		});
		drawServiceButtons(pid, tid, $t);
		drawPortButtons($t);
		A(() => {
			if (!canMerge(pid, $t)) return;
			S.iconButton({ icon: gitMerge, ariaLabel: 'Merge…', attrs: 'fg:$s-success', key: 'mod+shift+g',
				tooltip: 'The agent reports the work ready to merge', click: () => doneDialog(pid, tid, $t) });
		});
		S.iconButton({ icon: menu, ariaLabel: 'Task menu', tooltip: 'Everything to do with this task',
			click: (e: Event) => { S.showFloatingMenu({ anchor: e.currentTarget as HTMLElement, items: headerItems(pid, tid, $t) }); } });
	});
}

/**
 * The header's menu: the task's own (where it can go, and how it ends), with
 * what is for the human at the wheel and its settings slotted in, and below
 * those its services and its forwarded ports.
 */
function headerItems(pid: string, tid: string, $t: any): S.MenuEntry[] {
	const extra: S.MenuEntry[] = [];
	// Only while the task is yours: rebasing and rebuilding both move the
	// ground under a running agent, and both are for the human at the wheel.
	if (waitsForHuman($t)) {
		if ($t.behind) {
			const branch = $state.projects[pid]?.defaultBranch ?? 'main';
			extra.push({ label: `Rebase onto the latest ${branch} (${$t.behind} behind)`, icon: arrowDownToLine, click: () => rebaseTask(pid, tid, $t) });
		}
		extra.push({ label: 'Rebuild the container', icon: refreshCw, click: () => void cmd('reloadTask', { pid, tid }) });
	}
	if ($state.projects[pid]?.scratch) extra.push({ label: 'Turn into project…', icon: folderGit2, click: () => turnIntoProjectDialog(pid, tid) });
	extra.push({ label: 'Settings…', icon: settings, click: () => taskSettingsDialog(pid, tid, $t) });
	const items = taskMenuItems(pid, tid, $t, extra);
	if (hasServices($t)) items.push({ separator: true }, ...serviceItems(pid, tid, $t));
	if ($t.ports?.length) items.push({ separator: true }, ...portItems($t));
	return items;
}

/**
 * Make a scratch task the first task of a project of its own: a repository
 * made in the directory given, from the same empty commit the task grew from,
 * with the task moved into it — conversation and work and all, the work
 * unmerged.
 */
function turnIntoProjectDialog(pid: string, tid: string): void {
	const $form = A.proxy({ dir: '', name: '' });
	void S.dialog({ header: 'Turn into project', attrs: 'w:32rem', content: close => {
		S.form({
			submit: async () => {
				const result = await cmd('turnIntoProject', { pid, tid, dir: $form.dir.trim(), name: $form.name.trim() });
				if (!result) return;
				close();
				route.go(pathTo(result.pid, result.tid));
			},
			content: () => {
				A('p mt:0 #A new git repository is made in this directory, and this task becomes the first task of its project, its work unmerged.');
				S.textline({ label: 'Directory', placeholder: '~/projects/app', required: true, bind: A.ref($form, 'dir') });
				S.textline({ label: 'Name', help: 'What to call it in the list; the directory name by default.', bind: A.ref($form, 'name') });
			},
			actions: () => S.button({ content: 'Turn into project', icon: folderGit2, type: 'submit' }),
		});
	}});
}

/** The chat, what is worth acting on right now, and the input. */
export function drawAgent(pid: string, tid: string, $t: any): void {
	// The log, the notes and the composer are one drop target: a file let go
	// anywhere in the column is meant for the message being written.
	const area = () => zone.querySelector('textarea') as HTMLTextAreaElement;
	const zone = A('div display:flex flex-direction:column gap:$3 flex:1 min-width:0 min-height:0', () => {
		drawTaskHeader(pid, tid, $t);
		drawChat(pid, tid, $t);
		A(() => {
			if ($t.phase === 'closed') {
				if ($state.projects[pid]?.scratch) return; // there was nothing to merge it into
				const branch = $state.projects[pid]?.defaultBranch ?? 'main';
				A('div.s-s.warning.tonal p:$2 text=', $t.changes?.length
					? `⚠ closed without merging: nothing of this task is on ${branch}; its work is kept.`
					: `⚠ closed without merging: nothing of this task is on ${branch}.`);
				return;
			}
			if ($t.phase === 'done') {
				const branch = $state.projects[pid]?.defaultBranch ?? 'main';
				A('div.s-s.success.tonal p:$2 text=',
					`✔ merged into ${branch}. Messaging the agent picks the task back up: it keeps everything it `
					+ `knows, gets a fresh clone of ${branch} to work in, and what it changes becomes a commit of its own.`);
				return;
			}
			if (!waitsForHuman($t)) return;
			// The last review's feedback is put in the message box, to send on as it
			// stands, to word differently, or to clear: what the reviewer asks for is
			// the user's to weigh, and saying it back to the agent is a message like
			// any other. A message half written wins (see restoreDraft).
			if ($t.review) {
				restoreDraft(pid, tid, $t.review);
				A('div.s-s.warning.tonal p:$2 #⚠ the automated review asks for changes; they are in the message box below, to send on, reword or clear');
			}
		});
		// Asked mid-turn: the agent waits on the answer, whatever the phase says.
		A(() => {
			if (!$t.openrouterAsk) return;
			drawStrip('warning', `⚠ the agent asks for $${$t.openrouterAsk} of OpenRouter spending, beyond the task’s OpenRouter budget`,
				() => S.button({ content: 'Answer…', attrs: '.small .warning', click: () => openRouterDialog(pid, tid, $t) }));
		});
		A(() => { if ($t.openrouterAsk) openRouterDialog(pid, tid, $t); });
		A(() => {
			if (!['building', 'starting', 'stopping', 'error'].includes($t.status)) return;
			const { text, color } = taskActivity(pid, $t);
			A(`div.s-s.${color}.tonal p:$2 text=`, text);
		});
		drawInputBar(pid, tid, $t);
	}) as HTMLElement;
	acceptFiles(zone, area, files => addFiles(pid, tid, files));
}

// The tasks that have the dialog below on screen, so it is not stacked twice.
const asking = new Set<string>();

/**
 * An agent's request for OpenRouter spending that its task's OpenRouter budget
 * has no room for: raise the budget to make room, prefilled with what that
 * takes, or turn the request down. The agent waits for the answer.
 */
function openRouterDialog(pid: string, tid: string, $t: any): void {
	const key = `${pid}/${tid}`;
	if (asking.has(key)) return;
	asking.add(key);
	const { openrouterAsk: ask, openrouterGranted: granted = 0, openrouterBudget: budget = 0 } = A.peek(() => ({ ...$t }));
	const $form = A.proxy({ budget: String(Math.round((granted + ask) * 100) / 100) });
	void S.dialog({ header: 'OpenRouter request', attrs: 'w:32rem', content: close => {
		// Answered, here or elsewhere: nothing is left to decide.
		A(() => { if (!$t.openrouterAsk) close(); });
		S.form({
			submit: async () => { if (await cmd('grantOpenRouter', { pid, tid, budget: $form.budget })) close(); },
			content: () => {
				A('p m:0 text=', `The agent asks for an OpenRouter API key with a limit of $${ask}. This task’s OpenRouter budget is `
					+ `$${budget}, of which $${granted.toFixed(2)} has been handed out already, so the OpenRouter budget would need to be raised to grant it.`);
				S.textline({ label: 'OpenRouter budget limit (USD)', type: 'number', bind: A.ref($form, 'budget') });
			},
			actions: () => {
				S.button({ content: 'Reject request', attrs: '.neutral', click: () => { void cmd('rejectOpenRouter', { pid, tid }); close(); } });
				S.button({ content: 'Change OpenRouter budget', icon: check, type: 'submit' });
			},
		});
	}}).then(() => asking.delete(key));
}

// While something goes on in the task (see taskBusy), colour flows around the
// composer's border: this is where the eye waits for the agent.
const working = A.insertCss({
	'& .s-input, & .s-input:focus-visible': 'border-color: transparent; animation: tps-flow 3s linear infinite; '
		+ 'background: linear-gradient(color-mix(in oklab, $s-bg, $s-text 4%), color-mix(in oklab, $s-bg, $s-text 4%)) padding-box, '
		+ 'linear-gradient(90deg, $s-primary, $s-link, $s-warning, $s-primary) 0 0 / 200% 100% border-box;',
	'& .s-input:not(:focus-visible)': 'box-shadow: 0 0 8px -2px color-mix(in oklab, $s-link, transparent 40%);',
});
A.insertGlobalCss({ '@keyframes tps-flow': { to: 'background-position: 0 0, 200% 0;' } });

function drawInputBar(pid: string, tid: string, $t: any): void {
	// Whatever was typed here and never sent, from before this task was left.
	const draft = chatDraft(pid, tid);
	const $atts = attachments(pid, tid);
	const $has = A.proxy({ text: !!draft.trim() });
	// Looked up rather than remembered: with a draft restored, the field is in
	// use before it has seen a single keystroke.
	const area = () => bar.querySelector('textarea') as HTMLTextAreaElement;
	const sendMsg = () => {
		const el = area();
		const text = el.value.trim();
		const files = takeAttachments(pid, tid);
		if (!text && !files.length) return;
		el.value = '';
		el.dispatchEvent(new Event('input')); // shrink it back down, drop $has.text, and forget the draft
		void cmd('chat', { pid, tid, text, files });
	};
	const bar = A('div display:flex flex-direction:column gap:$2', () => {
		A(() => A(working + '=', taskBusy($t)));
		drawAttachments(pid, tid, name => {
			dropAttachment(pid, tid, name);
			removeRef(area(), uploadPath(name));
		});
		S.textarea({
			placeholder: 'Message the agent…', inputAttrs: 'max-height:40dvh',
			value: draft,
			// The field's own bottom-right corner, beside the caret: the paperclip
			// that attaches a file, and then — send while there is something to
			// send, else stop while claude works. An inset sizes what it holds
			// `.small`; sending is the one thing in the composer to aim at, so that
			// button takes its size back.
			suffix: () => {
				attachButton(area, files => addFiles(pid, tid, files));
				A(() => {
					const size = 'w:2.2rem h:2.2rem font-size:1.1rem ';
					if ($has.text || $atts.length) S.iconButton({ icon: sendHorizontal, ariaLabel: 'Send', key: 'mod+enter', attrs: size + 'fg:$s-primary', click: sendMsg });
					else if ($t.working) S.iconButton({ icon: circleStop, ariaLabel: 'Stop the agent', attrs: size + 'fg:$s-danger',
						click: () => void cmd('stopAgent', { pid, tid }) });
					// Nothing to send and nothing to stop: how full the agent's window is,
					// and the button that empties it. What is about the conversation rather
					// than about this message belongs where the eye already is when nothing
					// is being typed. Nothing is measured before the agent has run, and a
					// finished task has no window to look into — an empty ring would be a
					// button over nothing, so neither draws one.
					else if (hasWorkspace($t) && contextSlices($t)) S.iconButton({
						icon: () => drawContextRing($t, '1.4rem'),
						ariaLabel: 'Compact the agent’s memory',
						attrs: size + 'fg:$s-muted',
						tooltip: () => drawContextTip($t, 'Click to have it do that now.'),
						click: () => void cmd('chat', { pid, tid, text: '/compact' }),
					});
				});
			},
			input: (e: Event) => {
				const text = (e.target as HTMLTextAreaElement).value;
				$has.text = !!text.trim();
				setChatDraft(pid, tid, text);
			},
		});
	}) as HTMLElement;
	// A message handed back by a revert or a fork lands here, in the field as
	// it stands on screen.
	onComposer(pid, tid, text => {
		const el = area();
		el.value = text;
		el.dispatchEvent(new Event('input')); // grow it, and light the send button
		el.focus();
	});
}

/**
 * The right column for a task with a workspace: VS Code in its container,
 * once that is up and code-server runs in it — which the hold placed on
 * arrival (see useTask) asks the daemon for.
 */
export function drawTaskCode(pid: string, tid: string, $t: any, left: string): void {
	A(() => {
		if ($t.codePort) {
			// code-server's remote authority is the Host header, which the proxy
			// passes on unchanged. A restarted code-server has a new start time,
			// and the frame is reloaded for it.
			drawCode(`${pid}/${tid}`, `/code/${pid}/${tid}/?folder=/work`, `${$t.codePort}:${$t.codeStart}`, left);
		} else if ($t.status === 'error' || $t.codeError) {
			S.box({ contentAttrs: 'display:flex flex-direction:column align-items:flex-start', content: () => {
				A('p fg:$s-danger text=', $t.status === 'error' ? `Workspace error: ${$t.statusDetail || 'unknown'}` : `VS Code failed to start: ${$t.codeError}`);
				S.button({ content: 'Retry', click: () => void cmd('openTask', { pid, tid }) });
			}});
		} else {
			S.box({ contentAttrs: 'display:flex flex-direction:column', content: () => {
				A('p text=', `${$t.status === 'up' ? 'starting VS Code' : taskActivity(pid, $t).text}…`);
				A('progress w:100%');
			}});
		}
	});
}

/**
 * The right column for a finished task, which has nothing to open: one that
 * merged (its work being on the branch) or one closed without merging (its
 * work kept beside it). Its workspace is gone either way, and its
 * conversation kept, so picking it back up — by a message to the agent, or by
 * hand here — clones the branch again, puts the kept work back if there is
 * any, and carries on.
 */
export function drawDonePanel(pid: string, tid: string, $t: any): void {
	const branch = $state.projects[pid]?.defaultBranch ?? 'main';
	const closed = $t.phase === 'closed';
	const scratch = !!$state.projects[pid]?.scratch;
	const files = `${$t.changes?.length} file${$t.changes?.length === 1 ? '' : 's'}`;
	S.box({ header: scratch ? 'Closed' : closed ? 'Not merged' : `Merged into ${branch}`, contentAttrs: 'display:flex flex-direction:column align-items:flex-start gap:$2', content: () => {
		A('p m:0 text=', scratch
			? `This task was closed. ${$t.changes?.length
				? `Its work is kept, ${files}, and its conversation with it: pick it back up to have the work back as it was and carry on.`
				: 'It had no work to keep; its conversation is kept. Pick it back up to carry on in an empty workspace.'}`
		: closed ? `This task was closed without merging. ${$t.changes?.length
				? `Its work is kept, ${files} off ${branch}, and its conversation with it: pick it back up to have the work back as it was and carry on.`
				: `It had no work to keep; its conversation is kept. Pick it back up to give it a fresh clone of ${branch} and carry on.`}`
			: `This task's work is on ${branch}, and its workspace is gone. The conversation is kept: message the agent `
				+ `to pick the task back up in a fresh clone of ${branch}, or take it on yourself.`);
		A('div display:flex gap:$2 flex-wrap:wrap', () => {
			S.button({ content: 'Pick up as human', icon: user, attrs: '.small', click: () => void cmd('moveTask', { pid, tid, phase: 'human' }) });
			S.button({ content: 'Delete task…', icon: trash2, attrs: '.small .neutral', click: () => void deleteTask(pid, tid, $t) });
		});
	}});
}

