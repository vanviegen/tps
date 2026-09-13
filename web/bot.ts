import A from 'aberdeen';
import type { IconOptions } from 'staffa/icons.js';
import { animationCss, markup, STROKE } from './robot.ts';

/**
 * TPS's robot as an icon, drawn like the ones from staffa (the same options,
 * the same `<svg>`) so it stands in for lucide's bot wherever that was. The
 * artwork is robot.ts's; this is the one place it meets the DOM.
 *
 * With `.tps-busy` among its attrs it comes alive: it types away at its
 * keyboard, then looks up and thinks, and types again — the sign that the
 * agent has the task, in place of the breathing the other icons do.
 */
export function bot(opts: IconOptions = {}): void {
	const size = opts.size ?? 24;
	const el = A('svg.s-icon.tps-bot aria-hidden=true viewBox="0 0 24 24" fill=none',
		'width=', size, 'height=', size,
		'stroke=', opts.color ?? 'currentColor', 'color=', opts.color ?? 'currentColor',
		'stroke-width=', opts.strokeWidth ?? STROKE, 'stroke-linecap=', opts.cap ?? 'round', 'stroke-linejoin=', opts.join ?? 'round',
		opts.attrs) as SVGSVGElement;
	// innerHTML on the <svg> itself puts the children in the SVG namespace.
	el.innerHTML = INNER;
}

const INNER = markup();

A.insertGlobalCss(animationCss());
