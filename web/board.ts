import A from 'aberdeen';
import * as S from 'staffa';
import { container, plus } from 'staffa/icons.js';
import { $state } from './conn.ts';
import { cmd, drawLiveLink, PHASES, PHASE_LABELS } from './util.ts';

export function drawBoard($panel: S.Panel<{ pid: string }>): void {
	$panel.maxWidth = 'none';
	const pid = $panel.params.pid;
	A('display:flex flex-direction:column gap:$3 h:100%');
	A(() => {
		const $p = $state.projects[pid];
		if (!$p) {
			if ($state.ready) S.box({ header: 'Unknown project', content: 'This project is not registered (anymore).' });
			return;
		}
		$panel.title = $p.name;
		if ($p.dirty) {
			A('div.tps-warn rich=', `⚠ **Uncommitted changes** in \`${$p.dir}\`. Creating tasks and merging to done are blocked until the project worktree is clean.`);
		}
		if ($p.needsSetup) drawSetup(pid, $p);
		else drawColumns(pid, $p, $panel);
	});
}

function drawColumns(pid: string, $p: any, $panel: S.Panel): void {
	A('div.tps-board', () => {
		for (const phase of PHASES) {
			A('div.tps-col.s-s.neutral',
				'dragover=', (e: DragEvent) => e.preventDefault(),
				'drop=', (e: DragEvent) => {
					e.preventDefault();
					const tid = e.dataTransfer?.getData('text/tps');
					if (tid) void cmd('moveTask', { pid, tid, phase });
				},
				() => {
					A('div.tps-col-head', () => {
						A(`h4#${PHASE_LABELS[phase]}`);
						if (phase === 'plan') S.iconButton({ icon: plus, ariaLabel: 'New task', attrs: '.small', click: () => newTaskDialog(pid, $panel) });
					});
					A('div.tps-col-cards', () => {
						A.onEach($p.tasks, ($t: any, tid: string) => {
							if ($t.phase !== phase) return; // each card lives in its phase's column
							drawCard(pid, tid, $t, $panel);
						}, (_$t: any, tid: string) => tid);
					});
				});
		}
	});
}

function drawCard(pid: string, tid: string, $t: any, $panel: S.Panel): void {
	A('div.tps-card.s-s.neutral draggable=true',
		'dragstart=', (e: DragEvent) => e.dataTransfer?.setData('text/tps', tid),
		'click=', () => void $panel.open(`/p/${pid}/t/${tid}`),
		() => {
			S.addContextMenu({ items: [
				...PHASES.filter(p => p !== $t.phase).map(p => ({
					label: `Move to ${PHASE_LABELS[p]}`,
					click: () => void cmd('moveTask', { pid, tid, phase: p }),
				})),
				{ separator: true as const },
				{ label: 'Delete task', attrs: 'fg:$s-danger', click: () => void deleteTask(pid, tid, $t) },
			]});
			A('div.tps-card-title', () => {
				A('span flex:1 text=', A.ref($t, 'title'));
				drawLiveLink($t);
				drawStatusDot($t);
			});
			A('div.tps-badges', () => {
				if ($t.implementModel !== 'none') A('span.tps-badge text=', A.ref($t, 'implementModel'));
				if ($t.reviewModel !== 'none') A('span.tps-badge#🤖 review');
				if ($t.skipHumanReview) A('span.tps-badge#⏩ auto-done');
				if ($t.hasQuestions) A('span.tps-badge.is-warn#❓ questions');
				if ($t.agent && $t.agent !== 'idle') A(`span.tps-badge.is-run#⚙ ${$t.agent}`);
			});
		});
}

/** A container's lifecycle, shown subtly: just a colored dot. */
function drawStatusDot($t: any): void {
	A(() => {
		const s = $t.status;
		const color = s === 'up' ? 'success' : s === 'error' ? 'danger'
			: (s === 'building' || s === 'starting' || s === 'stopping') ? 'warning' : undefined;
		if (color) A('span.tps-dot', `bg:$s-${color}`, 'title=', `workspace: ${s}`);
	});
}

export async function deleteTask(pid: string, tid: string, $t: any): Promise<void> {
	if (await S.confirm(`Delete **${$t.title}**? This removes the task branch, its worktree and its container.`)) {
		void cmd('deleteTask', { pid, tid });
	}
}

function newTaskDialog(pid: string, $panel: S.Panel): void {
	const $form = A.proxy({ title: '', description: '' });
	void S.dialog({ header: 'New task', content: close => {
		S.form({
			submit: async () => {
				const result = await cmd('createTask', { pid, title: $form.title, description: $form.description });
				if (result) {
					close();
					void $panel.open(`/p/${pid}/t/${result.tid}`);
				}
			},
			content: () => {
				S.textline({ label: 'Title', required: true, bind: A.ref($form, 'title') });
				S.textarea({
					label: 'Description', rows: 6, bind: A.ref($form, 'description'),
					help: 'Becomes .tps/TASK.md, to be refined during planning by you and the agents.',
				});
			},
			actions: () => S.button({ content: 'Create task', type: 'submit' }),
		});
	}});
}

function drawSetup(pid: string, $p: any): void {
	const $selected: Record<string, boolean> = A.proxy({});
	A.peek(() => { for (const id of $p.detected ?? []) $selected[id] = true; });
	S.box({ header: 'Set up the dev container', contentAttrs: 'max-width:44rem', content: () => {
		A('p rich=', 'This project has no `.tps/Containerfile` yet. TPS will generate one (Debian with code-server and the claude CLI) and commit it to the default branch. Toolchains detected in the repository are preselected:');
		A('div display:flex flex-direction:column gap:$1 mv:$3', () => {
			A.onEach($state.tools, ($tool: any) => {
				S.checkbox({ label: $tool.label, bind: A.ref($selected, $tool.id) });
			});
		});
		S.button({ content: 'Create & commit Containerfile', icon: container, click: () => {
			void cmd('createContainerfile', { pid, tools: Object.keys($selected).filter(k => $selected[k]) });
		}});
	}});
}
