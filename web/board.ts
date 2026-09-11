import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { plus, settings } from 'staffa/icons.js';
import { $state } from './conn.ts';
import { addTask, moveTask, taskMenuItems, taskSettingsDialog } from './task.ts';
import { autoStarts, COLUMNS, costText, drawLiveLink, drawTaskIcon, pathTo, phaseOrder, PHASE_LABELS, taskActivity, taskTitle, waitsForHuman } from './util.ts';

/**
 * The project's board: a box per column, a card dropped anywhere in a box's
 * body. Its notices and settings live in the left column. The width the
 * columns share is defined once, so they can't drift apart.
 *
 * Two phases share their column with a quieter one below a marker: Human has
 * the muted tasks at its foot (still waiting for you, just not out loud), and
 * Merge has the closed ones under it — merging is a moment, being done is
 * where a task stays. Every area keeps some empty room, so there is always
 * somewhere to drop a card.
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
				content: () => {
					if (phase === 'human') {
						drawStack(pid, $p, 'human', 'min-height:2.5rem flex: 1 0 auto;');
						drawStack(pid, $p, 'muted', 'min-height:2.5rem flex: 1 0 auto;', { label: 'Muted tasks', dim: true });
					} else if (phase === 'merge') {
						// Merging is over in a minute, so this part is mostly the way in:
						// it says what dropping a card here does rather than sit empty.
						drawStack(pid, $p, 'merge', 'flex:none min-height:2.5rem', { placeholder: 'Drag here to merge' });
						drawStack(pid, $p, 'done', 'flex:1 min-height:2.5rem', { label: 'Done' });
						drawStack(pid, $p, 'closed', 'flex:1 min-height:2.5rem', { label: 'Closed', dim: true });
					} else {
						drawStack(pid, $p, phase, 'flex:1 min-height:2.5rem');
					}
				},
			});
		}
	});
}

/**
 * The cards of one phase, in an area that takes a card dropped anywhere on it.
 * A `label` heads the area (the marker a shared column is split by, part of
 * the drop area so the line itself takes a card too), `placeholder` is what an
 * empty area says instead of showing nothing, and `dim` is for the half that
 * should not draw the eye.
 */
function drawStack(pid: string, $p: any, phase: string, attrs: string, opts: { label?: string; placeholder?: string; dim?: boolean } = {}): void {
	A(`div display:flex flex-direction:column ${attrs}`,
		'dragover=', (e: DragEvent) => e.preventDefault(),
		'drop=', (e: DragEvent) => {
			e.preventDefault();
			const tid = e.dataTransfer?.getData('text/tps');
			if (tid && $p.tasks[tid]) void moveTask(pid, tid, $p.tasks[tid], phase);
		},
		() => {
			if (opts.label) A('div display:flex align-items:center gap:$2 mt:$2 mb:$1 fg:$s-muted font-size:0.8em', () => {
				A('span flex:none text=', opts.label);
				A('span flex:1 h:1px bg:$s-faint');
			});
			A(`div display:flex flex-direction:column flex:1${opts.dim ? ' opacity:0.6' : ''}`, () => {
				A.onEach($p.tasks, ($t: any, tid: string) => {
					if ($t.phase !== phase) return; // each card lives in its phase's column
					drawCard(pid, tid, $t);
				}, phaseOrder);
				if (opts.placeholder) A(() => {
					if (Object.values($p.tasks as Record<string, any>).some($t => $t.phase === phase)) return;
					A('div flex:1 display:flex align-items:center justify-content:center text-align:center p:$2 r:$s-radius fg:$s-muted font-size:0.85em',
						'border: 1px dashed $s-faint;', 'text=', opts.placeholder);
				});
			});
		});
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
