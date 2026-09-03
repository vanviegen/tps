import A from 'aberdeen';
import * as S from 'staffa';
import { circleStop, settings, sparkles } from 'staffa/icons.js';
import { deleteTask } from './board.ts';
import { $state, watchTask } from './conn.ts';
import { drawTerminal } from './term.ts';
import { cmd, drawLiveLink, PHASE_LABELS, PHASES, taskActivity } from './util.ts';

type TaskPanel = S.Panel<{ pid: string; tid: string }>;

/**
 * The task page: VS Code taking all the space it can get, with a narrow agent
 * column beside it: one compact control line, then the live terminal.
 */
export function drawTask($panel: TaskPanel): void {
	const { pid, tid } = $panel.params;
	$panel.maxWidth = 'none';
	watchTask(pid, tid);
	void cmd('openTask', { pid, tid });
	A('p:$2');
	A(() => {
		const $t = $state.projects[pid]?.tasks?.[tid];
		if (!$t) {
			if ($state.ready) S.box({ header: 'Task not found', content: 'This task does not exist (anymore).' });
			return;
		}
		A(() => { $panel.title = $t.title; });
		A('div.tps-task', () => {
			A('div.tps-main-col', () => drawVscode(pid, tid, $t, $panel));
			A('div.tps-agent-col.s-s.neutral', () => drawAgentColumn(pid, tid, $t));
		});
	});
}

function drawVscode(pid: string, tid: string, $t: any, $panel: TaskPanel): void {
	const $mode = A.derive(() => $t.status === 'up' ? 'up' : $t.status === 'error' ? 'error' : 'busy');
	const $reviewing = A.derive(() => $t.phase === 'review');
	A(() => {
		if ($mode.value === 'up') {
			// The payload makes code-server open the file whose turn it is: the
			// review notes during human review, the task description otherwise.
			const file = $reviewing.value ? 'REVIEW.md' : 'TASK.md';
			const payload = JSON.stringify([['openFile', `vscode-remote://${location.host}/work/.tps/${file}`]]);
			const iframe = A('iframe.tps-vscode', { allow: 'clipboard-read; clipboard-write' },
				'src=', `/code/${pid}/${tid}/?folder=/work&payload=${encodeURIComponent(payload)}`) as HTMLIFrameElement;
			iframe.addEventListener('load', () => iframe.focus());
			A(() => { // hand keyboard focus to the editor whenever the panel (re)appears
				if ($panel.visible) setTimeout(() => iframe.focus(), 100);
			});
		} else if ($mode.value === 'error') {
			S.box({ contentAttrs: 'display:flex flex-direction:column gap:$2 align-items:flex-start', content: () => {
				A('p m:0 fg:$s-danger', () => A(`#Workspace error: ${$t.statusDetail || 'unknown'}`));
				S.button({ content: 'Retry', click: () => void cmd('openTask', { pid, tid }) });
			}});
		} else {
			// An open task always has its workspace loading or loaded.
			A(() => { if ($t.status === 'down') void cmd('openTask', { pid, tid }); });
			S.box({ contentAttrs: 'display:flex flex-direction:column gap:$2', content: () => {
				A('p m:0', () => A(`#${$t.statusDetail || $t.status}…`));
				A('progress w:100%');
				A('p fg:$s-muted font-size:0.85em m:0 #The first start builds the container image, which can take a few minutes. Progress shows in the terminal on the right.');
			}});
		}
	});
}

function drawAgentColumn(pid: string, tid: string, $t: any): void {
	A('div.tps-agent-head', () => {
		A('div.tps-state', () => {
			const activity = taskActivity($t);
			A('span.tps-dot', `bg:$s-${activity.color}`);
			A(`span fg:$s-${activity.color} text=`, activity.text);
			drawLiveLink($t);
		});
		S.select({
			attrs: 'flex-shrink:0',
			options: PHASES.map(phase => ({ value: phase, label: PHASE_LABELS[phase] })),
			bind: {
				get value() { return $t.phase; },
				set value(phase: string) {
					if (phase && phase !== A.peek($t, 'phase')) void cmd('moveTask', { pid, tid, phase });
				},
			},
		});
		A(() => {
			if ($t.agent !== 'idle') {
				S.iconButton({ icon: circleStop, ariaLabel: 'Stop agent', attrs: '.small fg:$s-danger',
					click: () => void cmd('stopAgent', { pid, tid }) });
			}
		});
		S.iconButton({ icon: settings, ariaLabel: 'Task settings', attrs: '.small',
			click: () => settingsDialog(pid, tid, $t) });
	});
	drawTerminal(pid, tid);
}

