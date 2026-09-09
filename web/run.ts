import A from 'aberdeen';
import * as S from 'staffa';
import { externalLink } from 'staffa/icons.js';
import { cmd } from './util.ts';

/**
 * Run the project (its Containerfile's CMD) in the task's container: a dialog
 * with the console output and, once something answers on $PORT, the site.
 * Closing it stops the run, unless asked to keep it going.
 */
export function runDialog(pid: string, tid: string, $t: any): void {
	const $ui = A.proxy({ tab: 'console', keep: false });
	if (A.peek(() => $t.run?.status) !== 'running') void cmd('runTask', { pid, tid });
	const url = () => `http://${location.hostname}:${$t.appPort}/`;
	void S.dialog({
		header: () => { A('span text=', 'Run: '); A('code text=', $t.runCmd); },
		attrs: 'w:110rem max-width:96vw',
		onClose: () => { if (!$ui.keep) void cmd('stopRun', { pid, tid }); },
		content: close => {
			S.tabs({
				bind: A.ref($ui, 'tab'),
				contentAttrs: 'h:65dvh display:flex flex-direction:column',
				tabs: [
					{ id: 'console', label: 'Console', content: () => drawConsole($t) },
					{ id: 'site', label: 'Live Site', get disabled() { return !$t.live; }, content: () => {
						A('iframe flex:1 w:100% border:0', 'src=', url());
					}},
				],
			});
			// The site as soon as it answers; the console until then.
			A(() => { if ($t.live) $ui.tab = 'site'; });
			A('div display:flex align-items:center gap:$2 margin-top:$3', () => {
				S.checkbox({ label: 'Keep running when closed', bind: A.ref($ui, 'keep') });
				A('div flex:1');
				A(() => {
					S.iconButton({ icon: externalLink, ariaLabel: 'Open in a new tab', disabled: !$t.live, click: () => window.open(url(), '_blank') });
					if ($t.run?.status === 'running') S.button({ content: 'Stop', attrs: '.danger', click: () => void cmd('stopRun', { pid, tid }) });
					else S.button({ content: 'Run again', click: () => void cmd('runTask', { pid, tid }) });
				});
				S.button({ content: 'Close', attrs: '.neutral', click: close });
			});
		},
	});
}

/** The output tail, following new output unless scrolled up, and a status line. */
function drawConsole($t: any): void {
	const el = A('pre r:0 flex:1 min-height:0 overflow:auto white-space:pre-wrap overflow-wrap:anywhere', () => {
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
		else if (run?.status === 'running') A('text=', $t.live ? 'Running; something answers on $PORT.' : 'Running; nothing answers on $PORT yet.');
		else if (run?.status === 'exited') A('text=', run.code ? `Exited with code ${run.code}.` : 'Finished.');
		else A('text=', 'Starting…');
	});
}
