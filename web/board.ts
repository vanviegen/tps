import A from 'aberdeen';
import * as S from 'staffa';
import { gitBranch, plus, settings, squareCode } from 'staffa/icons.js';
import { $state } from './conn.ts';
import { moveTask, taskActions } from './task.ts';
import { cmd, costText, drawLiveLink, drawTaskIcon, ELLIPSIS, hostName, PHASES, PHASE_LABELS, taskActivity, tidOrder } from './util.ts';

/**
 * One board column, and the width the strips above the board take: two columns
 * plus the gap between them. Defined once, so the two rows can't drift apart.
 */
const boardWidths = A.insertCss({
	'&': `--col: clamp(190px, calc((100% - ${PHASES.length - 1} * var(--m3)) / ${PHASES.length}), 320px);`
		+ ' --strip: calc(2 * var(--col) + var(--m3));',
});

export function drawBoard($panel: S.Panel<{ pid: string }>): void {
	$panel.maxWidth = 'none';
	const pid = $panel.params.pid;
	A('display:flex flex-direction:column gap:$3', boardWidths);
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
		A('div display:flex flex-wrap:wrap gap:$3', () => {
			drawRepoBox(pid, $p, $panel);
			drawNotices($p);
		});
		drawColumns(pid, $p, $panel);
	});
}

/** What stands between the user and the board: the host's state, or a project that failed to register. */
function drawNotices($p: any): void {
	const strip = (color: string, text: string, action?: () => void) =>
		A(`div.s-s.${color}.tonal p:$2 w:var(--strip) display:flex align-items:center gap:$2`, () => {
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
}

/** Where the project's own checkout lives, what git makes of it, and the way into VS Code on it. */
function drawRepoBox(pid: string, $p: any, $panel: S.Panel): void {
	A(() => {
		const dirty = !!$p.dirty;
		A(`div.s-s.${dirty ? 'warning.tonal' : 'neutral'} p:$2 w:var(--strip) display:flex align-items:center gap:$2 cursor:pointer`,
			'click=', () => void $panel.open(`/p/${pid}/code`),
			() => {
				S.addTooltip({ tip: dirty
					? 'Task workspaces clone the committed state only, so this work stays out of them and may block a merge. Click to open VS Code on the checkout.'
					: 'Open VS Code on this checkout' });
				squareCode({ size: '2em', attrs: 'flex-shrink:0 fg:$s-accent' });
				A('div flex:1 min-width:0 font-size:0.9em', () => {
					// The directory gives way first: the host and the branch are short, and cutting them loses more.
					A('div display:flex align-items:baseline gap:$1', () => {
						A('b flex-shrink:0 text=', hostName($p.host));
						A(`span ${ELLIPSIS} min-width:0 fg:$s-muted font-family:monospace text=`, $p.dir);
						A(() => {
							if (!$p.branch) return;
							A('span flex-shrink:0 display:flex align-items:center gap:$0 font-family:monospace', () => {
								gitBranch({ size: '1em' });
								A('text=', $p.branch);
							});
						});
					});
					A('div display:flex align-items:baseline gap:$1', () => {
						A('span flex-shrink:0 #git:');
						A(() => {
							if ($p.git == null) A('span fg:$s-muted #checking…');
							else if (dirty) A(`span ${ELLIPSIS} min-width:0 text=`, `${$p.git} · not in task workspaces`);
							else { A('span flex-shrink:0 fg:$s-success #✔'); A('span #Clean'); }
						});
					});
				});
			});
	});
}

/** VS Code on the project's own checkout, served by the daemon that owns it. */
export function drawProjectCode($panel: S.Panel<{ pid: string }>): void {
	const pid = $panel.params.pid;
	$panel.maxWidth = 'none';
	$panel.title = 'Code';
	A('display:flex flex-direction:column');
	A(() => {
		$panel.loading = !$state.ready;
		const $p = $state.projects[pid];
		if (!$p) {
			if ($state.ready) S.box({ header: 'Unknown project', content: 'This project is not in the list (anymore).' });
			return;
		}
		const start = () => void cmd('openProjectCode', { pid });
		A(start);
		A(() => {
			if (!$p.codePort) {
				S.box({ contentAttrs: 'display:flex flex-direction:column align-items:flex-start', content: () => {
					if ($p.codeError) A('p fg:$s-danger text=', $p.codeError);
					else { A('p#Starting VS Code on the checkout…'); A('progress w:100%'); }
					S.button({ content: 'Retry', attrs: '.small .neutral', click: start });
				}});
				return;
			}
			A('p:0'); // edge to edge
			// code-server's remote authority is the Host header, which the proxy passes on unchanged.
			const src = `/code/${pid}/-/?folder=${encodeURIComponent($p.dir)}`;
			const iframe = A('iframe flex:1 w:100% border:0', { allow: 'clipboard-read; clipboard-write' }, 'src=', src) as HTMLIFrameElement;
			iframe.addEventListener('load', () => iframe.focus());
			A(() => { if ($panel.visible) setTimeout(() => iframe.focus(), 100); });
		});
	});
}

/** One box per phase; a card is dropped anywhere in a box's body. */
function drawColumns(pid: string, $p: any, $panel: S.Panel): void {
	A('div display:flex gap:$3 align-items:stretch overflow-x:auto flex:1', () => {
		for (const phase of PHASES) {
			S.box({
				attrs: 'w:var(--col) flex:none mt:0 min-height:14rem',
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
