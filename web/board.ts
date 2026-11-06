import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { chevronsDown, funnel, gitCommitHorizontal, gitCommitVertical, plus, rocket, settings, x } from 'staffa/icons.js';
import { $state } from './conn.ts';
import { deployProject, moveTask, taskMenuItems, taskSettingsDialog } from './task.ts';
import { autoStarts, busyAttrs, canMerge, cmd, COLUMNS, contextSlices, costText, drawContextRing, drawContextTip, drawLiveLink, drawTaskIcon, ELLIPSIS, isJob, isSchedule, keepsWork, mergeText, pathTo, phaseOrder, PHASE_ICONS, PHASE_LABELS, projectColor, requested, scheduleText, taskActivity, taskTitle, waitsForHuman, type Phase } from './util.ts';

/**
 * A board: a box per column, a card dropped anywhere in a box's body. It holds
 * the tasks of one project, or of all of them (the front page), each card
 * edged in its project's colour. The width the columns share is defined once,
 * so they can't drift apart.
 *
 * The column says what phase its cards are in — its header wears the phase's
 * icon — so a card repeats none of that: it carries a glyph only where it has
 * something else to say (see saysMore).
 *
 * Two columns hold more than their own phase, the extra cards wearing the icon
 * that tells them apart. Plan has the started (muted) tasks at its top: put
 * away with their work kept, to be taken up again, and at its bottom a section
 * of scheduled tasks: they never leave Plan, and are no cards to drag. Done
 * has the ones closed without merging mixed in with the merged ones. Merging is a moment on the
 * way to Done rather than a place of its own, so those cards simply sit at the
 * top of the Done column, wearing the merge icon until it is over: the merge
 * queue (see daemon/queue.go), the one merging at its bottom and the ones
 * queued after it above, the last one queued on top. Dragging one of those
 * onto another puts it above or below that one in the queue, by the half it
 * is dropped on; below the one merging, it merges in its place right away.
 *
 * A column is one drop area: where in it a card lands says nothing, as muting
 * and closing are not things to do by accident.
 *
 * On the board of a project that deploys, Done is its branch as it is (see
 * drawBranch), cut by a line where the last deploy is, and below a faint line
 * the tasks with no place on it. The column's rocket deploys the tip; one
 * hanging above a card or commit of the branch deploys the branch up to that,
 * moving the line there — up for what is still to go, down for a deploy to
 * take back. A project deployed outside TPS, its last deploy recorded by hand
 * in refs/tps/deployed, gets the same column without the rockets.
 */
const boardWidths = A.insertCss({
	'&': `--col: clamp(190px, calc((100% - ${COLUMNS.length - 1} * var(--m3)) / ${COLUMNS.length}), 320px);`,
});

/**
 * What each column is filtered by, keyed by project (none for all of them)
 * and phase. Module state, as a filter outlives the popup it was typed in —
 * and outlives leaving the board, which its lit funnel is there to say on the
 * way back.
 */
const $filters = A.proxy<Record<string, string>>({});

/** The filter a column goes by, as its key into `$filters`. */
function filterKey(pid: string | undefined, phase: Phase): string {
	return `${pid ?? ''}:${phase}`;
}

// A funnel on every column would be noise at full strength, so a resting one
// barely shows until pointed at. One that is hiding cards steps forward in the
// accent colour instead: with its popup dismissed, the glyph is all that says
// why the column is short.
const funnelLook = A.insertCss({
	'&': 'opacity:0.3 transition: opacity 0.15s, color 0.15s;',
	'&:hover, &:focus-visible': 'opacity:1',
	'&.lit': 'opacity:1 fg:$s-accent',
});

/** A task on the board. */
type Card = { pid: string; tid: string };

/**
 * The board of project `pid`, or of every project without one. `create` is
 * what the Plan column's ✛ does.
 */