function settingsDialog(pid: string, tid: string, $t: any): void {
	const models = () => ['none', ...$state.models];
	void S.dialog({ header: 'Task settings', content: () => {
		A('display:flex flex-direction:column gap:$3');
		S.select({
			label: 'Implementation model', options: models,
			help: "With 'none', you implement the task yourself in VS Code.",
			bind: {
				get value() { return $t.implementModel ?? 'sonnet'; },
				set value(implementModel: string) { if (implementModel) void cmd('setTaskConfig', { pid, tid, implementModel }); },
			},
		});
		S.select({
			label: 'Review model', options: models,
			help: "Reviews as part of the Implement phase, into .tps/REVIEW.md. With 'none', reviewing is all yours.",
			bind: {
				get value() { return $t.reviewModel ?? 'none'; },
				set value(reviewModel: string) { if (reviewModel) void cmd('setTaskConfig', { pid, tid, reviewModel }); },
			},
		});
		let cyclesTimer: ReturnType<typeof setTimeout>;
		S.textline({
			label: 'Maximum automatic review cycles', type: 'number',
			help: 'How often reviewer feedback may trigger another implementer run before the task goes to human review.',
			value: String(A.peek($t, 'maxReviewCycles') ?? 1),
			input: e => {
				const maxReviewCycles = Math.max(0, parseInt((e.target as HTMLInputElement).value) || 0);
				clearTimeout(cyclesTimer);
				cyclesTimer = setTimeout(() => {
					if (maxReviewCycles !== A.peek($t, 'maxReviewCycles')) void cmd('setTaskConfig', { pid, tid, maxReviewCycles });
				}, 600);
			},
		});
		S.checkbox({
			label: 'Skip human review',
			help: 'Merge to done automatically once implementation (and agent review, if any) is finished.',
			checked: !!A.peek($t, 'skipHumanReview'),
			change: e => void cmd('setTaskConfig', { pid, tid, skipHumanReview: (e.target as HTMLInputElement).checked }),
		});
		drawDependencies(pid, tid, $t);
		A('hr border:0 border-top: 1px solid $s-faint; w:100% m:0');
		A('div display:flex gap:$2', () => {
			A(() => {
				if ($t.phase === 'plan' && $t.agent === 'idle' && $t.implementModel !== 'none') {
					S.button({ content: 'Refine plan', icon: sparkles, attrs: '.small',
						click: () => void cmd('runPlanAgent', { pid, tid }) });
				}
			});
			S.button({ content: 'Delete task', attrs: '.danger .outlined .small', click: () => void deleteTask(pid, tid, $t) });
		});
	}});
}

function drawDependencies(pid: string, tid: string, $t: any): void {
	const $p = $state.projects[pid];
	const deps = (): string[] => ($t.dependencies ?? []) as string[];
	const depTitle = (ref: string) => $p?.tasks?.[ref.replace(/^tps\//, '')]?.title ?? ref;
	A('div display:flex flex-direction:column gap:$1', () => {
		S.select({
			label: 'Dependencies', placeholder: 'Add dependency…',
			help: 'Tasks that must reach Done before this one starts.',
			options: () => Object.keys($p?.tasks ?? {})
				.filter(other => other !== tid && !deps().includes('tps/' + other))
				.map(other => ({ value: 'tps/' + other, label: $p.tasks[other].title })),
			bind: {
				get value() { return ''; },
				set value(ref: string) {
					if (ref) void cmd('setTaskConfig', { pid, tid, dependencies: [...deps(), ref] });
				},
			},
		});
		A(() => {
			if (!deps().length) return;
			A('div display:flex flex-wrap:wrap gap:$1', () => {
				for (const ref of deps()) {
					A('span.tps-chip', () => {
						A('span text=', depTitle(ref));
						A('button.tps-chip-x title=Remove #×', 'click=', () =>
							void cmd('setTaskConfig', { pid, tid, dependencies: deps().filter(d => d !== ref) }));
					});
				}
			});
		});
		S.checkbox({
			label: 'Start implementation when dependencies are done',
			help: 'Combined with "Skip human review" this makes a fully unsupervised chain.',
			checked: !!A.peek($t, 'startWhenDepsDone'),
			change: e => void cmd('setTaskConfig', { pid, tid, startWhenDepsDone: (e.target as HTMLInputElement).checked }),
		});
	});
}
