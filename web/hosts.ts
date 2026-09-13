import A from 'aberdeen';
import * as S from 'staffa';
import { download, ellipsisVertical, keyRound, plug, plus, power, refreshCw, serverCrash, serverOff, trash2, triangleAlert } from 'staffa/icons.js';
import { askLabel, askSummary, hostAsk, showAsk } from './ask.ts';
import { $state } from './conn.ts';
import { addProjectDialog, projectsOn } from './projects.ts';
import { cmd, drawStrip, ELLIPSIS, hostIcon, hostName } from './util.ts';

/**
 * The hosts: the machines projects run on, this one and the ones reached over
 * SSH. They live behind the sidebar's *Manage hosts* button — one box each,
 * outlined in what it is doing, holding whatever it wants from you — and the
 * ones that want something also show in the sidebar itself (see hostIssue).
 */

/** The hosts in the order they are shown: this machine first, the rest by name. */
export function sortedHosts(): [string, any][] {
	return (Object.entries($state.hosts ?? {}) as [string, any][])
		.sort((a, b) => hostOrder(a[0]).localeCompare(hostOrder(b[0])));
}

function hostOrder(hid: string): string {
	return hid === 'local' ? '' : '1' + hostName(hid).toLowerCase();
}

/** The hosts, as a dialog: a box per host, and the way to add one. */
export function manageHostsDialog(): void {
	void S.dialog({ header: 'Hosts', attrs: 'w:36rem', contentAttrs: 'display:flex flex-direction:column gap:$3', content: () => drawHostList() });
}

/** A box per host, and the way to add one. */
export function drawHostList(): void {
	A('p.s-help m:0 rich=', 'The machines projects run on. *Add host* takes whatever you would type after `ssh`; TPS installs its daemon there and adopts the projects it already has.');
	A('div display:flex flex-direction:column gap:$2', () => {
		A.onEach($state.hosts, ($h: any, hid: string) => drawHostBox(hid, $h), (_$h: any, hid: string) => hostOrder(hid));
	});
	A('div display:flex justify-content:flex-end', () => {
		S.button({ content: 'Add host', icon: plus, attrs: '.small', click: () => void addHostDialog() });
	});
}

/**
 * What a host wants, for the sidebar: a login to give, a connection that
 * failed, a daemon to update. A host that works has nothing to say and gets
 * nothing here. The click leads to the thing itself: the question, or the
 * host's box.
 */
export function hostIssue(hid: string, $h: any): { color: string; text: string; icon: typeof serverCrash; click: () => void } | undefined {
	const ask = hostAsk(hid);
	if (ask) return { color: 'warning', text: `${askLabel(ask[1])}: ${askSummary(ask[1])}`, icon: keyRound, click: () => showAsk(ask[0]) };
	const status = $h?.status ?? 'connecting';
	const open = () => manageHostsDialog();
	if (status !== 'connected') {
		const quiet = status === 'connecting' || status === 'updating' || status === 'stopped';
		return { color: quiet ? 'neutral' : 'danger', text: `${status}${$h?.error ? ' · ' + $h.error : ''}`, icon: status === 'stopped' ? serverOff : serverCrash, click: open };
	}
	// A login to make, or one being made here (see ui/login.go): the click starts it, or starts it over.
	if ($h.signin || $h.login) return { color: 'warning', text: $h.signin || $h.login, icon: keyRound, click: () => void cmd('login', { hid }) };
	if ($h.warning) return { color: 'warning', text: $h.warning, icon: triangleAlert, click: open };
	if ($h.restarting) return { color: 'neutral', text: 'the daemon restarts into this build once nothing is running', icon: refreshCw, click: open };
	if ($h.updatable) return { color: 'neutral', text: 'the daemon runs another build of TPS', icon: download, click: () => void cmd('updateDaemon', { hid }) };
	return undefined;
}

/**
 * One host: what it is, what it is doing, and what it wants from you. The
 * border says the last of those at a glance, so a column of quiet hosts stays
 * quiet and the one in trouble stands out.
 */
