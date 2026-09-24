import A from 'aberdeen';
import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { check, download, folderGit2, keyRound, plug, plus, power, refreshCw, serverCrash, serverOff, trash2, triangleAlert } from 'staffa/icons.js';
import { showAsk } from './ask.ts';
import { $state } from './conn.ts';
import { addProjectDialog, projectsOn } from './projects.ts';
import { cmd, destName, drawStrip, ELLIPSIS, hostIcon, hostName, hostPath, pathTo, shortDir } from './util.ts';

/**
 * The hosts: the machines projects run on, this one and the ones reached over
 * SSH. They head the sidebar's groups, each saying by its colour what it is
 * doing (see hostState); clicking one opens its page, which is where
 * everything about a host is — what it is doing, where it is, its projects,
 * and what there is to do with it.
 */

/** Where a host is listed: this machine first, the rest by name. */
export function hostOrder(hid: string): string {
	return hid === 'local' ? '' : '1' + hostName(hid).toLowerCase();
}

export type HostState = {
	/** A staffa style — neutral, warning, danger or success — for the strip it is written on. */
	color: string;
	icon: typeof serverCrash;
	/** What the host is doing, or waiting for, in a line. */
	text: string;
	/** Nothing to say: it works and wants nothing. */
	ok: boolean;
	/** The one thing to do about it, where there is one. */
	action?: { label: string; run: () => void };
};

/**
 * What a host is, in one line — the only place that decides. The sidebar row,
 * the host's page and the strip above a project's board all say this, so
 * they say the same thing and offer the same way out of it.
 */
export function hostState(hid: string, $h: any): HostState {
	const connect = { label: 'Connect', run: () => void cmd('connectHost', { hid }) };
	if ($h?.ask) {
		// An unknown host key comes as a paragraph with a fingerprint in it:
		// that belongs in the dialog, where it can be read, not on a row.
		const key = $h.ask.kind === 'confirm';
		return {
			color: 'warning', icon: keyRound, ok: false,
			text: key ? 'This host is not known yet: its key wants checking.' : ($h.ask.text || '').replace(/\s+/g, ' ').trim(),
			action: { label: key ? 'Check key' : 'Log in', run: () => showAsk(hid) },
		};
	}
	switch ($h?.status ?? 'connecting') {
		case 'connected': break;
		case 'connecting': return { color: 'neutral', icon: plug, text: 'connecting…', ok: false };
		case 'updating': return { color: 'neutral', icon: download, text: 'updating the daemon…', ok: false };
		case 'stopped': return { color: 'neutral', icon: serverOff, text: 'the daemon is stopped', ok: false, action: connect };
		default: return { color: 'danger', icon: serverCrash, text: `${$h.status}${$h.error ? ' · ' + $h.error : ''}`, ok: false, action: connect };
	}
	// Connected: whatever the daemon there has to say for itself.
	if ($h.signingIn) return { color: 'warning', icon: keyRound, text: $h.signin, ok: false, action: { label: 'Sign in', run: () => signInDialog(hid) } };
	if ($h.signin || $h.login) return { color: 'warning', icon: keyRound, text: $h.signin || $h.login, ok: false };
	if ($h.warning) return { color: 'warning', icon: triangleAlert, text: $h.warning, ok: false };
	if ($h.restarting) return { color: 'neutral', icon: refreshCw, text: 'the daemon restarts into this build once nothing is running', ok: false };
	if ($h.updatable) {
		return { color: 'neutral', icon: download, text: 'the daemon runs another build of TPS', ok: false,
			action: { label: 'Update daemon', run: () => void cmd('updateDaemon', { hid }) } };
	}
	return { color: 'success', icon: hostIcon(hid), text: 'connected', ok: true };
}

/** The ink a state's colour is written in: there is no $s-neutral, and none is wanted. */
export function hostInk(color: string): string {
	return `$s-${color === 'neutral' ? 'muted' : color}`;
}

/**
 * A host's page: what it is and what it is doing, the projects it holds — with
 * the button that adds one, as that is what a host is for — then the two
 * things it is set by, and the things there are to do with it, each saying
 * what it does beside its button.
 */
