import A from 'aberdeen';
import * as S from 'staffa';
import { check, circleX, play, square } from 'staffa/icons.js';
import { ansiToHtml } from './ansi.ts';
import { busyAttrs, cmd, ELLIPSIS, portUrl } from './util.ts';

/**
 * A task's services: the named, long-running commands in its container —
 * the project's dev server (the Containerfile's CMD, as 'app'), a test suite,
 * a review app — declared by Containerfile.dev or started ad hoc, by the
 * agent (tps-service-manager) or from here. The play button opens a menu of
 * them; a service opens a console with its output and the buttons to start,
 * stop and restart it. The ports the container forwards need no menu: they are
 * in the task's header (see drawTaskHeader). A service belongs to the task,
 * not to its container: a replaced container ends what ran, and leaves every
 * service idle with the command it runs, ready to start again.
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

/** Whether the task has services: what the play button opens the menu of. */
export function hasServices($t: any): boolean {
	return !!$t.services?.length;
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
		// Running is the play the row would have started, breathing: the same
		// sign of something going on that a task's icon gives (see busyAttrs).
		case 'running': return () => play({ attrs: `fg:$s-danger ${busyAttrs(play)}` });
		case 'exited': return s.code ? () => circleX({ attrs: 'fg:$s-danger' }) : check;
		case 'stopped': return square;
		default: return play;
	}
}

/**
 * The play button's menu: a row per service, a click opening its console — and
 * starting one that is not started.
 */
export function servicesMenu(anchor: HTMLElement, pid: string, tid: string, $t: any): void {
	// Plain copies: the menu is built once, as it opens, off the state of that moment.
	const services: Service[] = A.peek(() => ($t.services ?? []).map((s: Service) => ({ ...s })));
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
			click: () => serviceDialog(pid, tid, $t, s.name, s.status === 'idle'),
		});
	}
	S.showFloatingMenu({ anchor, items });
}

/**
 * The forwarded ports as "8080 → 56123", the port inside the container and the
 * one it is on here: a link that opens the page in a new tab once something
 * answers HTTP there, and what is in the way of that until then.
 */
export function drawPortLinks($t: any): void {
	A('small display:flex flex-wrap:wrap gap:$2 min-width:0', () => {
		for (const p of ($t.ports ?? []) as Port[]) {
			const label = portLabel(p);
			if (p.live) {
				A('a fg:$s-link text-decoration:none', 'href=', portUrl(p), 'target=_blank', 'text=', label,
					() => S.addTooltip({ tip: 'Open in a new tab' }));
			} else {
				A('span fg:$s-muted text=', label, () => S.addTooltip({ tip: portTip(p) }));
			}
		}
	});
}

const portLabel = (p: Port) => `${p.port} → ${p.host}`;
const portTip = (p: Port) => p.live ? 'Open in a new tab' : p.open ? 'Something listens here, but does not answer HTTP' : 'Nothing listens here yet';

/**
 * One service's console: its output, following new output unless scrolled
 * up, and the buttons for it. Opened with start, it starts the service
 * first, as the menu does for one that is not started. The service outlives
 * the dialog — the menu brings it back with the output intact — unless *Stop*
 * ends it.
 */
export function serviceDialog(pid: string, tid: string, $t: any, name: string, start = false): void {
	const find = (): Service | undefined => ($t.services ?? []).find((s: Service) => s.name === name);
	// Starting is no state of the service: it stays idle until the container has
	// it running, and the console says so for as long as the command is out.
	const $starting = A.proxy({ value: false });
	const run = async () => {
		$starting.value = true;
		await cmd('runService', { pid, tid, name });
		$starting.value = false;
	};
	if (start) void run();
	let close: () => void;
	void S.dialog({
		header: () => { A('span text=', name + ': '); A(() => A('code text=', find()?.cmd ?? '')); },
		// A height of its own, so the console scrolls inside it and the dialog never does.
		attrs: 'w:110rem max-width:96vw h:min(88vh,800px)',
		content: c => { close = c; drawConsole($t, name, find, $starting); },
		footer: () => {
			A('div display:flex flex-wrap:wrap gap:$2 margin-right:auto', () => drawPorts($t));
			A(() => {
				const s = find();
				if (s?.status === 'running') {
					S.button({ content: 'Stop', attrs: '.danger', click: () => void cmd('stopService', { pid, tid, name }) });
					S.button({ content: 'Restart', attrs: '.neutral', click: () => void cmd('restartService', { pid, tid, name }) });
					S.button({ content: 'Background', attrs: '.neutral', tooltip: 'Leave it running; the play button brings this back', click: () => close() });
				} else {
					if (s) S.button({ content: s.status === 'idle' ? 'Start' : 'Run again', click: () => void run() });
					S.button({ content: 'Close', attrs: '.neutral', click: () => close() });
				}
			});
		},
	});
}

function drawConsole($t: any, name: string, find: () => Service | undefined, $starting: { value: boolean }): void {
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
		else if (!s) A('text=', 'Gone: the task has no such service.');
		else if (s.status === 'running') A('text=', 'Running.' + ($t.ports ? '' : ' Ports named by EXPOSE lines in Containerfile.dev are forwarded; this one has none.'));
		else if ($starting.value) A('text=', 'Starting…');
		else A('text=', statusText(s).replace(/^./, c => c.toUpperCase()) + '.');
	});
}

/** One button per forwarded port, live once something answers HTTP there: the dialog covers the header they are in otherwise. */
function drawPorts($t: any): void {
	A(() => {
		for (const p of ($t.ports ?? []) as Port[]) {
			S.button({ content: portLabel(p), attrs: '.neutral', disabled: !p.live, tooltip: portTip(p),
				click: () => window.open(portUrl(p), '_blank') });
		}
	});
}