function drawHostBox(hid: string, $h: any): void {
	A('div min-width:0', () => {
		S.addContextMenu({ get items(): S.MenuEntry[] { return hostItems(hid, $h); } });
		// The border is the status, so the box is redrawn when that changes;
		// nothing inside it holds anything worth keeping across that.
		A(() => S.box({
			attrs: `mt:0 min-width:0 border: 1px solid $s-${hostColor(hid, $h)};`,
			headerAttrs: 'display:flex align-items:center gap:$2 min-width:0',
			contentAttrs: 'display:flex flex-direction:column gap:$2 p:$2',
			header: () => {
				A('span display:flex flex-shrink:0', () => hostIcon(hid)({ size: '1.1em' }));
				A(`span flex:1 ${ELLIPSIS} text=`, hostName(hid));
				S.iconButton({
					icon: ellipsisVertical, ariaLabel: 'Host menu', attrs: '.small',
					click: e => void S.showFloatingMenu({ items: hostItems(hid, $h), anchor: e.currentTarget as HTMLElement }),
				});
			},
			content: () => {
				A(() => drawHostFacts(hid, $h));
				A(() => drawHostState(hid, $h));
			},
		}));
	});
}

/** The plain facts about a host: where it is, and how much of the board is there. */
function drawHostFacts(hid: string, $h: any): void {
	A(`small ${ELLIPSIS} fg:$s-muted`, () => {
		const projects = projectsOn(hid).length;
		const where = hid === 'local' ? 'this machine' : $h.dest || hostName(hid);
		A('text=', `${where} · ${projects || 'no'} project${projects === 1 ? '' : 's'}`);
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

/** Anything about the host that asks for a look, or a click. */
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
	if ($h.status !== 'connected') {
		const waiting = $h.status === 'connecting' || $h.status === 'updating';
		drawStrip(waiting || $h.status === 'stopped' ? 'neutral' : 'danger',
			`${$h.status}${$h.error ? ' · ' + $h.error : ''}`,
			waiting ? undefined : () => S.button({ content: 'Connect', attrs: '.small', click: () => void cmd('connectHost', { hid }) }));
	} else if ($h.signin || $h.login) {
		drawStrip('warning', $h.signin || $h.login,
			() => S.button({ content: 'Sign in', attrs: '.small', click: () => void cmd('login', { hid }) }));
	} else if ($h.warning) {
		drawStrip('warning', $h.warning);
	} else if ($h.restarting) {
		drawStrip('neutral', 'The daemon restarts into this build as soon as nothing is running.');
	} else if ($h.updatable) {
		drawStrip('neutral', 'The daemon runs another build of TPS.',
			() => S.button({ content: 'Update', attrs: '.small', click: () => void cmd('updateDaemon', { hid }) }));
	}
}

/** What the host's menu offers, from its header button and its context menu. */
function hostItems(hid: string, $h: any): S.MenuEntry[] {
	const items: S.MenuEntry[] = [{ label: 'Add project…', icon: plus, click: () => addProjectDialog(hid) }, { separator: true }];
	if ($h.status !== 'connected') items.push({ label: 'Connect', icon: plug, click: () => void cmd('connectHost', { hid }) });
	if ($h.updatable) items.push({ label: 'Update daemon', icon: download, click: () => void cmd('updateDaemon', { hid }) });
	items.push({ label: 'Sign in to claude…', icon: keyRound, click: () => void cmd('login', { hid }) }); // also for a login that stopped working
	items.push({ label: 'Stop daemon', icon: power, click: async () => {
		if (await S.confirm(`Stop the TPS daemon on ${hostName(hid)}? Its running workspaces are shut down; Connect starts it again.`)) void cmd('stopDaemon', { hid });
	}});
	if (hid !== 'local') {
		items.push({ separator: true }, { label: 'Remove host', icon: trash2, click: () => void removeHost(hid) });
	}
	return items;
}

async function removeHost(hid: string): Promise<void> {
	const projects = A.peek(() => projectsOn(hid).length);
	const also = projects ? ` Its ${projects} project${projects > 1 ? 's' : ''} leave${projects > 1 ? '' : 's'} the list with it.` : '';
	if (!(await S.confirm(`Remove "${A.peek(() => hostName(hid))}" from the list?${also} Nothing changes on the host itself: its daemon and tasks keep running, and come back when you add it again.`))) return;
	void cmd('removeHost', { hid });
}

/**
 * Adding a host: an ssh destination. Connecting to it happens in its box, and
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

