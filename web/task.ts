import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { arrowDownToLine, check, circleSlash, circleStop, gitMerge, play, refreshCw, sendHorizontal, settings, trash2, user, x } from 'staffa/icons.js';
import { acceptFiles, addFiles, attachButton, attachments, dropAttachment, drawAttachments, removeRef, takeAttachments, uploadFiles, uploadPath } from './attach.ts';
import { bot } from './bot.ts';
import { drawChat, promptDialog } from './chat.ts';
import { drawCode } from './code.ts';
import { $state, watchTask } from './conn.ts';
import { hold, release } from './holds.ts';
import { applyNotifyDefault, notifies, notifiesByDefault, toggleDefaultNotifies, toggleNotifies } from './notify.ts';
import { anyRunning, hasServices, servicesMenu } from './services.ts';
import { autoStarts, busyAttrs, canMerge, chatDraft, cmd, contextSlices, debounce, drawContextRing, drawContextTip, hasWorkspace, hostName, isFinished, isOpenable, onComposer, pathTo, restoreDraft, selection, setChatDraft, PHASE_ICONS, PHASE_LABELS, taskActivity, taskBusy, taskName, tidOrder, waitingFor, waitsForHuman, type Phase } from './util.ts';

/**
 * Keep the task's chat streaming for as long as the calling scope lives, and
 * hold the task (see holds.ts) once it has a workspace: arriving at it opens
 * VS Code on it, and that stays open — listed in the sidebar — until closed.
 *
 * Opening a muted task is how it stops being muted: looking at it is taking it
 * back on, and a task you are working on belongs in the sidebar with the rest.
 */
