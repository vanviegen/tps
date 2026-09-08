import A from 'aberdeen';
import * as S from 'staffa';
import { plus, settings } from 'staffa/icons.js';
import { $state } from './conn.ts';
import { moveTask, taskActions } from './task.ts';
import { cmd, costText, drawLiveLink, drawTaskIcon, hostName, PHASES, PHASE_LABELS, taskActivity, tidOrder } from './util.ts';

export function drawBoard($panel: S.Panel<{ pid: string }>): void {
	$panel.maxWidth = 'none';
	const pid = $panel.params.pid;
	A('display:flex flex-direction:column gap:$3');
	A(() => {
		$panel.loading = !$state.ready;
		const $p = $state.projects[pid];
		if (!$p) {
			if ($state.ready) S.box({ header: 'Unknown project', content: 'This project is not in the list (anymore).' });
			return;
		}
		A(() => { $panel.title = $p.name; });
		$panel.actions = () => S.iconButton({ icon: settings, ariaLabel: 'Project settings', attrs: '.small',
			click: () => projectSettingsDialog(pid, $p) });
		drawNotices($p);
		drawColumns(pid, $p, $panel);
	});
}

/** What stands between the user and the board: the host's state, a failed registration, a dirty checkout. */
function drawNotices($p: any): void {
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
		const where = $p.host !== 'local' ? ` on ${hostName($p.host)}` : '';
		strip('warning', `⚠ **Uncommitted changes** in \`${$p.dir}\`${where}. Task workspaces clone the committed state only, and merging may be blocked.`);
	});
}

/** One box per phase; a card is dropped anywhere in a box's body. */
function drawColumns(pid: string, $p: any, $panel: S.Panel): void {
	A('div display:flex gap:$3 align-items:stretch overflow-x:auto flex:1', () => {
		for (const phase of PHASES) {
			S.box({
				attrs: 'flex:1 min-width:190px max-width:320px mt:0 min-height:14rem',
				contentAttrs: 'flex:1 display:flex flex-direction:column',
				header: () => {
					A('text=', PHASE_LABELS[phase]);
					if (phase === 'plan') S.iconButton({ icon: plus, ariaLabel: 'New task', attrs: '.small ml:auto', click: () => void newTask(pid, $panel) });
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
						drawCard(pid, tid, $t, $panel);
					}, (_$t: any, tid: string) => tidOrder(tid));
				},
			});
		}
	});
}

function drawCard(pid: string, tid: string, $t: any, $panel: S.Panel): void {
	A('div draggable=true',
		'dragstart=', (e: DragEvent) => e.dataTransfer?.setData('text/tps', tid),
		'click=', () => void $panel.open(`/p/${pid}/t/${tid}`),
		() => {
			S.addContextMenu({ get items() {
				const actions = taskActions(pid, tid, $t, $panel);
				const items: S.MenuEntry[] = actions.map(a => ({ label: a.label, icon: a.icon, disabled: a.disabled, attrs: a.danger ? 'fg:$s-danger' : '', click: a.click }));
				items.splice(-1, 0, { separator: true });
				return items;
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
						else if ($t.phase === 'human' && $t.commitMessage) A('small fg:$s-success #✔ ready to merge');
					});
				});
			}});
		});
}

/** Create an empty task and jump straight into its page. */
async function newTask(pid: string, $panel: S.Panel): Promise<void> {
	const result = await cmd('createTask', { pid });
	if (result) void $panel.open(`/p/${pid}/t/${result.tid}`);
}

/** The daemon-side setting, and the host the project lives on. */
function projectSettingsDialog(pid: string, $p: any): void {
	void S.dialog({ header: 'Project settings', content: () => {
		S.checkbox({
			label: 'Merge without confirmation',
			help: 'When the agent reports a task ready, merge it right away instead of waiting for you. Individual tasks can override this in their settings.',
			checked: !!A.peek($p, 'autoMerge'),
			change: (e: Event) => void cmd('setProject', { pid, autoMerge: (e.target as HTMLInputElement).checked }),
		});
		A('div.s-field', () => {
			A('label #Host');
			A(() => {
				const $h = $state.hosts?.[$p.host];
				A('div text=', `${$p.host === 'local' ? 'This machine' : $h?.dest ?? $p.host} · ${$h?.status ?? 'unknown'}`);
				A('div.s-help text=', $p.dir);
				if (!$h) return;
				A('div display:flex flex-wrap:wrap gap:$2 mt:$2', () => {
					if ($p.host !== 'local') S.button({ content: 'Copy claude login to host', attrs: '.small .neutral', click: async () => {
						if (await cmd('copyCredentials', { hid: $p.host })) S.toast({ message: `Your claude login is now on ${$h.name}`, type: 'success' });
					}});
					S.button({ content: 'Stop daemon', attrs: '.small .danger .outlined', click: async () => {
						if (await S.confirm(`Stop the TPS daemon on **${$h.name}**? Its running workspaces are shut down; Connect starts it again.`)) void cmd('stopDaemon', { hid: $p.host });
					}});
				});
			});
		});
	}});
}