export function drawHostPage(hid: string): void {
	A('div display:flex flex-direction:column gap:$3 max-width:52rem', () => {
		A(() => {
			const $h = $state.hosts?.[hid];
			// Removed, here or from another dashboard: there is nothing left to show.
			if (!$h) return void route.go('/');
			const state = hostState(hid, $h);
			A('div display:flex align-items:center gap:$3 flex-wrap:wrap min-width:0', () => {
				A(`span display:inline-flex flex-shrink:0 fg:${hostInk(state.color)}`, () => state.icon({ size: '1.8em' }));
				A('div display:flex flex-direction:column min-width:0 flex:1', () => {
					A(`h2 m:0 font-size:1.15em ${ELLIPSIS} text=`, hostName(hid));
					A(`small ${ELLIPSIS} fg:$s-muted`, () => {
						// Where it is, unless its name says that already.
						const where = hid === 'local' ? 'This machine' : $h.dest ?? hid;
						if (where !== hostName(hid)) A('text=', where + ' · ');
						A(`span fg:${hostInk(state.color)} text=`, state.ok ? 'connected' : state.color === 'neutral' ? state.text : 'needs attention');
					});
				});
				S.button({ content: 'Add project', icon: plus, disabled: $h.status !== 'connected', click: () => addProjectDialog(hid) });
			});
			if (!state.ok) drawStrip(state.color, state.text, () => drawAction(state));
		});
		S.box({ header: 'Projects', attrs: 'mt:0', contentAttrs: 'display:flex flex-direction:column gap:$1', content: () => {
			A(() => {
				const projects = projectsOn(hid);
				if (!projects.length) A('p m:0 fg:$s-muted rich=', 'No projects here yet. A project is a directory holding a git repository on this host: *Add project* picks one, and its board starts out empty.');
				for (const [pid, $p] of projects) drawProjectRow(pid, $p);
			});
		}});
		// Outside the scopes above: a redraw while typing would take the cursor with it.
		drawHostSettings(hid);
		A(() => drawHostActions(hid, $state.hosts?.[hid]?.status));
	});
}

/** The way out of what a host is doing, where its state has one. */
export function drawAction(state: HostState): void {
	if (state.action) S.button({ content: state.action.label, attrs: `.small .${state.color}`, click: state.action.run });
}

/** A project on the host's page: its name and directory, and how many of its tasks are under way. */
function drawProjectRow(pid: string, $p: any): void {
	A('a display:flex align-items:center gap:$2 ph:$2 pv:$1 r:$s-radius-sm text-decoration:none fg:$s-text min-width:0',
		'.tps-hover', 'href=', pathTo(pid), () => {
			A('span display:inline-flex flex-shrink:0 fg:$s-muted', () => folderGit2({ size: '1.1em' }));
			A(`b flex-shrink:0 text=`, $p.name);
			A(`span flex:1 min-width:0 font-size:0.9em fg:$s-muted ${ELLIPSIS} text=`, shortDir($p.host, $p.dir));
			const tasks = Object.values($p.tasks ?? {}) as any[];
			const yours = tasks.filter(t => t.phase === 'human').length;
			const busy = tasks.filter(t => t.phase === 'agent' || t.phase === 'review').length;
			if (busy) A('span flex-shrink:0 font-size:0.85em fg:$s-muted text=', `${busy} with an agent`);
			if (yours) A('span flex-shrink:0 font-size:0.85em fg:$s-warning text=', `${yours} waiting for you`);
		});
}

A.insertGlobalCss({ '.tps-hover:hover': 'background: color-mix(in oklab, $s-text, transparent 92%);' });

/**
 * What the host is set by: the name it is shown by, and the ssh destination it
 * is reached at, with a Save of their own — nothing typed here reaches the host
 * until that is pressed. Saving another destination drops the connection and
 * makes it anew, as if the host were added again: the projects found there take
 * the place of the ones it had. This machine has no destination to point
 * elsewhere, so it only takes a name.
 */
function drawHostSettings(hid: string): void {
	const initial = () => A.peek(() => ({ name: $state.hostNames?.[hid] ?? '', dest: $state.hosts?.[hid]?.dest ?? '' }));
	const $form = A.proxy(initial());
	S.box({ header: 'Settings', attrs: 'mt:0', content: () => {
		S.form({
			submit: async () => {
				const result = await cmd('setHost', { hid, name: $form.name, dest: $form.dest.trim() });
				if (!result) return;
				// Another destination is another host, with a page of its own.
				if (result.hid !== hid) void route.go(hostPath(result.hid));
				else Object.assign($form, initial());
			},
			content: () => {
				S.textline({ label: 'Name', placeholder: A.peek(() => destName(hid)), bind: A.ref($form, 'name'), help: 'What to call it in the sidebar.' });
				if (hid !== 'local') S.textline({
					label: 'SSH destination', placeholder: 'user@host', required: true, bind: A.ref($form, 'dest'),
					help: 'Whatever you would type after `ssh` (options like `-p 2222` go in front). Changing it reconnects, to whatever is there.',
				});
			},
			actions: () => A(() => {
				const was = initial();
				const changed = $form.name !== was.name || $form.dest !== was.dest;
				S.button({ content: 'Save', icon: check, attrs: '.small', type: 'submit', disabled: !changed });
			}),
		});
	}});
}

/**
 * The things to do with a host, each on a line that says what it does. The
 * daemon's own only while it is there to be asked.
 */