export function useTask(pid: string, tid: string, $t: any): void {
	watchTask(pid, tid);
	A(() => { if (isOpenable($t)) hold(pid, tid); });
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
 * Close the task: VS Code on it is stopped and it leaves the sidebar — unless
 * it waits for a human, which keeps it listed. Whoever was looking at it
 * lands on the project's board.
 */
export function closeTask(pid: string, tid: string): void {
	release(pid, tid);
	if (A.peek(selection).tid === tid) void route.go(pathTo(pid));
}

/**
 * The phases the task can be moved to; the one it is in is the disabled one.
 * The three ways a task ends are one entry — Done asks which of them is meant
 * (see doneDialog) — and the two that are not places to be put but states to
 * be in only show where they do mean something: Muted is Human with the task
 * put away, so it is offered while it waits for you.
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
	if ($t.phase === 'human') items.push({ label: PHASE_LABELS.muted, icon: PHASE_ICONS.muted, click: () => void moveTask(pid, tid, $t, 'muted') });
	items.push({
		label: 'Done…', icon: check,
		disabled: isFinished($t) || $t.phase === 'merge',
		click: () => doneDialog(pid, tid, $t),
	});
	return items;
}

/**
 * The task's whole menu: where it can go, and how it ends — plus the one thing
 * that is not a phase but belongs with them, throwing it away. `extra` slots
 * in behind that, for the callers that have more to offer.
 */
export function taskMenuItems(pid: string, tid: string, $t: any, extra: S.MenuEntry[] = []): S.MenuEntry[] {
	return [
		...phaseItems(pid, tid, $t),
		{ separator: true },
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
	if (phase === $t.phase) return false;
	if (phase === 'plan') {
		const busy = $t.working ? ' The agent is still working; it is stopped.' : '';
		if (!(await S.confirm(`Move this task back to Plan? All work is discarded: the workspace, the chat, and every unmerged change.${busy}`))) return false;
	} else if (phase === 'done' || phase === 'closed' || phase === 'merge') {
		if ($t.phase !== 'merge') doneDialog(pid, tid, $t); // in Merging it is already on its way there
		return false;
	} else if (phase === 'agent' && !(await confirmOvertake(pid, $t))) return false;
	void cmd('moveTask', { pid, tid, phase });
	return true;
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
 * into it. It is the front half of a merge, and goes the same way: the work is
 * taken off as a patch, the tree reset to the branch, and the work put back on
 * top — with what does not merge cleanly left in the files, marked, for you or
 * the agent to resolve.
 */
async function rebaseTask(pid: string, tid: string, $t: any): Promise<void> {
	const branch = A.peek(() => $state.projects[pid]?.defaultBranch) ?? 'main';
	const busy = A.peek($t, 'working') ? ' The agent is still working; it is stopped first.' : '';
	if (!(await S.confirm(`Put this task's work onto the latest ${branch}? The workspace is reset to the branch and the work `
		+ `— uncommitted changes and untracked files alike — is applied on top of it again, so nothing is lost. What does not `
		+ `merge cleanly is left in the files with conflict markers, for you or the agent, which is told what happened the `
		+ `next time it is sent in.${busy}`))) return;
	void cmd('rebaseTask', { pid, tid });
}

/**
 * How a task ends, as one dialog with a tab per answer: merged onto the branch
 * — the usual one, and the one it opens on — or put away without merging, its
 * work kept as a patch, off the branch. Everything that ends a task lands
 * here, since which of the two is meant is worth being sure of.
 *
 * A task with nothing to merge (one still in Plan) and one that is closed
 * already (nothing left to put away) have only one answer between them, and
 * get that tab's content without the strip above it.
 */
export function doneDialog(pid: string, tid: string, $t: any): void {
	const branch = A.peek(() => $state.projects[pid]?.defaultBranch) ?? 'main';
	const mergeable = $t.phase !== 'plan' && hasWorkspace($t);
	const closable = $t.phase !== 'closed';
	if (!mergeable && !closable) return;
	const $merge = A.proxy({ message: (A.peek($t, 'commitMessage') || A.peek($t, 'title') || '') as string });
	void S.dialog({ header: mergeable && !closable ? 'Merge this task' : 'Finish this task', attrs: 'w:44rem', content: close => {
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
			A('p mt:0 rich=', mergeable
				? `The task ends as it stands: its work is kept as a patch, off ${branch}, and its workspace is removed. Nothing else will see the work — but for as long as the task is not deleted it can be picked up again, which puts the work onto the latest ${branch}, where it can still be merged.`
				: `This task has nothing to merge, so this is the only way it ends: it is put away as it stands.`);
			A('div display:flex gap:$2 justify-content:flex-end', () => {
				S.button({ content: 'Finish without merging', icon: circleSlash, attrs: '.danger .outlined',
					// The task is over: its VS Code goes with it, the way Close does
					// it. Opening the task again brings the session back.
					click: () => { close(); void cmd('moveTask', { pid, tid, phase: 'closed' }); closeTask(pid, tid); } });
			});
		};
		if (mergeable && closable) {
			S.tabs({ contentAttrs: 'pt:$3', tabs: [
				{ id: 'merge', label: `Merge into ${branch}`, icon: gitMerge, content: drawMerge },
				{ id: 'closed', label: 'Don’t merge', icon: circleSlash, content: drawDontMerge },
			]});
		} else if (mergeable) drawMerge();
		else drawDontMerge();
	}});
}

/**
 * Start a task: no dialog, and no half-existing state either — the task is
 * created right away, in Plan and without a description, so the board and the
 * sidebar have it from the first moment. It goes by "New" until its
 * description's first line names it (see taskTitle).
 */
export async function addTask(pid: string): Promise<void> {
	// The server fills in the project's defaults; nothing to send along. All
	// but one, that is: ready notifications are this browser's, so its own
	// default is applied here (see notify.ts).
	const created = await cmd('createTask', { pid });
	if (!created) return;
	applyNotifyDefault(pid, created.tid as string);
	route.go(pathTo(pid, created.tid as string));
}

/**
 * The models to choose from for one of the task's two model settings: what the
 * claude on the project's host offers, plus whatever the task is set to, so a
 * model that host no longer lists still shows.
 */
function modelOptions(pid: string, $t: any, field = 'model'): string[] {
	const host = $state.hosts[$state.projects[pid]?.host];
	const models: string[] = [...(host?.models ?? $state.models ?? [])];
	const current = $t[field];
	if (current && !models.includes(current)) models.push(current);
	return models;
}

/** Why the list is the built-in one rather than what claude offers, if it is. */
function modelsError(pid: string): string {
	const hid = $state.projects[pid]?.host;
	const host = $state.hosts[hid];
	if (!host?.modelsError) return '';
	return `claude on ${hostName(hid)} could not be asked which models it offers, so this is the built-in list: ${host.modelsError}`;
}

/**
 * Model, what the task follows, budget, merge behaviour: everything about a
 * task except its phase and title. Changes go through `save`, which either
 * tells the server or (drawn on a project's defaults, which have no phase and
 * so no `Start after`) sets what the next task there starts with.
 */
export function drawTaskFields(pid: string, tid: string | undefined, $t: any, save: (patch: object) => void): void {
	// The two models stand together: a task is often worth a different mind from
	// the one that reads its work over. Under either, the reason its list is the
	// built-in one, if it is not claude's own.
	A(() => S.select({
		label: 'Agent model', options: () => modelOptions(pid, $t),
		// The rules every one of the agent's turns is worked under are the one
		// thing it is told that never shows in the log, and this is where the
		// mind they are told to is chosen. The reviewer's own are in the log,
		// on the note that starts a review (see review.go).
		help: () => {
			A('a cursor:pointer #What TPS tells the agent…', 'click=', () => promptDialog('What TPS tells the agent', $state.agentPrompt));
			const err = modelsError(pid);
			if (err) A('div text=', err);
		},
		bind: {
			get value() { return $t.model ?? 'default'; },
			set value(model: string) { if (model) save({ model }); },
		},
	}));
	A(() => S.select({
		label: 'Review model', options: () => modelOptions(pid, $t, 'reviewModel'), help: modelsError(pid) || undefined,
		bind: {
			get value() { return $t.reviewModel ?? 'default'; },
			set value(model: string) { if (model) save({ reviewModel: model }); },
		},
	}));
	// Only while the task is in Plan: following others is how it leaves Plan,
	// so once it has, there is nothing left to set here.
	A(() => {
		if ($t.phase !== 'plan') return;
		S.autocomplete({
			label: 'Start after', multi: true, allowCustom: false,
			placeholder: 'Tasks to wait for…',
			help: 'The task hands itself to the agent once these are all done or deleted, and its plan is closed. Its workspace is made then, so it holds their merged work.',
			// The tasks it already follows stay listed even when done, so their
			// chips read as names rather than as numbers.
			options: () => {
				const after: string[] = $t.startAfter ?? [];
				const self = tid;
				const $tasks = $state.projects[pid]?.tasks ?? {};
				return Object.keys($tasks)
					.filter(o => o !== self && (after.includes(o) || !isFinished($tasks[o])))
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
		label: 'Task budget limit (USD)', type: 'number',
		help: 'The task is parked for you when spending reaches it. Empty: no limit.',
		value: A.peek($t, 'budget') != null ? String(A.peek($t, 'budget')) : '',
		input: debounce(600, (e: Event) => save({ budget: (e.target as HTMLInputElement).value })),
	});
	drawReadyFields(pid, $t, save);
	// The odd one out: this setting is not the task's but this browser's, so it
	// goes nowhere near `save` (see notify.ts). Switching it on can be refused —
	// the browser may not allow notifications — and the box then says so by
	// going back to where it was.
	S.checkbox({
		label: 'Ready notifications',
		help: tid
			? 'A desktop notification from this browser when the task comes back to you. Kept by the browser, not by the task.'
			: 'Switch them on for the tasks made here from now on. Kept by the browser, not by the project.',
		checked: A.peek(() => tid ? notifies(pid, tid) : notifiesByDefault(pid)),
		change: async (e: Event) => {
			await (tid ? toggleNotifies(pid, tid) : toggleDefaultNotifies(pid));
			(e.target as HTMLInputElement).checked = A.peek(() => tid ? notifies(pid, tid) : notifiesByDefault(pid));
		},
	});
}

/**
 * What becomes of finished work: one question per moment where it can go more
 * than one way, each answered on its own, and the first answer to each is what
 * a task does when nothing is said. The two about the review are asked even
 * where no review is set to happen, because a review asked for by hand (the
 * board's Review column) ends the same way and follows the same answers.
 */
function drawReadyFields(pid: string, $t: any, save: (patch: object) => void): void {
	S.select({
		label: 'When the agent reports the task ready',
		help: 'Reviewing is a second agent reading the work over against what you asked for, and above all for size: what can be left out, and what the project already does elsewhere. It fixes the small and obvious itself, and either accepts the work or lists what to change.',
		options: [
			{ value: 'review', label: 'Have it reviewed' },
			{ value: 'human', label: 'Assign it to me' },
			{ value: 'merge', label: 'Merge it' },
		],
		bind: {
			get value() { return $t.onReady ?? 'review'; },
			set value(onReady: string) { if (onReady) save({ onReady }); },
		},
	});
	S.select({
		label: 'When a review asks for changes',
		options: [
			{ value: '0', label: 'Assign it to me' },
			{ value: '1', label: 'Send it back to the agent, at most once' },
			{ value: '2', label: 'Send it back to the agent, at most twice' },
			{ value: '3', label: 'Send it back to the agent, at most three times' },
		],
		help: 'Each time, the work is reviewed again. What the last review still asks for comes to you, in the task’s message box.',
		bind: {
			get value() { return String($t.reviewLoops ?? 0); },
			set value(reviewLoops: string) { if (reviewLoops) save({ reviewLoops }); },
		},
	});
	S.select({
		label: 'When a review accepts',
		options: [
			{ value: 'human', label: 'Assign it to me' },
			{ value: 'merge', label: 'Merge it' },
		],
		bind: {
			get value() { return $t.onAccept ?? 'human'; },
			set value(onAccept: string) { if (onAccept) save({ onAccept }); },
		},
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
	const box = A('div display:flex flex-direction:column h:100%', () => {
		S.textarea({
			attrs: 'h:100%', inputAttrs: 'flex:1 min-height:0', autoGrow: false, resize: 'none',
			placeholder: 'What should the agent do?', value: A.peek($t, 'description') ?? '',
			suffix: () => attachButton(area, take),
			input: (e: Event) => {
				const description = (e.target as HTMLTextAreaElement).value;
				$t.description = description;
				store(description);
			},
		});
	}) as HTMLElement;
	acceptFiles(box, area, take);
	// A task with nothing written down yet has nothing on its page but this
	// field, and writing is the whole of what there is to do: it takes the
	// cursor. One that says something already is arrived at to be read as often
	// as to be edited, so that one is left alone. (After the frame, or the
	// field isn't there yet.)
	if (!(A.peek($t, 'description') ?? '').trim()) requestAnimationFrame(() => (box.querySelector('textarea') as HTMLTextAreaElement | null)?.focus());
}

/** The chat, what is worth acting on right now, and the input. */
export function drawAgent(pid: string, tid: string, $t: any): void {
	// The log, the notes and the composer are one drop target: a file let go
	// anywhere in the column is meant for the message being written.
	const area = () => zone.querySelector('textarea') as HTMLTextAreaElement;
	const zone = A('div display:flex flex-direction:column gap:$3 flex:1 min-width:0 min-height:0', () => {
		// The log fills the column, with the icons floating over its top right
		// corner: chrome that would otherwise cost the conversation a row.
		A('div position:relative display:flex flex-direction:column flex:1 min-width:0 min-height:0', () => {
			drawChat(pid, tid, $t);
			// A neutral surface, opaque and rounded, so the log scrolls under them
			// rather than through them. How tall the row is the buttons decide, so it
			// is measured rather than guessed: the log keeps that much room at its top
			// (see drawChat) for its first entry to start below them.
			const bar = A('div.s-s.neutral.shadow position:absolute top:0 right:0 display:flex align-items:center p:0.15rem r:99em', () => {
				A(() => { // its own scope: a service arriving must not redraw the row
					if (!hasServices($t)) return;
					// Red while something runs: services go on behind a closed console, and this is what brings it back.
					const running = anyRunning($t);
					S.iconButton({ icon: play, ariaLabel: running ? 'Services: something is running' : 'Services and ports',
						tooltip: 'What runs in the container, and the ports it forwards',
						attrs: running ? 'fg:$s-danger' : '', click: (e: Event) => servicesMenu(e.currentTarget as HTMLElement, pid, tid, $t) });
				});
				// Only while the task is yours: rebasing and rebuilding both move the
				// ground under a running agent, and both are for the human at the wheel.
				A(() => {
					if (!waitsForHuman($t) || !$t.behind) return;
					const branch = $state.projects[pid]?.defaultBranch ?? 'main';
					S.iconButton({ icon: arrowDownToLine, ariaLabel: 'Rebase onto the latest ' + branch,
						tooltip: () => A('text=', `${branch} has moved on by ${$t.behind} commit${$t.behind === 1 ? '' : 's'}: replay this task's work on top of it`),
						click: () => rebaseTask(pid, tid, $t) });
				});
				A(() => {
					if (!waitsForHuman($t)) return;
					S.iconButton({ icon: refreshCw, ariaLabel: 'Rebuild the container',
						tooltip: 'Rebuild the container from Containerfile.dev',
						click: () => void cmd('reloadTask', { pid, tid }) });
				});
				S.iconButton({ icon: settings, ariaLabel: 'Task settings', tooltip: 'Its title, its model, its budget — everything but its phase', click: () => taskSettingsDialog(pid, tid, $t) });
				// The phase, worn as the icon it has on the board and in the sidebar,
				// is the button for everything that is about the phase: a menu needs
				// no glyph of its own where the state it acts on is one. It breathes
				// while something is going on, exactly as the sidebar's does.
				A(() => {
					const phase = $t.phase as Phase;
					const icon = PHASE_ICONS[phase] ?? bot;
					S.iconButton({
						icon: () => icon({ attrs: taskBusy($t) ? busyAttrs(icon) : undefined }),
						ariaLabel: `Phase: ${PHASE_LABELS[phase] ?? phase}`,
						tooltip: `${PHASE_LABELS[phase] ?? phase} — move it to another phase, finish it, delete it`,
						click: (e: Event) => { S.showFloatingMenu({ anchor: e.currentTarget as HTMLElement, items: taskMenuItems(pid, tid, $t) }); },
					});
				});
				S.iconButton({ icon: x, ariaLabel: 'Close', key: 'mod+shift+x', tooltip: 'Close VS Code and put the task away; one waiting for you stays listed',
					click: () => closeTask(pid, tid) });
			}) as HTMLElement;
			const measure = new ResizeObserver(() => bar.parentElement?.style.setProperty('--tps-overlay', `${bar.offsetHeight}px`));
			measure.observe(bar);
			A.clean(() => measure.disconnect());
		});
		A(() => {
			if ($t.phase === 'closed') {
				const branch = $state.projects[pid]?.defaultBranch ?? 'main';
				A('div.s-s.warning.tonal p:$2 text=', $t.changes?.length
					? `⚠ closed without merging: nothing of this task is on ${branch}; its work is kept as a patch.`
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
			if ($t.conflicts?.length) {
				const branch = $state.projects[pid]?.defaultBranch ?? 'main';
				A('div.s-s.warning.tonal p:$2 display:flex align-items:center gap:$2', () => {
					A('span flex:1 text=', `⚠ conflicts in ${$t.conflicts.join(', ')}: putting this task's work onto the latest ${branch} did not merge cleanly there. `
						+ 'Resolve the markers in VS Code, or send in the agent, which is told about them.');
					S.button({ content: 'Send in the agent', icon: bot, attrs: '.small', click: () => void moveTask(pid, tid, $t, 'agent') });
				});
			}
		});
		// The merge has no button of its own down here: the agent reporting the
		// work done is a moment in the log, and the button sits with it (see
		// drawReady). The key it had reaches it from wherever the log is scrolled.
		A(() => { if (canMerge($t)) S.bindKey('mod+shift+g', 'Merge this task…', () => doneDialog(pid, tid, $t)); });
		A(() => {
			if (!['building', 'starting', 'stopping', 'error'].includes($t.status)) return;
			const { text, color } = taskActivity(pid, $t);
			A(`div.s-s.${color}.tonal p:$2 text=`, text);
		});
		drawInputBar(pid, tid, $t);
	}) as HTMLElement;
	acceptFiles(zone, area, files => addFiles(pid, tid, files));
}

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
 * work kept as a patch). Its workspace is gone either way, and its
 * conversation kept, so picking it back up — by a message to the agent, or by
 * hand here — clones the branch again, puts the patch on top if there is one,
 * and carries on.
 */
export function drawDonePanel(pid: string, tid: string, $t: any): void {
	const branch = $state.projects[pid]?.defaultBranch ?? 'main';
	const closed = $t.phase === 'closed';
	S.box({ header: closed ? 'Not merged' : `Merged into ${branch}`, contentAttrs: 'display:flex flex-direction:column align-items:flex-start gap:$2', content: () => {
		A('p m:0 text=', closed
			? `This task was closed without merging. ${$t.changes?.length
				? `Its work is kept as a patch of ${$t.changes.length} file${$t.changes.length === 1 ? '' : 's'}, off ${branch}, and its conversation with it: pick it back up to put the work onto the latest ${branch} and carry on.`
				: `It had no work to keep; its conversation is kept. Pick it back up to give it a fresh clone of ${branch} and carry on.`}`
			: `This task's work is on ${branch}, and its workspace is gone. The conversation is kept: message the agent `
				+ `to pick the task back up in a fresh clone of ${branch}, or take it on yourself.`);
		A('div display:flex gap:$2 flex-wrap:wrap', () => {
			S.button({ content: 'Pick up as human', icon: user, attrs: '.small', click: () => void cmd('moveTask', { pid, tid, phase: 'human' }) });
			S.button({ content: 'Delete task…', icon: trash2, attrs: '.small .neutral', click: () => void deleteTask(pid, tid, $t) });
		});
	}});
}

