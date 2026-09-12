import A from 'aberdeen';
import * as S from 'staffa';
import { check, circleX, globe, loaderCircle, play, square } from 'staffa/icons.js';
import { ansiToHtml } from './ansi.ts';
import { cmd, ELLIPSIS, portUrl } from './util.ts';

/**
 * A task's services: the named, long-running commands in its container —
 * the project's dev server (the Containerfile's CMD, as 'app'), a test suite,
 * a review app — declared by Containerfile.dev or started ad hoc, by the
 * agent (tps-service-manager) or from here. The play button opens a menu of
 * them, with the forwarded ports below; a service opens a console with its
 * output and the buttons to start, stop and restart it.
 */
export interface Service {
	name: string;
	cmd: string;
	declared?: boolean;
	status: 'idle' | 'running' | 'exited' | 'stopped';
	code?: number;
	started?: number;
	ended?: number;
}

interface Port { port: number; host: number; live?: boolean; open?: boolean; }

/** Whether any service of the task runs: the play button is red then. */
export function anyRunning($t: any): boolean {
	return ($t.services ?? []).some((s: Service) => s.status === 'running');
}

/** Whether the task has anything for the menu: a service, or a forwarded port. */
export function hasServices($t: any): boolean {
	return !!($t.services?.length || $t.ports?.length);
}

function statusText(s: Service): string {
	switch (s.status) {
		case 'running': return 'running' + (s.started ? ` since ${clock(s.started)}` : '');
		case 'exited': return (s.code ? `exited with code ${s.code}` : 'finished') + (s.ended ? ` at ${clock(s.ended)}` : '');
		case 'stopped': return 'stopped' + (s.ended ? ` at ${clock(s.ended)}` : '');
		default: return 'not started';
	}
}