export function drawBoard(pid: string | undefined, create: () => void): void {
	// Every task on the board in one list, so a column can sort the cards of
	// all projects among each other.
	// The project's own jobs (see isJob) are not cards: the sidebar has them.
	const $cards = A.multiMap($state.projects, ($p: any, p: string) => p === pid || !pid
		? Object.fromEntries(Object.entries($p.tasks ?? {}).filter(([, $t]) => !isJob($t)).map(([tid]) => [`${p}/${tid}`, { pid: p, tid }]))
		: undefined);
	A('div display:flex gap:$3 align-items:stretch overflow-x:auto h:100%', boardWidths, () => {
		for (const phase of COLUMNS) {
			S.box({
				attrs: 'w:var(--col) flex:none mt:0 min-height:14rem',
				// min-height:0 lets the body shrink below its content, so it is the
				// part that scrolls while the header stays put.
				contentAttrs: 'flex:1 min-height:0 overflow-y:auto display:flex flex-direction:column',
				header: () => {
					A('span display:inline-flex flex:none fg:$s-muted', () => PHASE_ICONS[phase]({ size: '1.1em' }));
					A('text=', PHASE_LABELS[phase]);
					A('div display:flex align-items:center gap:$1 ml:auto', () => {
						if (phase === 'plan') S.iconButton({ icon: plus, ariaLabel: 'Create task', attrs: '.small', click: create });
						if (phase === 'done' && pid) drawDeployButton(pid);
						drawFilterButton(pid, phase);
					});
				},
				content: () => drawColumn(pid, $cards, phase),
			});
		}
	});
}

/**
 * Done's rocket, where the project deploys: deploy the tip of the branch. It
 * lights up while the branch holds work not deployed yet, and breathes while a
 * deploy runs, when it opens the Deploy task instead.
 */
function drawDeployButton(pid: string): void {
	A(() => {
		const $p = $state.projects[pid];
		if (!$p?.deploy?.declared) return;
		if (deploying($p)) return void S.iconButton({ icon: rocket, ariaLabel: 'Deploying', attrs: `.small fg:$s-success ${busyAttrs(rocket)}`,
			tooltip: 'Deploying: the Deploy task shows how it goes', click: () => void route.go(pathTo(pid, 'deploy')) });
		const branch = $p.defaultBranch ?? 'main';
		S.iconButton({ icon: rocket, ariaLabel: 'Deploy', attrs: $p.deploy.pending ? '.small fg:$s-success' : '.small',
			tooltip: $p.deploy.pending ? `Deploy: ${branch} holds work not deployed yet` : `Deploy ${branch} again: it is deployed as it is`,
			click: () => void deployProject(pid) });
	});
}

/** Whether the project's Deploy task (see isJob) is running a deploy. */
function deploying($p: any): boolean {
	return $p.tasks?.deploy?.phase === 'merge';
}

/**
 * The column's filter: a funnel that opens a field to type in, filtering the
 * cards below as the letters arrive. Dismissing the popup leaves the filter
 * standing — emptying the field is what ends one — so the funnel lights up for
 * as long as there is something typed, and says what in its tooltip.
 */
function drawFilterButton(pid: string | undefined, phase: Phase): void {
	const key = filterKey(pid, phase);
	S.iconButton({
		ariaLabel: `Filter ${PHASE_LABELS[phase]}`,
		attrs: `.small ${funnelLook}`,
		tooltip: () => A(() => A('text=', $filters[key] ? `Showing the tasks matching “${$filters[key]}”` : 'Show only the tasks matching what you type')),
		click: (e: Event) => showFilterMenu(e.currentTarget as HTMLElement, key, phase),
		// Whether it is lit is decided from inside the button, as a class on it,
		// rather than by redrawing it: the popup hangs off this very element, and
		// replacing it mid-typing would leave that hanging off a ghost.
		icon: () => {
			A(() => A('.lit=', !!$filters[key]));
			funnel();
		},
	});
}

/**
 * The field itself, in a popup hanging off the funnel. It is the menu's only
 * row, and the first thing in it that takes focus, so it has the caret the
 * moment it opens; escape or a click elsewhere puts the popup away, leaving
 * what was typed to go on filtering.
 */
function showFilterMenu(anchor: HTMLElement, key: string, phase: Phase): void {
	S.showFloatingMenu({ anchor, dropdownAttrs: 'p:$2 w:16rem', items: [() => {
		S.textline({
			placeholder: `Filter ${PHASE_LABELS[phase]}…`, bind: A.ref($filters, key),
			// Its own scope, so the button coming and going never touches the
			// field: redrawing that would take the caret with it.
			suffix: () => A(() => {
				if (!$filters[key]) return;
				S.iconButton({ icon: x, attrs: '.small', tooltip: 'Clear the filter', click: () => { $filters[key] = ''; } });
			}),
		});
	}] });
}

/**
 * Whether a card survives its column's filter: its title matched the way the
 * palette matches, a term at a time from the start of a word — so "fix log"
 * finds "Fix the log's scrolling".
 */
function passesFilter($t: any, key: string): boolean {
	const q = $filters[key];
	return !q || S.matchWords(taskTitle($t), q);
}

/**
 * One column: the cards it holds, and the empty room under them — all of it
 * taking a card dropped on it, which moves that card's task to the phase the
 * column is named after.
 */
