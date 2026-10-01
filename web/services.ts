import A from 'aberdeen';
import * as S from 'staffa';
import { check, circleX, ethernetPort, globe, play, square } from 'staffa/icons.js';
import { ansiToHtml } from './ansi.ts';
import { busyAttrs, cmd, ELLIPSIS, portUrl } from './util.ts';

/**
 * A task's services: the named, long-running commands in its container —
 * the project's dev server (the Containerfile's CMD, as 'app'), a test suite,
 * a review app — declared by Containerfile.dev or started ad hoc, by the
 * agent (tps-guest-tool) or from here. The task's menu lists them, and the
 * header has a play button for each one running or recently ended (see
 * drawServiceButtons); a service opens a console with its output and the
 * buttons to start, stop and restart it. The ports the container forwards
 * are in the task's menu too, and in the header while something listens on
 * one (see drawPortButtons). A service belongs to the task,
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

/** Whether the task has services: the task menu lists them then. */
export function hasServices($t: any): boolean {
	return !!$t.services?.length;
}

function statusText(s: Service): string {
	switch (s.status) {
		case 'running': return 'Running' + (s.started ? ` since ${clock(s.started)}` : '');
		case 'exited': return (s.code ? `Exited with code ${s.code}` : 'Finished') + (s.ended ? ` at ${clock(s.ended)}` : '');
		case 'stopped': return 'Stopped' + (s.ended ? ` at ${clock(s.ended)}` : '');
		default: return 'Not started';
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
 * The services as menu rows, at the bottom of the task menu: a line per
 * service, its icon telling its status, a click opening its console — and
 * starting one that is not started.
 */
export function serviceItems(pid: string, tid: string, $t: any): S.MenuEntry[] {
	// Plain copies: the menu is built once, as it opens, off the state of that moment.
	const services: Service[] = A.peek(() => ($t.services ?? []).map((s: Service) => ({ ...s })));
	const items: S.MenuEntry[] = [];
	for (const s of services) {
		items.push({
			icon: statusIcon(s),
			label: () => {
				A('div display:flex align-items:baseline gap:$2 min-width:0', () => {
					A('b text=', s.name);
					A('code font-size:0.85em fg:$s-muted max-width:32rem', ELLIPSIS, 'text=', s.cmd);
				});
			},
			tooltip: statusText(s),
			click: () => serviceDialog(pid, tid, $t, s.name, s.status === 'idle'),
		});
	}
	return items;
}

/** How long an ended service keeps its button in the header. */
const RECENT = 15 * 60_000;

// The time, a minute at a time: what ended recently is read against it.
const $clock = A.proxy({ now: Date.now() });
setInterval(() => { $clock.now = Date.now(); }, 60_000);

/**
 * A button per service that runs or ended within the last quarter hour: a
 * play with the service's name set small and low beside it, orange while
 * running, then green, or red when it exited with an error. A click opens its
 * console.
 */
export function drawServiceButtons(pid: string, tid: string, $t: any): void {
	A(() => {
		for (const s of ($t.services ?? []) as Service[]) {
			if (s.status !== 'running' && !(s.ended && $clock.now - s.ended < RECENT)) continue;
			const color = s.status === 'running' ? '$s-warning' : s.code ? '$s-danger' : '$s-success';
			S.iconButton({
				icon: () => {
					play({ attrs: `fg:${color} ${s.status === 'running' ? busyAttrs(play) : ''}` });
					A('span font-size:0.6em align-self:flex-end margin-left:-0.15em text=', s.name);
				},
				attrs: 'width:auto px:$1', ariaLabel: `Service ${s.name}`, tooltip: `${s.name}: ${statusText(s)}`,
				click: () => serviceDialog(pid, tid, $t, s.name),
			});
		}
	});
}

/**
 * A button per forwarded port something listens on, reading `:8080`: green
 * where it answers HTTP, opening it in a new tab; otherwise grey, saying where
 * on this machine the port is reached.
 */
export function drawPortButtons($t: any): void {
	A(() => {
		for (const p of ($t.ports ?? []) as Port[]) {
			if (!p.open) continue;
			S.iconButton({
				icon: () => A('span font-size:0.85em font-family:monospace text=', `:${p.port}`),
				attrs: `width:auto px:$1 ${p.live ? 'fg:$s-success' : ''}`, ariaLabel: `Port ${p.port}`, tooltip: portTip(p),
				click: () => p.live ? window.open(portUrl(p), '_blank') : S.alert(portWhere(p)),
			});
		}
	});
}

/**
 * The forwarded ports as menu rows, each with its status: the ones answering
 * HTTP open in a new tab, the rest are there to be read.
 */
export function portItems($t: any): S.MenuEntry[] {
	return A.peek(() => (($t.ports ?? []) as Port[]).map(p => ({ ...p }))).map(p => ({
		icon: p.live ? globe : ethernetPort,
		label: () => {
			A('span text=', portLabel(p));
			A('span fg:$s-muted font-size:0.85em margin-left:$2 text=', p.live ? 'HTTP' : p.open ? 'listening' : 'closed');
		},
		tooltip: portTip(p),
		disabled: !p.live,
		click: () => window.open(portUrl(p), '_blank'),
	}));
}

const portWhere = (p: Port) => `Port ${p.port} of the container is forwarded to ${location.hostname}:${p.host}.`;
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
					S.button({ content: 'Background', attrs: '.neutral', tooltip: 'Leave it running; its play button above the chat brings this back', click: () => close() });
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
		else A('text=', statusText(s) + '.');
	});
}

/** One button per forwarded port, live once something answers HTTP there. */
function drawPorts($t: any): void {
	A(() => {
		for (const p of ($t.ports ?? []) as Port[]) {
			S.button({ content: portLabel(p), attrs: '.neutral', disabled: !p.live, tooltip: portTip(p),
				click: () => window.open(portUrl(p), '_blank') });
		}
	});
}
