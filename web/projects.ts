import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { plus } from 'staffa/icons.js';
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

/** One row of the project selector: its name, dimly its host, a sign when something is wrong. */
export function projectItem(pid: string, $p: any): S.MenuItem {
	return {
		href: pathTo(pid),
		label: () => {
			A(`span flex:1 ${ELLIPSIS} text=`, A.ref($p, 'name'));
			A(() => {
				const $h = $state.hosts?.[$p.host];
				if ($p.host !== 'local' && $h?.name) A(`small ${ELLIPSIS} fg:$s-muted text=`, $h.name);
				const problem = $p.error || ($h && $h.status !== 'connected' ? `${$h.name}: ${$h.status}` : '');
				if (problem) A('span fg:$s-danger #⚠', () => S.addTooltip({ tip: problem }));
			});
			A(() => drawBadge(humanTasks(pid)));
		},
	};
}

/** The right column while no project is picked: one line names the repository. */
export function drawAddProject(): void {
	const $form = A.proxy({ spec: '', name: '' });
	S.box({ attrs: 'max-width:44rem', header: 'Add project', content: () => {
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
			click: async () => {
				const host = A.peek(() => hostName($p.host));
				if (!(await S.confirm(`Remove **${A.peek($p, 'name')}** from the list? Nothing changes on ${host}: its tasks keep running there, and come back when you add the project again.`))) return;
				close();
				if (await cmd('removeProject', { pid })) route.go('/');
			},
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
