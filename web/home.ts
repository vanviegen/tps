import A from 'aberdeen';
import * as S from 'staffa';
import { folderGit2, plus } from 'staffa/icons.js';
import { $state } from './conn.ts';
import { cmd } from './util.ts';

export function drawHome($panel: S.Panel): void {
	$panel.title = 'Projects';
	$panel.maxWidth = 'medium';
	$panel.actions = () => S.button({ content: 'Add project', icon: plus, attrs: '.small', click: () => addProjectDialog($panel) });
	A('display:flex flex-direction:column gap:$3');
	A(() => {
		if (!$state.connected) {
			S.box('Connecting to the TPS server…');
			return;
		}
		if (A.isEmpty($state.projects)) {
			S.box({ header: 'Welcome to TPS', content: () => {
				A('p#TPS runs AI coding agents on kanban tasks. Each task lives on its own git branch and gets a worktree plus a podman dev container on demand.');
				A('p#Add a project to get started: any local directory containing a git repository.');
				S.button({ content: 'Add project', icon: plus, click: () => addProjectDialog($panel) });
			}});
			return;
		}
		A.onEach($state.projects, ($p: any, pid: string) => {
			S.box({ contentAttrs: 'display:flex align-items:center gap:$3', content: () => {
				folderGit2({ size: '1.6em', color: 'var(--s-accent)' });
				A('div flex:1 min-width:0', () => {
					A('a href=', `/p/${pid}`, () => A('b text=', A.ref($p, 'name')));
					A('div fg:$s-muted font-size:0.85em text=', A.ref($p, 'dir'));
				});
				A('div fg:$s-muted white-space:nowrap', () => {
					const n = Object.keys($p.tasks ?? {}).length;
					A(`#${n} task${n === 1 ? '' : 's'}`);
				});
				S.menuButton({
					button: { content: '⋯', attrs: '.neutral .small', ariaLabel: 'Project actions' },
					items: [{
						label: 'Remove from TPS',
						attrs: 'fg:$s-danger',
						click: async () => {
							if (await S.confirm(`Remove **${$p.name}** from TPS? The repository and its task branches are left untouched.`)) {
								void cmd('removeProject', { pid });
							}
						},
					}],
				});
			}});
		}, ($p: any) => $p.name);
	});
}

function addProjectDialog($panel: S.Panel): void {
	const $form = A.proxy({ dir: '', name: '' });
	void S.dialog({ header: 'Add project', content: close => {
		S.form({
			submit: async () => {
				const result = await cmd('addProject', { dir: $form.dir, name: $form.name || undefined });
				if (result) {
					close();
					void $panel.open(`/p/${result.pid}`);
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
