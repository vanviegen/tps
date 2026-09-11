import A from 'aberdeen';
import * as S from 'staffa';
import { cmd, portUrl } from './util.ts';

/**
 * Run the project (its Containerfile's CMD) in the task's container: a dialog
 * with the console output, and a button per port the image exposes, live once
 * HTTP answers there. The run outlives the dialog — the play button reopens it
 * with the output intact — unless *Stop* ends it on the way out.
 */
export function runDialog(pid: string, tid: string, $t: any): void {
	if (A.peek(() => $t.run?.status) !== 'running') void cmd('runTask', { pid, tid });
	let close: () => void;
	void S.dialog({
		header: () => { A('span text=', 'Run: '); A('code text=', $t.runCmd); },
		// A height of its own, so the console scrolls inside it and the dialog never does.
		attrs: 'w:110rem max-width:96vw h:min(88vh,800px)',
		content: c => { close = c; drawConsole($t); },
		footer: () => {
			A('div display:flex flex-wrap:wrap gap:$2 margin-right:auto', () => drawPorts($t));
			A(() => {
				if ($t.run?.status === 'running') {
					S.button({ content: 'Stop', attrs: '.danger', click: () => { void cmd('stopRun', { pid, tid }); close(); } });
					S.button({ content: 'Background', attrs: '.neutral', tooltip: 'Leave it running; the play button brings this back', click: () => close() });
				} else {
					S.button({ content: 'Run again', click: () => void cmd('runTask', { pid, tid }) });
					S.button({ content: 'Close', attrs: '.neutral', click: () => close() });
				}
			});
		},
	});
}

/** The output tail, following new output unless scrolled up, and a status line. */
function drawConsole($t: any): void {
	const el = A('pre r:0 flex:1 min-height:0 m:0 overflow:auto white-space:pre-wrap overflow-wrap:anywhere', () => {
		A('text=', $t.runLog || '');
	}) as HTMLElement;
	let stick = true;
	el.addEventListener('scroll', () => {
		stick = el.scrollHeight - el.scrollTop - el.clientHeight < 60;
	});
	A(() => {
		void $t.runLog;
		if (stick) requestAnimationFrame(() => { el.scrollTop = el.scrollHeight; });
	});
	A('div fg:$s-muted font-size:0.9em', () => {
		const run = $t.run;
		if ($t.status !== 'up') A('text=', `${$t.statusDetail || $t.status}…`);
		else if (run?.status === 'running') A('text=', $t.ports ? 'Running.' : 'Running. Ports named by EXPOSE lines in Containerfile.dev are forwarded; this one has none.');
		else if (run?.status === 'exited') A('text=', run.code ? `Exited with code ${run.code}.` : 'Finished.');
		else A('text=', 'Starting…');
	});
}

/**
 * One button per forwarded port, "8080 → 56123" being the port inside the
 * container and the one it is on here; it opens the page in a new tab, and
 * is disabled until something answers HTTP there.
 */
function drawPorts($t: any): void {
	A(() => {
		for (const p of $t.ports ?? []) {
			S.button({ content: `${p.port} → ${p.host}`, attrs: '.neutral', disabled: !p.live,
				tooltip: p.live ? 'Open in a new tab' : 'Nothing answers HTTP here yet',
				click: () => window.open(portUrl(p), '_blank') });
		}
	});
}
