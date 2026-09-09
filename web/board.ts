import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { pencil, plus, settings } from 'staffa/icons.js';
import { addTask, moveTask, phaseItems, renameTask, taskSettingsDialog } from './task.ts';
import { autoStarts, costText, drawLiveLink, drawTaskIcon, pathTo, PHASES, PHASE_LABELS, taskActivity, taskTitle, tidOrder } from './util.ts';

/**
 * The project's board: one box per phase, a card dropped anywhere in a box's
 * body. Its notices and settings live in the left column. The width the
 * columns share is defined once, so they can't drift apart.
 */
const boardWidths = A.insertCss({
	'&': `--col: clamp(190px, calc((100% - ${PHASES.length - 1} * var(--m3)) / ${PHASES.length}), 320px);`,
});

export function drawBoard(pid: string, $p: any): void {
	A('div display:flex gap:$3 align-items:stretch overflow-x:auto h:100%', boardWidths, () => {
		for (const phase of PHASES) {
			S.box({
				attrs: 'w:var(--col) flex:none mt:0 min-height:14rem',
				contentAttrs: 'flex:1 display:flex flex-direction:column',
				header: () => {
					A('text=', PHASE_LABELS[phase]);
					if (phase === 'plan') S.iconButton({ icon: plus, ariaLabel: 'Add task', attrs: '.small ml:auto', click: () => void addTask(pid) });
				},
				content: () => {
					A('dragover=', (e: DragEvent) => e.preventDefault(),
						'drop=', (e: DragEvent) => {
							e.preventDefault();
							const tid = e.dataTransfer?.getData('text/tps');
							if (tid && $p.tasks[tid]) void moveTask(pid, tid, $p.tasks[tid], phase);
						});
					A.onEach($p.tasks, ($t: any, tid: string) => {
						if ($t.phase !== phase) return; // each card lives in its phase's column
						drawCard(pid, tid, $t);
					}, (_$t: any, tid: string) => tidOrder(tid));
				},
			});
		}
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
				return [...phaseItems(pid, tid, $t), { separator: true },
					{ label: 'Rename…', icon: pencil, click: () => void renameTask(pid, tid, $t) },
					{ label: 'Settings…', icon: settings, click: () => taskSettingsDialog(pid, tid, $t) }];
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
						else if ($t.phase === 'human' && $t.rebasing) A('small fg:$s-warning #⚠ merge paused');
						else if ($t.phase === 'human' && $t.commitMessage) A('small fg:$s-success #✔ ready to merge');
					});
				});
			}});
		});
}
