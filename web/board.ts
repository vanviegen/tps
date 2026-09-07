import A from 'aberdeen';
import { go } from 'aberdeen/route';
import * as S from 'staffa';
import { container, plus, settings } from 'staffa/icons.js';
import { $state } from './conn.ts';
import { cmd, costText, drawLiveLink, drawTaskIcon, PHASES, PHASE_LABELS, taskActivity, tidOrder, type Phase } from './util.ts';

export function drawBoard($panel: S.Panel<{ pid: string }>): void {
	$panel.maxWidth = 'none';
	const pid = $panel.params.pid;
	A('display:flex flex-direction:column gap:$3');
	A(() => {
		$panel.loading = !$state.ready;
		const $p = $state.projects[pid];
		if (!$p) {
			if ($state.ready) S.box({ header: 'Unknown project', content: 'This project is not registered (anymore).' });
			return;
		}
		$panel.title = $p.name;
		$panel.actions = () => S.iconButton({ icon: settings, ariaLabel: 'Project settings', attrs: '.small',
			click: () => projectSettingsDialog(pid, $p) });
		A(() => {
			const $h = $state.hosts?.[$p.host];
			if (!$h || $h.status === 'connected') return;
			A('div.s-s.danger.tonal p:$2 display:flex align-items:center gap:$2', () => {
				A('span flex:1 text=', `${$h.dest}: ${$h.status}${$h.error ? ' · ' + $h.error : ''}`);
				S.button({ content: 'Connect', attrs: '.small', click: () => void cmd('connectHost', { hid: $p.host }) });
			});
		});
		if ($p.dirty) {
			const where = $p.host && $p.host !== 'local' ? ` on ${$state.hosts?.[$p.host]?.dest ?? $p.host}` : '';
			A('div.s-s.warning.tonal p:$2 rich=', `⚠ **Uncommitted changes** in \`${$p.dir}\`${where}. Task workspaces clone the committed state only, and merging may be blocked.`);
		}
		if ($p.needsSetup) drawSetup(pid, $p);
		else drawColumns(pid, $p, $panel);
	});
}

/** Move a task to a phase, asking for confirmation when that discards work. */
export async function moveTask(pid: string, tid: string, $t: any, phase: Phase): Promise<void> {
	if (phase === 'plan' && $t.phase !== 'plan') {
		if (!(await S.confirm('Move this task back to **Plan**? All work is discarded: the workspace, the chat, and every unmerged change.'))) return;
	}
	void cmd('moveTask', { pid, tid, phase });
}

/** One box per phase; a card is dropped anywhere in a box's body. */
function drawColumns(pid: string, $p: any, $panel: S.Panel): void {
	A('div display:flex gap:$3 align-items:stretch overflow-x:auto flex:1', () => {
		for (const phase of PHASES) {
			S.box({
				attrs: 'flex: 1 0 250px; max-width:340px mt:0 min-height:14rem',
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
		'click=', () => void $panel.open(`/p/${pid}/t/${tid}/${$t.phase === 'plan' ? 'settings' : 'agent'}`),
		() => {
			S.addContextMenu({ items: [
				...PHASES.filter(p => p !== $t.phase).map(p => ({
					label: `Move to ${PHASE_LABELS[p]}`,
					click: () => void moveTask(pid, tid, $t, p),
				})),
				{ separator: true as const },
				{ label: 'Delete task', attrs: 'fg:$s-danger', click: () => void deleteTask(pid, tid, $t) },
			]});
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

export async function deleteTask(pid: string, tid: string, $t: any): Promise<boolean> {
	if (!(await S.confirm(`Delete **${$t.title || 'this task'}**? This removes the task, its workspace and its container. Merged work stays merged.`))) return false;
	void cmd('deleteTask', { pid, tid });
	return true;
}

/** Create an empty task and jump straight into its plan page. */
async function newTask(pid: string, $panel: S.Panel): Promise<void> {
	const result = await cmd('createTask', { pid });
	if (result) void $panel.open(`/p/${pid}/t/${result.tid}/settings`);
}

function projectSettingsDialog(pid: string, $p: any): void {
	void S.dialog({ header: 'Project settings', content: close => {
		S.checkbox({
			label: 'Merge without confirmation',
			help: 'When the agent reports a task ready, merge it right away instead of waiting for you. Individual tasks can override this in their settings.',
			checked: !!A.peek($p, 'autoMerge'),
			change: (e: Event) => void cmd('setProject', { pid, autoMerge: (e.target as HTMLInputElement).checked }),
		});
		A('div display:flex justify-content:flex-end', () => {
			S.button({ content: 'Remove project from TPS', attrs: '.danger .outlined .small', click: async () => {
				if (await S.confirm(`Remove **${$p.name}** from TPS? The repository is left untouched, but all tasks and their workspaces are forgotten.`)) {
					close();
					if (await cmd('removeProject', { pid })) go('/');
				}
			}});
		});
	}});
}

function drawSetup(pid: string, $p: any): void {
	const $selected: Record<string, boolean> = A.proxy({});
	A.peek(() => { for (const id of $p.detected ?? []) $selected[id] = true; });
	S.box({
		header: 'Set up the dev container',
		contentAttrs: 'display:flex flex-direction:column',
		content: () => {
			A('p rich=', 'This project has no `Containerfile.dev` yet. TPS will generate one (Debian with code-server and the claude CLI) and commit it to the repository root. Toolchains detected in the repository are preselected:');
			A('div display:flex flex-direction:column gap:$1', () => {
				A.onEach($state.tools, ($tool: any) => {
					S.checkbox({ label: $tool.label, bind: A.ref($selected, $tool.id) });
				});
			});
		},
		footer: () => S.button({ content: 'Create & commit Containerfile.dev', icon: container, click: () => {
			void cmd('createContainerfile', { pid, tools: Object.keys($selected).filter(k => $selected[k]) });
		}}),
	});
}
