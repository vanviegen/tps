import A from 'aberdeen';
import { go } from 'aberdeen/route';
import * as S from 'staffa';
import { plus } from 'staffa/icons.js';
import { $state } from './conn.ts';
import { cmd } from './util.ts';

/** The '/' route: straight to the first project's board, or a welcome when there is none. */
export function drawHome($panel: S.Panel): void {
	$panel.title = 'TPS';
	A(() => {
		$panel.loading = !$state.ready;
		if (!$state.ready) return;
		const projects = Object.entries($state.projects ?? {}) as [string, any][];
		if (projects.length) {
			projects.sort((a, b) => (a[1].name < b[1].name ? -1 : 1));
			setTimeout(() => void $panel.open(`/p/${projects[0][0]}`), 0);
			return;
		}
		S.box({ header: 'Welcome to TPS', content: () => {
			A('p#TPS runs AI coding agents on kanban tasks. Each task works in its own clone of your repository, inside a podman dev container, and is merged back when done.');
			A('p#Add a project to get started: any local directory containing a git repository.');
			S.button({ content: 'Add project', icon: plus, click: () => addProjectDialog() });
		}});
	});
}

export function addProjectDialog(): void {
	const $form = A.proxy({ dir: '', name: '' });
	void S.dialog({ header: 'Add project', content: close => {
		S.form({
			submit: async () => {
				const result = await cmd('addProject', { dir: $form.dir, name: $form.name || undefined });
				if (result) {
					close();
					go(`/p/${result.pid}`);
				}
			},
			content: () => {
				S.textline({ label: 'Git project directory', required: true, placeholder: '~/projects/my-app', bind: A.ref($form, 'dir') });
				S.textline({ label: 'Name', help: 'Defaults to the directory name.', bind: A.ref($form, 'name') });
			},
			actions: () => S.button({ content: 'Add project', type: 'submit' }),
		});
	}});
}