function drawColumn(pid: string | undefined, $cards: Record<string, Card>, phase: Phase): void {
	const key = filterKey(pid, phase);
	// The gap is the one the box's body would have given the cards, now that
	// they hang in here rather than directly in it.
	A('div display:flex flex-direction:column flex:1 gap:$3',
		'dragover=', (e: DragEvent) => e.preventDefault(),
		'drop=', (e: DragEvent) => {
			e.preventDefault();
			const [p, tid] = e.dataTransfer?.getData('text/tps').split('/') ?? [];
			const $t = $state.projects[p]?.tasks?.[tid];
			// A started card let go in Plan is already where it sits.
			if (!$t || (phase === 'plan' && $t.phase === 'muted')) return;
			void moveTask(p, tid, $t, phase);
		},
		() => {
			// The ones on their way to Done go above the ones that got there, and
			// the started ones above the ones never begun.
			if (phase === 'done') drawCards($cards, ['merge'], key, undefined, queueOrder);
			if (phase === 'plan') {
				drawCards($cards, ['muted'], key);
				drawCards($cards, ['plan'], key, $t => !isSchedule($t));
				return drawSchedules($cards, key);
			}
			if (phase !== 'done') return drawCards($cards, [phase], key);
			A(() => {
				const $p = pid ? $state.projects[pid] : undefined;
				if (!$p?.deploy) return drawCards($cards, ['done', 'closed'], key);
				// The branch, and after a faint line the tasks with no place on it
				// (closed, merged before TPS stamped its commits, or merged long ago).
				const onBranch = drawBranch(pid!, $p, key);
				const placeless = Object.entries($p.tasks ?? {}).some(([tid, $t]: [string, any]) =>
					!isJob($t) && ($t.phase === 'done' || $t.phase === 'closed') && !onBranch.has(tid));
				if (placeless) A('div border-top: 1px solid currentColor; fg:$s-faint');
				drawCards($cards, ['done', 'closed'], key, (_$t, tid) => !onBranch.has(tid));
			});
		});
}

/** Plan's Scheduled section: the scheduled tasks, under a label of their own. */
function drawSchedules($cards: Record<string, Card>, key: string): void {
	A(() => {
		if (Object.values($cards).some(({ pid, tid }) => isSchedule($state.projects[pid]?.tasks?.[tid] ?? {}))) A('div font-size:0.85em fg:$s-muted mt:$2 #Scheduled');
	});
	drawCards($cards, ['plan'], key, isSchedule);
}

/** A branch commit as the daemon sends it: its task, where it is a task's landing. */
type Commit = { sha: string; subject: string; tid?: string };

/** Where on the branch to deploy up to, and whether that takes back what the deploy holds. */
type DeployTo = { sha: string; back: boolean };

/**
 * Done of a project that deploys: its branch's commits, newest on top — a
 * task's as its card, the user's own as a line each, more than three of those
 * in a row folded into one — cut by the line where the last deploy is. Each
 * wears the rocket that deploys the branch up to it, but the one the deploy is
 * at already, and none while a deploy runs or where TPS does not deploy.
 * Answers the tasks it drew.
 */
function drawBranch(pid: string, $p: any, key: string): Set<string> {
	const commits: Commit[] = $p.commits ?? [];
	let line = commits.findIndex(c => c.sha === $p.deploy.at);
	if (line < 0) line = commits.length;
	const drawn = new Set<string>();
	let run: Commit[] = [];
	const rocketless = !$p.deploy.declared || deploying($p);
	const to = (c: Commit, n: number): DeployTo | undefined => rocketless || n === line ? undefined : { sha: c.sha, back: n > line };
	const flush = (upTo: number) => {
		if (run.length) drawCommits(pid, run, upTo - run.length, to);
		run = [];
	};
	commits.forEach((c, n) => {
		if (n === line) {
			flush(n);
			drawDeployLine($p.deploy.rewritten);
		}
		const $t = c.tid && !drawn.has(c.tid) ? $p.tasks?.[c.tid] : undefined;
		if ($t?.phase !== 'done') return void run.push(c);
		flush(n);
		drawn.add(c.tid!);
		if (passesFilter($t, key)) drawCard(pid, c.tid!, $t, to(c, n));
	});
	flush(commits.length);
	if (line === commits.length) drawDeployLine($p.deploy.rewritten);
	return drawn;
}

// The runs of the user's own commits the user unfolded, by project and first commit.
const $unfolded = A.proxy<Record<string, boolean>>({});

