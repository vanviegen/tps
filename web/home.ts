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

export function addProjectDialog(): void {
	const $form = A.proxy({ dir: '', name: '', host: 'local' });
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
				S.textline({ label: 'SSH destination', required: true, placeholder: 'user@host, or an alias from ~/.ssh/config', bind: A.ref($form, 'dest') });
				A(() => { if ($form.busy) A('p fg:$s-muted #Connecting… answer any login prompts that appear.'); });
			},
			actions: () => A(() => S.button({ content: 'Connect and add', type: 'submit', disabled: $form.busy })),
		});
	}});
}

/** Every host with its connection state, and what can be done about it. */
export function hostsDialog(): void {
	void S.dialog({ header: 'Hosts', attrs: 'w:40rem', content: () => {
		A.onEach($state.hosts, ($h: any, hid: string) => {
			S.box({
				header: () => {
					A('text=', A.ref($h, 'dest'));
					A(() => A(`small ml:auto fg:$s-${$h.status === 'connected' ? 'success' : 'danger'} text=`, $h.status));
				},
				contentAttrs: 'display:flex flex-direction:column gap:$2',
				content: () => {
					A(() => {
						if ($h.error) A('p fg:$s-danger text=', $h.error);
						if ($h.warning) A('p fg:$s-warning text=', $h.warning);
					});
					A('div display:flex gap:$2 flex-wrap:wrap', () => {
						A(() => { if ($h.status !== 'connected') S.button({ content: 'Connect', attrs: '.small', click: () => void cmd('connectHost', { hid }) }); });
						if (hid !== 'local') S.button({ content: 'Copy claude login', attrs: '.small .outlined', click: async () => {
							if (await cmd('copyCredentials', { hid })) S.toast({ message: `Your claude login is now on ${$h.dest}`, type: 'success' });
						}});
						S.button({ content: 'Stop daemon', attrs: '.small .outlined .danger', click: async () => {
							if (await S.confirm(`Stop the TPS daemon on **${$h.dest}**? Its running workspaces are shut down; it starts again when needed.`)) void cmd('stopDaemon', { hid });
						}});
						if (hid !== 'local') S.button({ content: 'Remove', attrs: '.small .outlined .danger', click: async () => {
							if (await S.confirm(`Remove **${$h.dest}**? Its projects disappear from this TPS; nothing changes on the host itself.`)) void cmd('removeHost', { hid });
						}});
					});
				},
			});
		}, ($h: any, hid: string) => hid === 'local' ? '' : $h.dest);
		A('div display:flex justify-content:flex-end', () => S.button({ content: 'Add host…', icon: plus, click: () => addHostDialog() }));
	}});
}
