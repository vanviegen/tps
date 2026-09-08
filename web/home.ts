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
			A('p#Add a project to get started: a directory containing a git repository, on this machine or on a host you reach over SSH.');
			A('div display:flex gap:$2', () => {
				S.button({ content: 'Add project', icon: plus, click: () => addProjectDialog() });
				S.button({ content: 'Add host', attrs: '.outlined', click: () => addHostDialog() });
			});
		}});
	});
}

export function addProjectDialog(hid = 'local'): void {
	const $form = A.proxy({ dir: '', name: '', host: hid });
	void S.dialog({ header: 'Add project', content: close => {
		S.form({
			submit: async () => {
				const result = await cmd('addProject', { dir: $form.dir, name: $form.name || undefined, host: $form.host });
				if (result) {
					close();
					go(`/p/${result.pid}`);
				}
			},
			content: () => {
				A(() => {
					const hosts = Object.entries($state.hosts ?? {}) as [string, any][];
					if (hosts.length < 2) return;
					S.select({ label: 'Host', options: hosts.map(([hid, $h]) => ({ value: hid, label: $h.dest })), bind: A.ref($form, 'host') });
				});
				S.textline({ label: 'Git project directory', required: true, placeholder: '~/projects/my-app', help: 'A path on the selected host.', bind: A.ref($form, 'dir') });
				S.textline({ label: 'Name', help: 'Defaults to the directory name.', bind: A.ref($form, 'name') });
			},
			actions: () => S.button({ content: 'Add project', type: 'submit' }),
		});
	}});
}

/** Register an SSH host; the server connects (asking for logins as needed) before accepting it. */
export function addHostDialog(): void {
	const $form = A.proxy({ dest: '', busy: false });
	void S.dialog({ header: 'Add host', content: close => {
		S.form({
			submit: async () => {
				$form.busy = true;
				const result = await cmd('addHost', { dest: $form.dest });
				$form.busy = false;
				if (result) {
					close();
					S.toast({ message: `Connected to ${$form.dest}`, type: 'success' });
				}
			},
			content: () => {
				A('p#A machine you can reach over SSH that has podman and git. TPS installs its daemon there; projects on that host keep running while this UI is closed.');
				S.textline({ label: 'SSH destination', required: true, placeholder: 'user@host, or an alias from ~/.ssh/config',
					help: 'Whatever you would type after `ssh`; options like `-p 2222` or `-J jumphost` in front of the host work too.', bind: A.ref($form, 'dest') });
				A(() => { if ($form.busy) A('p fg:$s-muted #Connecting… answer any login prompts that appear.'); });
			},
			actions: () => A(() => S.button({ content: 'Connect and add', type: 'submit', disabled: $form.busy })),
		});
	}});
}
