import A from 'aberdeen';
import { go } from 'aberdeen/route';
import * as S from 'staffa';
import { plus } from 'staffa/icons.js';
import { $state } from './conn.ts';
import { cmd, hostName } from './util.ts';

/** Projects, most recently active first. */
export function sortedProjects(): [string, any][] {
	const projects = Object.entries($state.projects ?? {}) as [string, any][];
	return projects.sort((a, b) => (b[1].activity ?? 0) - (a[1].activity ?? 0) || (a[1].name < b[1].name ? -1 : 1));
}

/** The '/' route: straight to the most recently active project, or a welcome when there is none. */
export function drawHome($panel: S.Panel): void {
	$panel.title = 'TPS';
	A(() => {
		$panel.loading = !$state.ready;
		if (!$state.ready) return;
		const first = A.peek(() => sortedProjects()[0]);
		if (first) {
			setTimeout(() => void $panel.open(`/p/${first[0]}`, 'replace'), 0);
			return;
		}
		S.box({ header: 'Welcome to TPS', content: () => {
			A('p#TPS runs AI coding agents on kanban tasks. Each task works in its own clone of your repository, inside a podman dev container, and is merged back when done.');
			A('p#Add a project to get started: a directory containing a git repository, on this machine or on a host you reach over SSH.');
			S.button({ content: 'Add project', icon: plus, click: () => addProjectDialog() });
		}});
	});
}

/** One line names the repository: a directory here, or host:directory over SSH. */
export function addProjectDialog(): void {
	void S.dialog({ header: 'Add project', content: close => {
		let input: HTMLInputElement | null = null;
		const submit = async () => {
			const spec = input?.value.trim();
			if (!spec) return;
			const result = await cmd('addProject', { spec });
			if (result) {
				close();
				go(`/p/${result.pid}`);
			}
		};
		const wrap = A('div display:flex flex-direction:column gap:$3', () => {
			A('p rich=', 'A directory holding a git repository: on this machine, or as `host:directory` on a host you reach over SSH (`user@host`, or an alias from `~/.ssh/config`; options like `-p 2222` go in front). TPS installs its daemon on a new host, and work there goes on while this dashboard is closed.');
			S.autocomplete({
				label: 'Repository', placeholder: '~/projects/app, or user@host:~/projects/app',
				options: () => (Object.values($state.hosts ?? {}) as any[]).filter($h => $h.dest).map($h => $h.dest + ':'),
			});
			A('div display:flex justify-content:flex-end', () => S.button({ content: 'Add project', click: () => void submit() }));
		}) as HTMLElement;
		input = wrap.querySelector('input');
		// Enter adds the project, unless the autocomplete is offering a host to pick.
		wrap.addEventListener('keydown', e => {
			if (e.key !== 'Enter' || wrap.querySelector('.s-option:not(.s-add)')) return;
			e.preventDefault();
			e.stopPropagation();
			void submit();
		}, true);
	}});
}

export async function renameProject(pid: string, $p: any): Promise<void> {
	const name = await S.prompt('Name for this project in the list:', A.peek($p, 'name'));
	if (name?.trim()) void cmd('renameProject', { pid, name: name.trim() });
}

export async function removeProject(pid: string, $p: any): Promise<void> {
	const host = A.peek(() => hostName($p.host));
	if (!(await S.confirm(`Remove **${A.peek($p, 'name')}** from the list? Nothing changes on ${host}: its tasks keep running there, and come back when you add the project again.`))) return;
	if (!await cmd('removeProject', { pid })) return;
	const base = `/p/${pid}`;
	if (location.pathname === base || location.pathname.startsWith(base + '/')) go('/');
}
