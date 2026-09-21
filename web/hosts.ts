import A from 'aberdeen';
import * as S from 'staffa';
import { check, download, keyRound, plug, plus, power, refreshCw, serverCrash, serverOff, trash2, triangleAlert } from 'staffa/icons.js';
import { askLabel, askSummary, hostAsk, showAsk } from './ask.ts';
import { $state } from './conn.ts';
import { addProjectDialog, projectsOn } from './projects.ts';
import { cmd, drawStrip, ELLIPSIS, hostName } from './util.ts';

/**
 * The hosts: the machines projects run on, this one and the ones reached over
 * SSH. They are listed at the foot of the sidebar, each saying by its colour
 * what it is doing (see hostIssue); clicking one opens its dialog, which is
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

/**
 * What a host wants, for its row in the sidebar: a login to give, a
 * connection that failed, a daemon to update. A host that works has nothing
 * to say and gets nothing here.
 */
export function hostIssue(hid: string, $h: any): { color: string; text: string; icon: typeof serverCrash } | undefined {
	const ask = hostAsk(hid);
	if (ask) return { color: 'warning', text: `${askLabel(ask[1])}: ${askSummary(ask[1])}`, icon: keyRound };
	const status = $h?.status ?? 'connecting';
	if (status !== 'connected') {
		const quiet = status === 'connecting' || status === 'updating' || status === 'stopped';
		return { color: quiet ? 'neutral' : 'danger', text: `${status}${$h?.error ? ' · ' + $h.error : ''}`, icon: status === 'stopped' ? serverOff : serverCrash };
	}
	// A login to make, or one being made here (see ui/login.go).
	if ($h.signin || $h.login) return { color: 'warning', text: $h.signin || $h.login, icon: keyRound };
	if ($h.warning) return { color: 'warning', text: $h.warning, icon: triangleAlert };
	if ($h.restarting) return { color: 'neutral', text: 'the daemon restarts into this build once nothing is running', icon: refreshCw };
	if ($h.updatable) return { color: 'neutral', text: 'the daemon runs another build of TPS', icon: download };
	return undefined;
}

/**
 * One host, as a dialog: the plain facts, whatever it is doing or waiting
 * for, the destination it is reached at, and everything there is to do with
 * it — a project to add, a login to make, the daemon to update or stop, the
 * host to take off the list.
 */
export function hostDialog(hid: string): void {
	void S.dialog({ header: () => A('text=', hostName(hid)), attrs: 'w:32rem', contentAttrs: 'display:flex flex-direction:column gap:$3', content: close => {
		A(() => {
			const $h = $state.hosts?.[hid];
			// Removed, here or from another dashboard: there is nothing left to show.
			if (!$h) return close();
			drawHostFacts(hid);
			drawHostState(hid, $h);
		});
		// Outside that scope: a redraw while typing would take the cursor with it.
		if (hid !== 'local') drawDestField(hid);
		A(() => drawHostActions(hid, $state.hosts?.[hid] ?? {}));
	}});
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
 * What a host's state amounts to, as a colour role: connected, quiet and with
 * nothing to answer is `success`.
 */
export function hostColor(hid: string, $h: any): string {
	const status = $h?.status ?? 'connecting';
	if (hostAsk(hid)) return 'warning'; // it is waiting for you, not for the network
	return status === 'connected' ? (($h.signin || $h.login || $h.warning || $h.updatable) ? 'warning' : 'success')
		: status === 'connecting' || status === 'updating' ? 'warning'
			: status === 'stopped' ? 'muted' : 'danger';
}

/** What the host is doing, or waiting for. */
function drawHostState(hid: string, $h: any): void {
	// A question (an ssh password, an unknown host key) waits here instead of
	// opening by itself; the strip is the way in to it.
	const ask = hostAsk(hid);
	if (ask) {
		const [id, $a] = ask;
		// The whole strip is the button: what it says is the question itself.
		drawStrip('warning', askSummary($a), () => {
			keyRound({ size: '1em' });
			A('span font-weight:600 text=', askLabel($a));
		}, () => showAsk(id));
		return;
	}
	// Anything else it has to say is what its row in the sidebar says; what to
	// do about it is in the buttons below, rather than said twice.
	const issue = hostIssue(hid, $h);
	if (issue) drawStrip(issue.color, issue.text);
}

/**
 * The ssh destination the host is reached at, to point it at another machine.
 * Saving drops the connection and makes it anew, as if the host were added
 * again: the projects found there take the place of the ones it had.
 */
function drawDestField(hid: string): void {
	const $form = A.proxy({ dest: A.peek(() => $state.hosts?.[hid]?.dest ?? '') });
	S.form({
		submit: () => void cmd('setHost', { hid, dest: $form.dest.trim() }),
		content: () => S.textline({
			label: 'Host', placeholder: 'user@host', required: true, bind: A.ref($form, 'dest'),
			help: 'Whatever you would type after `ssh` (options like `-p 2222` go in front).',
		}),
		actions: () => S.button({ content: 'Save', icon: check, attrs: '.small', type: 'submit' }),
	});
}

/** Everything there is to do with a host, as buttons. */
function drawHostActions(hid: string, $h: any): void {
	A('div display:flex flex-wrap:wrap align-items:center gap:$2', () => {
		S.button({ content: 'Add project', icon: plus, attrs: '.small', click: () => addProjectDialog(hid) });
		if ($h.status !== 'connected') S.button({ content: 'Connect', icon: plug, attrs: '.small .neutral', click: () => void cmd('connectHost', { hid }) });
		if ($h.updatable) S.button({ content: 'Update daemon', icon: download, attrs: '.small .neutral', click: () => void cmd('updateDaemon', { hid }) });
		// Also for a login that stopped working, so it is offered whatever the host says.
		S.button({ content: 'Sign in to claude', icon: keyRound, attrs: '.small .neutral', click: () => void cmd('login', { hid }) });
		S.button({ content: 'Stop daemon', icon: power, attrs: '.small .neutral', click: async () => {
			if (await S.confirm(`Stop the TPS daemon on ${hostName(hid)}? Its running workspaces are shut down; Connect starts it again.`)) void cmd('stopDaemon', { hid });
		} });
		if (hid !== 'local') {
			A('div flex:1');
			S.button({ content: 'Remove host', icon: trash2, attrs: '.small .danger .outlined', click: () => removeHost(hid) });
		}
	});
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

