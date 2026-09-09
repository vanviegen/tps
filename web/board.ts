import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { plus, settings } from 'staffa/icons.js';
import { $state } from './conn.ts';
import { moveTask, newTaskDialog, phaseItems, taskSettingsDialog } from './task.ts';
import { cmd, costText, drawLiveLink, drawTaskIcon, pathTo, PHASES, PHASE_LABELS, taskActivity, tidOrder } from './util.ts';

/** The project's board: what stands in its way, then a column per phase. */
export function drawBoard(pid: string, $p: any): void {
	A('div display:flex flex-direction:column gap:$3 h:100%', () => {
		drawNotices(pid, $p);
		drawColumns(pid, $p);
	});
}

/** A strip per thing worth knowing: the host's state, the project, its checkout. */
function drawNotices(pid: string, $p: any): void {
	const strip = (color: string, text: string, action?: () => void) =>
		A(`div.s-s.${color}.tonal p:$2 display:flex align-items:center gap:$2`, () => {
			A('span flex:1 rich=', text);
			action?.();
		});
	A(() => {
		const $h = $state.hosts?.[$p.host];
		if (!$h) return;
		if ($h.status !== 'connected') {
			strip('danger', `${$h.name}: ${$h.status}${$h.error ? ' · ' + $h.error : ''}`,
				() => S.button({ content: 'Connect', attrs: '.small', click: () => void cmd('connectHost', { hid: $p.host }) }));
		} else if ($p.error) {
			strip('danger', `${$p.dir}: ${$p.error}`);
		} else if ($h.warning) {
			strip('warning', $h.warning);
		} else if ($h.restarting) {
			strip('neutral', `The daemon on ${$h.name} restarts into this build as soon as nothing is running.`);
		} else if ($h.updatable) {
			strip('neutral', `The daemon on ${$h.name} runs another build of TPS.`,
				() => S.button({ content: 'Update daemon', attrs: '.small', click: () => void cmd('updateDaemon', { hid: $p.host }) }));
		}
	});
	A(() => {
		if (!$p.dirty) return;
		strip('warning', `Uncommitted work in \`${$p.dir}\` (${$p.git}). Task workspaces clone the committed state only, so this stays out of them and may block a merge.`,
			() => S.button({ content: 'Open', attrs: '.small', click: () => void route.go(pathTo(pid, 'base')) }));
	});
}

/**
 * One box per phase; a card is dropped anywhere in a box's body. The width the
 * columns share is defined once, so they can't drift apart.
 */
const boardWidths = A.insertCss({
	'&': `--col: clamp(190px, calc((100% - ${PHASES.length - 1} * var(--m3)) / ${PHASES.length}), 320px);`,
});

function drawColumns(pid: string, $p: any): void {
	A('div display:flex gap:$3 align-items:stretch overflow-x:auto flex:1', boardWidths, () => {
		for (const phase of PHASES) {
			S.box({
				attrs: 'w:var(--col) flex:none mt:0 min-height:14rem',
				contentAttrs: 'flex:1 display:flex flex-direction:column',
				header: () => {
					A('text=', PHASE_LABELS[phase]);
					if (phase === 'plan') S.iconButton({ icon: plus, ariaLabel: 'Add task', attrs: '.small ml:auto', click: () => newTaskDialog(pid) });
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

function drawCard(pid: string, tid: string, $t: any): void {
	A('div draggable=true',
		'dragstart=', (e: DragEvent) => e.dataTransfer?.setData('text/tps', tid),
		'click=', () => void route.go(pathTo(pid, tid)),
		() => {
			S.addContextMenu({ link: pathTo(pid, tid), get items(): S.MenuEntry[] {
				return [...phaseItems(pid, tid, $t), { separator: true },
					{ label: 'Settings…', icon: settings, click: () => taskSettingsDialog(pid, tid, $t) }];
			}});
			S.box({ attrs: 'cursor:pointer', contentAttrs: 'display:flex flex-direction:column gap:$1', content: () => {
				A('div display:flex align-items:center gap:$2 font-weight:600', () => {
					drawTaskIcon(pid, $t);
					A('span flex:1 text=', $t.title || '(untitled)');
					drawLiveLink($t);
				});
				A('div display:flex flex-wrap:wrap gap:$2', () => {
					A('small text=', A.ref($t, 'model'));
					A(() => {
						const cost = costText($t);
						if (cost) A('small text=', cost);
					});
					A(() => {
						if ($t.waiting) A('small text=', '⏳ ' + taskActivity(pid, $t).text);
						else if ($t.phase === 'human' && $t.rebasing) A('small fg:$s-warning #⚠ merge paused');
						else if ($t.phase === 'human' && $t.commitMessage) A('small fg:$s-success #✔ ready to merge');
					});
				});
			}});
		});
}