function drawHostActions(hid: string, status: string | undefined): void {
	const rows: [string, string, S.ButtonOptions][] = [];
	if (status === 'connected') {
		// Also for a login that still works, to replace it with another account.
		rows.push(['Claude account', 'Sign in on this host, again or with another account.',
			{ content: 'Sign in to claude', icon: keyRound, click: () => signIn(hid) }]);
		rows.push(['Stop the daemon', 'Its running workspaces are shut down; Connect starts it again.',
			{ content: 'Stop daemon', icon: power, click: () => stopDaemon(hid) }]);
	}
	if (hid !== 'local') {
		rows.push(['Remove this host', 'Takes it and its projects off the list; nothing on the host is thrown away.',
			{ content: 'Remove host', icon: trash2, attrs: '.small .danger .tonal', click: () => removeHost(hid) }]);
	}
	if (!rows.length) return;
	S.box({ header: 'Actions', attrs: 'mt:0', contentAttrs: 'display:flex flex-direction:column gap:$3', content: () => {
		for (const [title, text, button] of rows) {
			A('div display:flex align-items:center gap:$3 flex-wrap:wrap', () => {
				A('div flex:1 min-width:12rem display:flex flex-direction:column', () => {
					A('b text=', title);
					A('small fg:$s-muted text=', text);
				});
				S.button({ attrs: '.small .neutral', ...button });
			});
		}
	}});
}

/** What there is to do with a host, as the menu of its row in the sidebar. */
export function hostMenuItems(hid: string, $h: any): S.MenuEntry[] {
	const items: S.MenuEntry[] = [];
	if ($h.status === 'connected') {
		items.push(
			{ label: 'Add project…', icon: plus, click: () => addProjectDialog(hid) },
			{ label: 'Sign in to claude', icon: keyRound, click: () => void signIn(hid) },
			{ label: 'Stop daemon…', icon: power, click: () => void stopDaemon(hid) },
		);
	}
	if (hid !== 'local') {
		if (items.length) items.push({ separator: true });
		items.push({ label: 'Remove host…', icon: trash2, attrs: 'fg:$s-danger', click: () => void removeHost(hid) });
	}
	return items;
}

async function signIn(hid: string): Promise<void> {
	if (await cmd('login', { hid })) signInDialog(hid);
}

/**
 * The sign-in page claude offers, and a field for the code it shows. Where
 * the dashboard runs beside the browser, claude opens a tab of its own that
 * needs no code; either way the dialog goes once the sign-in is over.
 */
function signInDialog(hid: string): void {
	const $form = A.proxy({ code: '' });
	void S.dialog({ header: 'Sign in to claude', attrs: 'w:32rem', content: close => {
		A(() => { if (!$state.hosts?.[hid]?.signingIn) close(); });
		S.form({
			submit: () => { if ($form.code.trim()) void cmd('signInCode', { hid, code: $form.code }); },
			content: () => {
				A('p', () => {
					const url = $state.hosts?.[hid]?.signinURL;
					if (!url) return A('text=', 'Waiting for claude to offer its sign-in page…');
					A('text=', 'Open ');
					A('a target=_blank text="the sign-in page"', 'href=', url);
					A('text=', ', authorise TPS there and paste the code it shows here. A tab claude opened itself finishes without one.');
				});
				S.textline({ label: 'Code', bind: A.ref($form, 'code') });
			},
			actions: () => S.button({ content: 'Sign in', icon: check, type: 'submit' }),
		});
	}});
}

async function stopDaemon(hid: string): Promise<void> {
	if (!(await S.confirm(`Stop the TPS daemon on ${hostName(hid)}? Its running workspaces are shut down; Connect starts it again.`))) return;
	void cmd('stopDaemon', { hid });
}

async function removeHost(hid: string): Promise<void> {
	const projects = A.peek(() => projectsOn(hid).length);
	const also = projects ? ` Its ${projects} project${projects > 1 ? 's' : ''} leave${projects > 1 ? '' : 's'} the list with it.` : '';
	if (!(await S.confirm(`Remove "${A.peek(() => hostName(hid))}" from the list?${also} Nothing on the host is thrown away: work in hand runs to its end, and its daemon stops once nothing is running and nothing is connected to it. Adding the host again picks it all back up.`))) return;
	void cmd('removeHost', { hid });
}

/**
 * Adding a host: an ssh destination, and then its page, which is where its
 * projects are added. Connecting to it happens there, and so does the login
 * it may want.
 */
export function addHostDialog(): void {
	const $form = A.proxy({ dest: '' });
	void S.dialog({ header: 'Add host', attrs: 'w:32rem', content: close => {
		S.form({
			submit: async () => {
				if (!$form.dest.trim()) return;
				const result = await cmd('addHost', { dest: $form.dest.trim() });
				if (!result) return;
				close();
				void route.go(hostPath(result.hid));
			},
			content: () => {
				A('p rich=', 'A host you reach over SSH: `user@host`, or an alias from `~/.ssh/config` (options like `-p 2222` go in front). TPS installs its daemon there and adopts the projects it already has.');
				S.textline({ label: 'Host', placeholder: 'user@host', required: true, bind: A.ref($form, 'dest') });
			},
			actions: () => S.button({ content: 'Add', icon: plus, type: 'submit' }),
		});
	}});
}
