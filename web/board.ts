import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { funnel, plus, settings, x } from 'staffa/icons.js';
import { $state } from './conn.ts';
import { addTask, moveTask, taskMenuItems, taskSettingsDialog } from './task.ts';
import { autoStarts, COLUMNS, contextSlices, costText, drawContextRing, drawContextTip, drawLiveLink, drawTaskIcon, keepsWork, pathTo, phaseOrder, PHASE_ICONS, PHASE_LABELS, taskActivity, taskTitle, waitsForHuman, type Phase } from './util.ts';

/**
 * The project's board: a box per column, a card dropped anywhere in a box's
 * body. Its notices and settings live in the left column. The width the
 * columns share is defined once, so they can't drift apart.
 *
 * The column says what phase its cards are in — its header wears the phase's
 * icon — so a card repeats none of that: it carries a glyph only where it has
 * something else to say (see saysMore).
 *
 * Two columns hold more than their own phase, the extra cards wearing the icon
 * that tells them apart. Plan has the started (muted) tasks at its top: put
 * away with their work kept, to be taken up again. Done has the ones closed
 * without merging mixed in with the merged ones. Merging is a moment on the
 * way to Done rather than a place of its own, so those cards simply sit at the
 * top of the Done column, wearing the merge icon until it is over.
 *
 * A column is one drop area: where in it a card lands says nothing, as muting
 * and closing are not things to do by accident.
 */
const boardWidths = A.insertCss({
	'&': `--col: clamp(190px, calc((100% - ${COLUMNS.length - 1} * var(--m3)) / ${COLUMNS.length}), 320px);`,
});

/**
 * What each column is filtered by, keyed by project and phase. Module state,
 * as a filter outlives the popup it was typed in — and outlives leaving the
 * board, which its lit funnel is there to say on the way back.
 */
const $filters = A.proxy<Record<string, string>>({});

/** The filter a column goes by, as its key into `$filters`. */
function filterKey(pid: string, phase: Phase): string {
	return `${pid}:${phase}`;
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

export function drawBoard(pid: string, $p: any): void {
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
						if (phase === 'plan') S.iconButton({ icon: plus, ariaLabel: 'Create task', attrs: '.small', click: () => void addTask(pid) });
						drawFilterButton(pid, phase);
					});
				},
				content: () => drawColumn(pid, $p, phase),
			});
		}
	});
}

/**
 * The column's filter: a funnel that opens a field to type in, filtering the
 * cards below as the letters arrive. Dismissing the popup leaves the filter
 * standing — emptying the field is what ends one — so the funnel lights up for
 * as long as there is something typed, and says what in its tooltip.
 */
function drawFilterButton(pid: string, phase: Phase): void {
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
function drawColumn(pid: string, $p: any, phase: Phase): void {
	const key = filterKey(pid, phase);
	// The gap is the one the box's body would have given the cards, now that
	// they hang in here rather than directly in it.
	A('div display:flex flex-direction:column flex:1 gap:$3',
		'dragover=', (e: DragEvent) => e.preventDefault(),
		'drop=', (e: DragEvent) => {
			e.preventDefault();
			const tid = e.dataTransfer?.getData('text/tps');
			// A started card let go in Plan is already where it sits.
			if (!tid || !$p.tasks[tid] || (phase === 'plan' && $p.tasks[tid].phase === 'muted')) return;
			void moveTask(pid, tid, $p.tasks[tid], phase);
		},
		() => {
			// The ones on their way to Done go above the ones that got there, and
			// the started ones above the ones never begun.
			if (phase === 'done') drawCards(pid, $p, ['merge'], key);
			if (phase === 'plan') drawCards(pid, $p, ['muted'], key);
			drawCards(pid, $p, phase === 'done' ? ['done', 'closed'] : [phase], key);
		});
}

/**
 * The cards of some phases, oldest change at the bottom, and only the ones the
 * column's filter lets through.
 */
function drawCards(pid: string, $p: any, phases: Phase[], key: string): void {
	A.onEach($p.tasks, ($t: any, tid: string) => {
		if (!phases.includes($t.phase)) return; // each card lives in its phase's column
		if (!passesFilter($t, key)) return;
		drawCard(pid, tid, $t);
	}, phaseOrder);
}

/**
 * Whether a card's icon would say more than the column it sits in: the task is
 * waiting (for the tasks it follows, or for claude's usage limit), it is being
 * merged among the ones already merged, it keeps work the others in its column
 * do not (see keepsWork), or its workspace is in trouble — which the icon
 * carries as its colour. Everything else the icon could show is the phase, and
 * that is the column's to say.
 */
function saysMore(pid: string, $t: any): boolean {
	return autoStarts($t) || !!$t.limitUntil || $t.phase === 'merge' || keepsWork($t) || taskActivity(pid, $t).color === 'danger';
}

// Browsers can fire a click on the card a drag started from once that drag
// ends, which would open the task the user just dropped elsewhere. Any real
// click starts with a pointerdown, so that is where the flag is cleared.
let dragged = false;

function drawCard(pid: string, tid: string, $t: any): void {
	A('div draggable=true',
		'pointerdown=', () => { dragged = false; },
		'dragstart=', (e: DragEvent) => { dragged = true; e.dataTransfer?.setData('text/tps', tid); },
		'click=', () => { if (!dragged) void route.go(pathTo(pid, tid)); },
		() => {
			S.addContextMenu({ link: pathTo(pid, tid), get items(): S.MenuEntry[] {
				return taskMenuItems(pid, tid, $t, [{ label: 'Settings…', icon: settings, click: () => taskSettingsDialog(pid, tid, $t) }]);
			}});
			S.box({ attrs: 'cursor:pointer', contentAttrs: 'display:flex flex-direction:column gap:$1', content: () => {
				A('div display:flex align-items:center gap:$2 font-weight:600', () => {
					A(() => { if (saysMore(pid, $t)) drawTaskIcon(pid, $t, { color: keepsWork($t) ? '$s-warning' : undefined }); });
					A('span flex:1 text=', taskTitle($t));
					drawLiveLink($t);
				});
				A('div display:flex flex-wrap:wrap gap:$2', () => {
					A('small text=', A.ref($t, 'model'));
					A(() => {
						const cost = costText($t);
						if (cost) A('small text=', cost);
					});
					A(() => {
						if (!contextSlices($t)) return;
						A('small display:inline-flex align-items:center fg:$s-muted', () => {
							S.addTooltip({ tip: () => drawContextTip($t) });
							drawContextRing($t, '1.1em');
						});
					});
					A(() => {
						if (autoStarts($t)) A('small text=', taskActivity(pid, $t).text);
						else if (waitsForHuman($t) && $t.review) A('small fg:$s-warning #⚠ review feedback to weigh');
						else if (waitsForHuman($t) && $t.ready) A('small fg:$s-success #✔ ready to merge');
						else if (waitsForHuman($t) && $t.behind) A('small fg:$s-muted text=', `↓ ${$t.behind} behind ${$state.projects[pid]?.defaultBranch ?? 'main'}`);
					});
				});
			}});
		});
}
