import A from 'aberdeen';
import * as S from 'staffa';
import { check, download, ellipsisVertical, keyRound, plug, plus, power, refreshCw, serverCrash, serverOff, trash2, triangleAlert } from 'staffa/icons.js';
import { showAsk } from './ask.ts';
import { $state } from './conn.ts';
import { addProjectDialog, projectsOn } from './projects.ts';
import { cmd, destName, drawStrip, ELLIPSIS, hostIcon, hostName } from './util.ts';

/**
 * The hosts: the machines projects run on, this one and the ones reached over
 * SSH. They are listed at the foot of the sidebar, each saying by its colour
 * what it is doing (see hostState); clicking one opens its dialog, which is
 * where everything about a host is — what it is doing, where it is, and what
 * there is to do with it.
 */

/** The hosts in the order they are shown: this machine first, the rest by name. */
export function sortedHosts(): [string, any][] {
	return (Object.entries($state.hosts ?? {}) as [string, any][])
		.sort((a, b) => hostOrder(a[0]).localeCompare(hostOrder(b[0])));
}

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
 * the host's dialog and the strip above a project's board all say this, so
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
 * One host, as a dialog: the plain facts, what it is doing and the way out of
 * that where there is one, the destination it is reached at, and the rest of
 * what there is to do with it in the state it is in.
 */
export function hostDialog(hid: string): void {
	void S.dialog({ header: () => A('text=', hostName(hid)), attrs: 'w:32rem', contentAttrs: 'display:flex flex-direction:column gap:$3', content: close => {
		A(() => {
			const $h = $state.hosts?.[hid];
			// Removed, here or from another dashboard: there is nothing left to show.
			if (!$h) return close();
			drawHostFacts(hid);
			const state = hostState(hid, $h);
			drawStrip(state.color, state.text, () => drawAction(state));
		});
		// Outside that scope: a redraw while typing would take the cursor with it.
		drawHostFoot(hid, close);
	}});
}

/** The way out of what a host is doing, where its state has one. */
export function drawAction(state: HostState): void {
	if (state.action) S.button({ content: state.action.label, attrs: `.small .${state.color}`, click: state.action.run });
}

/** The plain facts about a host: how much of the board is there. */
function drawHostFacts(hid: string): void {
	A(`small ${ELLIPSIS} fg:$s-muted`, () => {
		const projects = projectsOn(hid).length;
		const count = `${projects || 'no'} project${projects === 1 ? '' : 's'}`;
		A('text=', hid === 'local' ? `this machine · ${count}` : count);
	});
}

/**
 * The foot of the dialog: the name the host is shown by, and the ssh
 * destination it is reached at, to point it at another machine, with their own
 * Save — nothing typed there reaches the host until that is pressed, and
 * Cancel drops it. Saving another destination drops the connection and makes
 * it anew, as if the host were added again: the projects found there take the
 * place of the ones it had. This machine has no destination to point
 * elsewhere, so it only takes a name.
 *
 * Beside the buttons, out of the way, the menu of what else there is to do
 * with the host.
 */
function drawHostFoot(hid: string, close: () => void): void {
	const $form = A.proxy(A.peek(() => ({ name: $state.hostNames?.[hid] ?? '', dest: $state.hosts?.[hid]?.dest ?? '' })));
	S.form({
		submit: async () => {
			if (await cmd('setHost', { hid, name: $form.name, dest: $form.dest.trim() })) close();
		},
		content: () => {
			S.textline({ label: 'Name', placeholder: A.peek(() => destName(hid)), bind: A.ref($form, 'name'), help: 'What to call it in the sidebar.' });
			if (hid !== 'local') S.textline({
				label: 'Host', placeholder: 'user@host', required: true, bind: A.ref($form, 'dest'),
				help: 'Whatever you would type after `ssh` (options like `-p 2222` go in front).',
			});
		},
		actionsAttrs: 'justify-content:flex-start',
		actions: () => {
			A(() => drawHostMenu(hid, $state.hosts?.[hid] ?? {}));
			A('div flex:1');
			S.button({ content: 'Cancel', attrs: '.small .neutral', click: close });
			S.button({ content: 'Save', icon: check, attrs: '.small', type: 'submit' });
		},
	});
}

/**
 * What else there is to do with a host: a menu, since none of it is what the
 * dialog is for. The daemon's own — a project to add, a login to make, the
 * daemon to stop — only while it is there to be asked.
 */
function drawHostMenu(hid: string, $h: any): void {
	const items: S.MenuEntry[] = [];
	if ($h.status === 'connected') {
		items.push(
			{ label: 'Add project…', icon: plus, click: () => addProjectDialog(hid) },
			// Also for a login that still works, to replace it with another account.
			{ label: 'Sign in to claude', icon: keyRound, click: () => void signIn(hid) },
			{ label: 'Stop daemon…', icon: power, click: () => void stopDaemon(hid) },
		);
	}
	if (hid !== 'local') {
		if (items.length) items.push({ separator: true });
		items.push({ label: 'Remove host…', icon: trash2, attrs: 'fg:$s-danger', click: () => void removeHost(hid) });
	}
	if (!items.length) return;
	S.menuButton({ button: { icon: ellipsisVertical, ariaLabel: 'What else to do with this host', attrs: '.small .neutral' }, items });
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
 * Adding a host: an ssh destination. Connecting to it happens in its row, and
 * so does the login it may want. Resolves with the new host's id, so the
 * project dialog that sent you here can select it.
 */
export function addHostDialog(): Promise<string | undefined> {
	const $form = A.proxy({ dest: '' });
	let hid: string | undefined;
	return S.dialog({ header: 'Add host', attrs: 'w:32rem', content: close => {
		S.form({
			submit: async () => {
				if (!$form.dest.trim()) return;
				const result = await cmd('addHost', { dest: $form.dest.trim() });
				if (!result) return;
				hid = result.hid;
				close();
			},
			content: () => {
				A('p rich=', 'A host you reach over SSH: `user@host`, or an alias from `~/.ssh/config` (options like `-p 2222` go in front). TPS installs its daemon there and adopts the projects it already has.');
				S.textline({ label: 'Host', placeholder: 'user@host', required: true, bind: A.ref($form, 'dest') });
			},
			actions: () => S.button({ content: 'Add', icon: plus, type: 'submit' }),
		});
	}}).then(() => hid);
}
