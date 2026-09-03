import { FitAddon } from '@xterm/addon-fit';
import { Terminal } from '@xterm/xterm';
import '@xterm/xterm/css/xterm.css';
import A from 'aberdeen';
import { onTerm } from './conn.ts';

/**
 * An xterm.js view of a task's agent/build output stream.
 *
 * The terminal is opened in an absolutely positioned inner element, so its
 * size is dictated purely by layout: xterm resizing its own content can never
 * change the observed size, which would loop the ResizeObserver.
 */
export function drawTerminal(pid: string, tid: string): void {
	let inner!: HTMLElement;
	A('div.tps-term', () => {
		inner = A('div.tps-term-inner') as HTMLElement;
	});
	const term = new Terminal({
		disableStdin: true,
		fontSize: 13,
		scrollback: 5000,
		theme: { background: '#16161e', foreground: '#c8c8d0' },
	});
	const fit = new FitAddon();
	term.loadAddon(fit);
	term.open(inner);
	let lastW = 0, lastH = 0, raf = 0;
	const refit = () => {
		cancelAnimationFrame(raf);
		raf = requestAnimationFrame(() => {
			const w = inner.clientWidth, h = inner.clientHeight;
			if (!w || !h || (w === lastW && h === lastH)) return;
			lastW = w;
			lastH = h;
			try { fit.fit(); } catch {}
		});
	};
	const resizer = new ResizeObserver(refit);
	resizer.observe(inner);
	refit();
	onTerm(pid, tid, (data, reset) => {
		if (reset) term.reset();
		term.write(data);
	});
	A.clean(() => {
		cancelAnimationFrame(raf);
		resizer.disconnect();
		term.dispose();
	});
}
