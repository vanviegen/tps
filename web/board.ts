import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { funnel, plus, settings, x } from 'staffa/icons.js';
import { $state } from './conn.ts';
import { addTask, moveTask, taskMenuItems, taskSettingsDialog } from './task.ts';
import { autoStarts, COLUMNS, costText, drawLiveLink, drawTaskIcon, pathTo, phaseOrder, PHASE_LABELS, taskActivity, taskTitle, waitsForHuman, type Phase } from './util.ts';

/**
 * The project's board: a box per column, a card dropped anywhere in a box's
 * body. Its notices and settings live in the left column. The width the
 * columns share is defined once, so they can't drift apart.
 *
 * Two columns hold more than their own phase, the extra one under a marker:
 * Human has the muted tasks at its foot (still waiting for you, just not out
 * loud), and Done has the ones closed without merging. Merging is a moment on
 * the way to Done rather than a place of its own, so those cards simply sit at
 * the top of the Done column, their icon turning until it is over.
 *
 * A column is one drop area, marker and all: which of its parts a card lands
 * on says nothing, as muting and closing are not things to do by accident.
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
			if (tid && $p.tasks[tid]) void moveTask(pid, tid, $p.tasks[tid], phase);
		},
		() => {
			// The ones on their way to Done go above the ones that got there.
			if (phase === 'done') drawCards(pid, $p, 'merge', key);
			drawCards(pid, $p, phase, key);
			if (phase === 'human') drawSection(pid, $p, 'muted', key);
			if (phase === 'done') drawSection(pid, $p, 'closed', key);
		});
}

/**
 * The cards of one phase, oldest change at the bottom, and only the ones the
 * column's filter lets through; `dim` is for the half that should not draw the
 * eye.
 */
function drawCards(pid: string, $p: any, phase: Phase, key: string, dim = false): void {
	A.onEach($p.tasks, ($t: any, tid: string) => {
		if ($t.phase !== phase) return; // each card lives in its phase's column
		if (!passesFilter($t, key)) return;
		drawCard(pid, tid, $t, dim);
	}, phaseOrder);
}

/**
 * The quieter half of a column: its cards under a marker naming them, and
 * nothing at all while there are none of them — an empty column says enough
 * by being empty. The marker sits right under the cards above it, in the same
 * rhythm, so it reads as a line between them rather than as a second column.
 */
function drawSection(pid: string, $p: any, phase: Phase, key: string): void {
	// Whether there is anything to head is derived into a flag of its own, so a
	// task arriving or leaving doesn't rebuild every card below the marker. A
	// filter counts here too: a marker over nothing would be a line to nowhere.
	const $any = A.proxy({ value: false });
	A(() => { $any.value = Object.values($p.tasks as Record<string, any>).some($t => $t.phase === phase && passesFilter($t, key)); });
	A(() => {
		if (!$any.value) return;
		A('div display:flex align-items:center gap:$2 fg:$s-muted font-size:0.8em', () => {
			A('span flex:none text=', PHASE_LABELS[phase]);
			A('span flex:1 h:1px bg:$s-faint');
		});
	});
	drawCards(pid, $p, phase, key, true);
}

// Browsers can fire a click on the card a drag started from once that drag
// ends, which would open the task the user just dropped elsewhere. Any real
// click starts with a pointerdown, so that is where the flag is cleared.
let dragged = false;

function drawCard(pid: string, tid: string, $t: any, dim: boolean): void {
	A(`div draggable=true${dim ? ' opacity:0.6' : ''}`,
		'pointerdown=', () => { dragged = false; },
		'dragstart=', (e: DragEvent) => { dragged = true; e.dataTransfer?.setData('text/tps', tid); },
		'click=', () => { if (!dragged) void route.go(pathTo(pid, tid)); },
		() => {
			S.addContextMenu({ link: pathTo(pid, tid), get items(): S.MenuEntry[] {
				return taskMenuItems(pid, tid, $t, [{ label: 'Settings…', icon: settings, click: () => taskSettingsDialog(pid, tid, $t) }]);
			}});
			S.box({ attrs: 'cursor:pointer', contentAttrs: 'display:flex flex-direction:column gap:$1', content: () => {
				A('div display:flex align-items:center gap:$2 font-weight:600', () => {
					drawTaskIcon(pid, $t);
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
						if (autoStarts($t)) A('small text=', '⏳ ' + taskActivity(pid, $t).text);
						else if (waitsForHuman($t) && $t.rebasing) A('small fg:$s-warning text=', $t.rebaseOnly ? '⚠ rebase paused' : '⚠ merge paused');
						else if (waitsForHuman($t) && $t.commitMessage) A('small fg:$s-success #✔ ready to merge');
						else if (waitsForHuman($t) && $t.behind) A('small fg:$s-muted text=', `↓ ${$t.behind} behind ${$state.projects[pid]?.defaultBranch ?? 'main'}`);
					});
				});
			}});
		});
}
