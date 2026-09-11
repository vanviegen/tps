import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { plus, settings } from 'staffa/icons.js';
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
					if (phase === 'plan') S.iconButton({ icon: plus, ariaLabel: 'Add task', attrs: '.small ml:auto', click: () => void addTask(pid) });
				},
				content: () => drawColumn(pid, $p, phase),
			});
		}
	});
}

/**
 * One column: the cards it holds, and the empty room under them — all of it
 * taking a card dropped on it, which moves that card's task to the phase the
 * column is named after.
 */
function drawColumn(pid: string, $p: any, phase: Phase): void {
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
			if (phase === 'done') drawCards(pid, $p, 'merge');
			drawCards(pid, $p, phase);
			if (phase === 'human') drawSection(pid, $p, 'muted');
			if (phase === 'done') drawSection(pid, $p, 'closed');
		});
}

/** The cards of one phase, oldest change at the bottom; `dim` is for the half that should not draw the eye. */
function drawCards(pid: string, $p: any, phase: Phase, dim = false): void {
	A.onEach($p.tasks, ($t: any, tid: string) => {
		if ($t.phase !== phase) return; // each card lives in its phase's column
		drawCard(pid, tid, $t, dim);
	}, phaseOrder);
}

/**
 * The quieter half of a column: its cards under a marker naming them, and
 * nothing at all while there are none of them — an empty column says enough
 * by being empty. The marker sits right under the cards above it, in the same
 * rhythm, so it reads as a line between them rather than as a second column.
 */
function drawSection(pid: string, $p: any, phase: Phase): void {
	// Whether there is anything to head is derived into a flag of its own, so a
	// task arriving or leaving doesn't rebuild every card below the marker.
	const $any = A.proxy({ value: false });
	A(() => { $any.value = Object.values($p.tasks as Record<string, any>).some($t => $t.phase === phase); });
	A(() => {
		if (!$any.value) return;
		A('div display:flex align-items:center gap:$2 fg:$s-muted font-size:0.8em', () => {
			A('span flex:none text=', PHASE_LABELS[phase]);
			A('span flex:1 h:1px bg:$s-faint');
		});
	});
	drawCards(pid, $p, phase, true);
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
