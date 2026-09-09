import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { plus, trash2 } from 'staffa/icons.js';
import { drawCode } from './code.ts';
import { $state } from './conn.ts';
import { humanTasks } from './task.ts';
import { cmd, drawBadge, ELLIPSIS, hostName, pathTo } from './util.ts';

/** Projects: the ones with a task waiting for a human first, then most recently active. */
export function sortedProjects(): [string, any][] {
	const projects = Object.entries($state.projects ?? {}) as [string, any][];
	return projects.sort((a, b) =>
		(humanTasks(b[0]) ? 1 : 0) - (humanTasks(a[0]) ? 1 : 0) ||
		(b[1].activity ?? 0) - (a[1].activity ?? 0) ||
		(a[1].name < b[1].name ? -1 : 1));
}

/** Drop a project from the list, once confirmed; used from its row and from its settings. */
async function removeProject(pid: string, $p: any): Promise<boolean> {
	const host = A.peek(() => hostName($p.host));
	if (!(await S.confirm(`Remove **${A.peek($p, 'name')}** from the list? Nothing changes on ${host}: its tasks keep running there, and come back when you add the project again.`))) return false;
	if (!(await cmd('removeProject', { pid }))) return false;
	route.go('/');
	return true;
}

/** The right column at "/": every listed project, plus the way to add one. */
export function drawProjectList(): void {
	A('div display:flex flex-direction:column gap:$3 max-width:44rem', () => {
		A(() => {
			const projects = sortedProjects();
			if (!projects.length) return;
			S.box({ header: 'Projects', contentAttrs: 'p:0', content: () => {
				for (const [pid, $p] of projects) drawProjectRow(pid, $p);
			}});
		});
		drawAddProject();
	});
}

// Rows are flush against each other; the last one leaves the box's edge alone.
const projectRow = A.insertCss({
	'&': 'display:flex align-items:center gap:$2 p:$2',
	'&:not(:last-child)': 'border-bottom: 1px solid $s-faint;',
});

/** One project: its name and what waits there, over where it lives and how busy it is. */
function drawProjectRow(pid: string, $p: any): void {
	A('div', projectRow, () => {
		// The remove button sits beside the link, not inside it.
		A('a flex:1 min-width:0 display:flex flex-direction:column gap:$1', 'href=', pathTo(pid), () => {
			A('div display:flex align-items:center gap:$2 min-width:0', () => {
				A(`span ${ELLIPSIS} text=`, A.ref($p, 'name'));
				A(() => drawBadge(humanTasks(pid)));
				A(() => {
					const $h = $state.hosts?.[$p.host];
					const problem = $p.error || ($h && $h.status !== 'connected' ? `${$h.name}: ${$h.status}` : '');
					if (problem) A('span fg:$s-danger #⚠', () => S.addTooltip({ tip: problem }));
				});
			});
			A(() => {
				const where = $p.host === 'local' ? $p.dir : `${hostName($p.host)}:${$p.dir}`;
				const open = openTasks(pid);
				A(`small ${ELLIPSIS} fg:$s-muted text=`, where + (open ? ` · ${open} open task${open > 1 ? 's' : ''}` : ''));
			});
		});
		S.iconButton({ icon: trash2, ariaLabel: 'Remove from list', attrs: '.small', click: () => void removeProject(pid, $p) });
	});
}

/** Tasks of a project that aren't merged away yet. */
function openTasks(pid: string): number {
	return (Object.values($state.projects[pid]?.tasks ?? {}) as any[]).filter($t => $t.phase !== 'done').length;
}

/** The way to add a project: a bare directory, listed underneath the others. */
function drawAddProject(): void {
	const $form = A.proxy({ spec: '', name: '' });
	S.box({ header: 'Add project', content: () => {
		S.form({
			submit: async () => {
				if (!$form.spec.trim()) return;
				const result = await cmd('addProject', { spec: $form.spec.trim(), name: $form.name.trim() });
				if (result) route.go(pathTo(result.pid));
			},
			content: () => {
				A('p rich=', 'A directory holding a git repository: on this machine, or as `host:directory` on a host you reach over SSH (`user@host`, or an alias from `~/.ssh/config`; options like `-p 2222` go in front). TPS installs its daemon on a new host, and work there goes on while this dashboard is closed.');
				S.autocomplete({
					label: 'Repository', placeholder: '~/projects/app, or user@host:~/projects/app',
					bind: A.ref($form, 'spec'),
					options: () => (Object.values($state.hosts ?? {}) as any[]).filter($h => $h.dest).map($h => $h.dest + ':'),
				});
				S.textline({ label: 'Name', help: 'What to call it in the list; the directory name by default.', bind: A.ref($form, 'name') });
			},
			actions: () => S.button({ content: 'Add', icon: plus, type: 'submit' }),
		});
	}});
}

/** A strip per thing worth knowing: the host's state, the project, its checkout. */
export function drawNotices(pid: string, $p: any): void {
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

/** The name, the merge behaviour, the host it lives on, and the way off the list. */
export function projectSettingsDialog(pid: string, $p: any): void {
	void S.dialog({ header: 'Project settings', attrs: 'w:36rem', content: close => {
		S.textline({
			label: 'Name', value: A.peek($p, 'name') ?? '',
			change: (e: Event) => {
				const name = (e.target as HTMLInputElement).value.trim();
				if (name) void cmd('renameProject', { pid, name });
			},
		});
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
		A('div display:flex mt:$2', () => S.button({
			content: 'Remove from list', attrs: '.small .danger .outlined',
			click: async () => { if (await removeProject(pid, $p)) close(); },
		}));
	}});
}

/** The right column for the base worktree: VS Code on the project's own checkout. */
export function drawProjectCode(pid: string, $p: any): void {
	const start = () => void cmd('openProjectCode', { pid });
	A(start);
	A(() => {
		if ($p.codePort) {
			// code-server's remote authority is the Host header, which the proxy passes on unchanged.
			drawCode(`${pid}/-`, `/code/${pid}/-/?folder=${encodeURIComponent($p.dir)}`);
			return;
		}
		S.box({ contentAttrs: 'display:flex flex-direction:column align-items:flex-start', content: () => {
			if ($p.codeError) A('p fg:$s-danger text=', $p.codeError);
			else { A('p#Starting VS Code on the checkout…'); A('progress w:100%'); }
			S.button({ content: 'Retry', attrs: '.small .neutral', click: start });
		}});
	});
}