/** A run of the user's own commits, the first at place `from` on the branch: a line each, or one for all of them. */
function drawCommits(pid: string, run: Commit[], from: number, to: (c: Commit, n: number) => DeployTo | undefined): void {
	const key = `${pid}/${run[0].sha}`;
	if (run.length > 3 && !$unfolded[key]) {
		A(`div ${commitLine}`, 'click=', () => { $unfolded[key] = true; }, () => {
			gitCommitVertical({ size: '1em' });
			A('span text=', `${run.length} commits`);
			S.addTooltip({ placement: 'left', tip: () => A('div white-space:pre-wrap text=', run.map(c => c.subject).join('\n')) });
		});
		return;
	}
	run.forEach((c, i) => {
		const deploy = to(c, from + i);
		A(`div ${commitLine}`, deploy ? deployHere : '', () => {
			gitCommitHorizontal({ size: '1em' });
			A(`span ${ELLIPSIS} text=`, c.subject);
			S.addTooltip({ placement: 'left', tip: () => A('text=', `${c.sha.slice(0, 10)} ${c.subject}`) }); // not over its rocket
			if (deploy) drawDeployHere(pid, deploy);
		});
	});
}

// A commit of the user's own: a quiet line between the cards.
const commitLine = 'display:flex align-items:center gap:$2 ph:$2 font-size:0.85em fg:$s-muted cursor:default';

/** Done's line between what is merged and what the last deploy holds — wherever a rewrite of the branch left it. */
function drawDeployLine(rewritten: boolean): void {
	A('div display:flex align-items:center gap:$1 fg:$s-success font-size:0.85em', () => {
		if (rewritten) S.addTooltip({ tip: 'The branch was rewritten since the last deploy, which went out as it was then: the line is after the newest task it held.' });
		A('div flex:1 border-top: 1px solid currentColor;');
		chevronsDown({ size: '1em' });
		A('span text=', rewritten ? 'Deployed (before a rewrite)' : 'Deployed');
		chevronsDown({ size: '1em' });
		A('div flex:1 border-top: 1px solid currentColor;');
	});
}

/** The rocket hanging above a card or commit, which deploys the branch up to it (see deployHere). */
function drawDeployHere(pid: string, to: DeployTo): void {
	A('div.tps-deploy-here', () => S.iconButton({ icon: rocket, attrs: '.small fg:$s-success',
		ariaLabel: 'Deploy up to here', tooltip: to.back ? 'Deploy up to here, taking back what came after it' : 'Deploy up to here',
		click: (e: Event) => { e.stopPropagation(); void deployProject(pid, to.sha); } }));
}

/**
 * The cards of some phases that `only` lets through, and the column's
 * filter, oldest change at the bottom — or in the order `order` gives.
 */
function drawCards($cards: Record<string, Card>, phases: Phase[], key: string, only?: ($t: any, tid: string) => boolean, order = changeOrder): void {
	A.onEach($cards, ({ pid, tid }) => {
		const $t = $state.projects[pid]?.tasks?.[tid];
		if (!$t || !phases.includes($t.phase)) return; // each card lives in its phase's column
		if ((only && !only($t, tid)) || !passesFilter($t, key)) return;
		drawCard(pid, tid, $t);
	}, order);
}

function changeOrder({ pid, tid }: Card): number[] {
	return phaseOrder($state.projects[pid]?.tasks?.[tid] ?? {}, tid);
}

/** A merge card's place: the last queued on top, the one merging at the bottom. */
function queueOrder({ pid, tid }: Card): number[] {
	return [-($state.projects[pid]?.queue ?? []).indexOf(tid), ...changeOrder({ pid, tid })];
}

/**
 * A card dragged onto a card of the merge queue of the same project: the
 * dragged one goes above it in the queue (after it, in the order they merge
 * in) when dropped on its upper half, and below it otherwise. Answers whether
 * that is what the drop was.
 */
function dropInQueue(e: DragEvent, pid: string, tid: string): boolean {
	const [p, moved] = e.dataTransfer?.getData('text/tps').split('/') ?? [];
	const queue: string[] = A.peek(() => $state.projects[pid]?.queue ?? []);
	if (p !== pid || moved === tid || !queue.includes(moved) || !queue.includes(tid)) return false;
	const box = (e.currentTarget as HTMLElement).getBoundingClientRect();
	const rest = queue.filter(q => q !== moved);
	const index = rest.indexOf(tid) + (e.clientY < box.top + box.height / 2 ? 1 : 0);
	void cmd('moveInQueue', { pid, tid: moved, index });
	return true;
}