function clock(ms: number): string {
	return new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function statusIcon(s: Service): S.MenuItem['icon'] {
	switch (s.status) {
		case 'running': return () => loaderCircle({ attrs: 'fg:$s-danger' });
		case 'exited': return s.code ? () => circleX({ attrs: 'fg:$s-danger' }) : check;
		case 'stopped': return square;
		default: return play;
	}
}

/**
 * The play button's menu: a row per service (a click opens its console, and
 * starts one that never ran), then a row per forwarded port — "8080 → 56123",
 * the port inside the container and the one it is on here — that opens the
 * page in a new tab once something answers HTTP there.
 */
export function servicesMenu(anchor: HTMLElement, pid: string, tid: string, $t: any): void {
	// Plain copies: the menu is built once, as it opens, off the state of that moment.
	const services: Service[] = A.peek(() => ($t.services ?? []).map((s: Service) => ({ ...s })));
	const ports: Port[] = A.peek(() => ($t.ports ?? []).map((p: Port) => ({ ...p })));
	const items: S.MenuEntry[] = [];
	for (const s of services) {
		items.push({
			icon: statusIcon(s),
			label: () => {
				A('div display:flex flex-direction:column min-width:0', () => {
					A('div', () => { A('b text=', s.name); A('span fg:$s-muted text=', ' · ' + statusText(s)); });
					A('code font-size:0.85em fg:$s-muted max-width:32rem', ELLIPSIS, 'text=', s.cmd);
				});
			},
			click: () => {
				if (s.status === 'idle') void cmd('runService', { pid, tid, name: s.name });
				serviceDialog(pid, tid, $t, s.name);
			},
		});
	}
	if (!services.length) {
		items.push(() => A('div p:$2 fg:$s-muted font-size:0.9em max-width:24rem text=',
			'No services. A CMD line in Containerfile.dev is the service "app"; a LABEL tps.service.<name>="command" line declares another.'));
	}
	items.push({ separator: true });
	for (const p of ports) {
		const label = `${p.port} → ${p.host}`;
		if (p.live) {
			items.push({ icon: globe, label, href: portUrl(p), target: '_blank', tooltip: 'Open in a new tab' });
		} else {
			items.push({ icon: globe, label: () => { A('span text=', label); A('span fg:$s-muted text=', p.open ? ' · open, not HTTP' : ' · nothing listening'); }, disabled: true });
		}
	}
	if (!ports.length) {
		items.push(() => A('div p:$2 fg:$s-muted font-size:0.9em max-width:24rem text=',
			'No forwarded ports: the EXPOSE lines of Containerfile.dev name them (a change takes a rebuild).'));
	}
	S.showFloatingMenu({ anchor, items });
}

/**
 * One service's console: its output, following new output unless scrolled
 * up, and the buttons for it. The service outlives the dialog — the menu
 * brings it back with the output intact — unless *Stop* ends it.
 */
export function serviceDialog(pid: string, tid: string, $t: any, name: string): void {
	const find = (): Service | undefined => ($t.services ?? []).find((s: Service) => s.name === name);
	let close: () => void;
	void S.dialog({
		header: () => { A('span text=', name + ': '); A(() => A('code text=', find()?.cmd ?? '')); },
		// A height of its own, so the console scrolls inside it and the dialog never does.
		attrs: 'w:110rem max-width:96vw h:min(88vh,800px)',
		content: c => { close = c; drawConsole($t, name, find); },
		footer: () => {
			A('div display:flex flex-wrap:wrap gap:$2 margin-right:auto', () => drawPorts($t));
			A(() => {
				const s = find();
				if (s?.status === 'running') {
					S.button({ content: 'Stop', attrs: '.danger', click: () => void cmd('stopService', { pid, tid, name }) });
					S.button({ content: 'Restart', attrs: '.neutral', click: () => void cmd('restartService', { pid, tid, name }) });
					S.button({ content: 'Background', attrs: '.neutral', tooltip: 'Leave it running; the play button brings this back', click: () => close() });
				} else {
					if (s) S.button({ content: s.status === 'idle' ? 'Start' : 'Run again', click: () => void cmd('runService', { pid, tid, name }) });
					S.button({ content: 'Close', attrs: '.neutral', click: () => close() });
				}
			});
		},
	});
}

function drawConsole($t: any, name: string, find: () => Service | undefined): void {
	// The log is a terminal's output: colours and progress lines are played out
	// (see ansi.ts) into HTML built here, never anything the log itself wrote.
	const el = A('pre r:0 flex:1 min-height:0 m:0 overflow:auto white-space:pre-wrap overflow-wrap:anywhere', () => {
		A('html=', ansiToHtml($t.serviceLogs?.[name] || ''));
	}) as HTMLElement;
	let stick = true;
	el.addEventListener('scroll', () => {
		stick = el.scrollHeight - el.scrollTop - el.clientHeight < 60;
	});
	A(() => {
		void $t.serviceLogs?.[name];
		if (stick) requestAnimationFrame(() => { el.scrollTop = el.scrollHeight; });
	});
	A('div fg:$s-muted font-size:0.9em', () => {
		const s = find();
		if ($t.status !== 'up') A('text=', `${$t.statusDetail || $t.status}…`);
		else if (!s) A('text=', 'Gone: the container was recreated, and what ran in it went with it.');
		else if (s.status === 'running') A('text=', 'Running.' + ($t.ports ? '' : ' Ports named by EXPOSE lines in Containerfile.dev are forwarded; this one has none.'));
		else if (s.status === 'idle') A('text=', 'Starting…');
		else A('text=', statusText(s).replace(/^./, c => c.toUpperCase()) + '.');
	});
}

/** One button per forwarded port, live once something answers HTTP there. */
function drawPorts($t: any): void {
	A(() => {
		for (const p of ($t.ports ?? []) as Port[]) {
			S.button({ content: `${p.port} → ${p.host}`, attrs: '.neutral', disabled: !p.live,
				tooltip: p.live ? 'Open in a new tab' : p.open ? 'Something listens here, but does not answer HTTP' : 'Nothing listens here yet',
				click: () => window.open(portUrl(p), '_blank') });
		}
	});
}