// The rocket above a card or commit of the branch: hidden until that is
// pointed at, it sits on a dashed line in the gap above it, which is where the
// deploy line would move to.
const deployHere = A.insertCss({
	'&': 'position:relative',
	'& > .tps-deploy-here': 'position:absolute left:0 right:0 height:0 z-index:1 opacity:0 top: calc(var(--m3) / -2); border-top: 1px dashed $s-success; transition: opacity 0.15s;',
	'&:hover > .tps-deploy-here': 'opacity:1',
	'& > .tps-deploy-here > *': 'position:absolute left:50% top:0 transform: translate(-50%, -50%); bg:$s-bg',
});

/**
 * Whether a card's icon would say more than the column it sits in: an agent
 * asked for the task and the user has yet to look at it, the task is waiting
 * (for the tasks it follows, or for claude's usage limit), it is being merged
 * among the ones already merged, it keeps work the others in its column
 * do not (see keepsWork), or its workspace is in trouble — which the icon
 * carries as its colour. Everything else the icon could show is the phase, and
 * that is the column's to say.
 */
function saysMore(pid: string, $t: any): boolean {
	return requested($t) || autoStarts($t) || !!$t.limitUntil || $t.phase === 'merge' || keepsWork($t) || taskActivity(pid, $t).color === 'danger';
}

// Browsers can fire a click on the card a drag started from once that drag
// ends, which would open the task the user just dropped elsewhere. Any real
// click starts with a pointerdown, so that is where the flag is cleared.
let dragged = false;

function drawCard(pid: string, tid: string, $t: any, deploy?: DeployTo): void {
	A(`div draggable=${!isSchedule($t)}`, deploy ? deployHere : '',
		'pointerdown=', () => { dragged = false; },
		'dragstart=', (e: DragEvent) => { dragged = true; e.dataTransfer?.setData('text/tps', `${pid}/${tid}`); },
		'drop=', (e: DragEvent) => {
			if (A.peek($t, 'phase') !== 'merge' || !dropInQueue(e, pid, tid)) return;
			e.preventDefault();
			e.stopPropagation(); // not a move to the column's phase as well
		},
		'click=', () => { if (!dragged) void route.go(pathTo(pid, tid)); },
		() => {
			S.addContextMenu({ link: pathTo(pid, tid), get items(): S.MenuEntry[] {
				return taskMenuItems(pid, tid, $t, [{ label: 'Settings…', icon: settings, click: () => taskSettingsDialog(pid, tid, $t) }]);
			}});
			// The title runs on as text does, so a long one takes the card's whole
			// width: what goes with it sits inline, at its start and its end.
			S.box({ attrs: `cursor:pointer border-left: 3px solid ${projectColor($state.projects[pid])};`, contentAttrs: 'display:flex flex-direction:column gap:$1', content: () => {
				A('div', () => {
					A(() => { if (saysMore(pid, $t)) A('span vertical-align:-0.15em mr:$1', () => drawTaskIcon(pid, $t, { color: keepsWork($t) ? '$s-warning' : undefined })); });
					// The space after it, rather than a margin on what follows, is where
					// the line may break, and is gone where it does.
					A('span text=', taskTitle($t) + ' ');
					A(() => {
						const cost = costText($t);
						if (cost) A('small display:inline-block fg:$s-muted mr:$2 text=', cost);
					});
					A(() => {
						if (!contextSlices($t)) return;
						A('small display:inline-flex vertical-align:-0.15em mr:$2 fg:$s-muted', () => {
							S.addTooltip({ tip: () => drawContextTip($t) });
							drawContextRing($t, '1.1em');
						});
					});
					A('span display:inline-flex vertical-align:-0.15em', () => drawLiveLink($t));
				});
				A(() => {
					if (isSchedule($t)) A('small fg:$s-muted text=', scheduleText($t));
					else if (autoStarts($t)) A('small text=', taskActivity(pid, $t).text);
					else if ($t.openrouterAsk) A('small fg:$s-warning #⚠ Asks for OpenRouter budget');
					else if ($t.suggestion) A('small fg:$s-warning #⚠ Suggests project settings');
					else if (waitsForHuman($t) && $t.review) A('small fg:$s-warning #⚠ Review feedback to weigh');
					else if (canMerge(pid, $t)) A('small fg:$s-success #✔ Ready to merge');
					else if ($t.phase === 'merge') A('small fg:$s-muted text=', mergeText($t));
					else if (waitsForHuman($t) && $t.behind) A('small fg:$s-muted text=', `↓ ${$t.behind} behind ${$state.projects[pid]?.defaultBranch ?? 'main'}`);
				});
			}});
			// After the box, which a margin would push down were it not the first child.
			if (deploy) drawDeployHere(pid, deploy);
		});
}
