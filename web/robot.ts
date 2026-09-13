/**
 * TPS's robot: one description of the artwork, for everything that wears it.
 * The app icon rasterizes these shapes (icons.mjs), the UI draws them as an
 * icon in the style of the others (bot.ts), and while the agent
 * works the icon types and thinks along the keyframes below.
 *
 * Drawn on the 24x24 canvas of the lucide icons beside it, in strokes of 2
 * with round caps; a dot is a filled circle. Each shape belongs to a part,
 * which is what the animation moves. No mouth: at the 17px the icon mostly
 * has, a face of two dots stays a face and one of three turns to mush.
 */

export type Part = 'lamp' | 'antenna' | 'head' | 'eyes' | 'hand-l' | 'hand-r' | 'keyboard';

export type Shape =
	| { part: Part; line: [number, number, number, number] }
	| { part: Part; rrect: [number, number, number, number, number] }
	| { part: Part; dot: [number, number, number] };

export const STROKE = 2;

/** The app icon's colour: staffa's primary, which is the UI's, so the icon matches it. */
export const COLOR = '#00a884';

/** What moves with what: the face and the antenna go where the head goes, and the lamp sits on the antenna. */
export const PARENT: Partial<Record<Part, Part>> = { eyes: 'head', antenna: 'head', lamp: 'antenna' };

export const SHAPES: Shape[] = [
	{ part: 'lamp', dot: [12, 2.4, 1.4] },
	{ part: 'antenna', line: [12, 4, 12, 5] },
	{ part: 'head', rrect: [6, 5, 12, 9, 2.5] },
	{ part: 'head', line: [3.5, 9, 3.5, 11] },     // ears
	{ part: 'head', line: [20.5, 9, 20.5, 11] },
	{ part: 'eyes', dot: [9.5, 9.5, 1.3] },
	{ part: 'eyes', dot: [14.5, 9.5, 1.3] },
	{ part: 'hand-l', dot: [9, 18.3, 1.3] },
	{ part: 'hand-r', dot: [15, 18.3, 1.3] },
	{ part: 'keyboard', line: [4, 21.5, 20, 21.5] },
];

/** The shapes as SVG markup for a 24x24 viewBox: a group per part, nested as PARENT says. */
export function markup(): string {
	const el = (s: Shape) => 'line' in s ? `<line x1="${s.line[0]}" y1="${s.line[1]}" x2="${s.line[2]}" y2="${s.line[3]}"/>`
		: 'rrect' in s ? `<rect x="${s.rrect[0]}" y="${s.rrect[1]}" width="${s.rrect[2]}" height="${s.rrect[3]}" rx="${s.rrect[4]}"/>`
		: `<circle cx="${s.dot[0]}" cy="${s.dot[1]}" r="${s.dot[2]}" fill="currentColor" stroke="none"/>`;
	const parts = [...new Set(SHAPES.map(s => s.part))];
	const group = (part: Part): string => `<g class="tps-bot-${part}">${SHAPES.filter(s => s.part === part).map(el).join('')}${parts.filter(p => PARENT[p] === part).map(group).join('')}</g>`;
	return parts.filter(p => !PARENT[p]).map(group).join('');
}

/**
 * Where a part is, relative to its drawn place: shifted by (x, y) canvas units,
 * turned and scaled about its own centre, and how opaque. Anything left out is
 * the drawn place.
 */
export interface Pose { x?: number; y?: number; rot?: number; scale?: number; opacity?: number }

/** One cycle of work, in seconds: a burst of typing, then a moment's thought. */
export const CYCLE = 4;

/** A tap of a hand on the keyboard: down fast, back a little slower. */
function tap(at: number): [number, Pose][] {
	return [[at, {}], [at + 1.5, { y: 1.6 }], [at + 4, {}]];
}

/**
 * Every part's poses over the cycle, as percentages of it, and straight lines
 * between them (CSS's `linear`); a part is at rest at 0% and 100%. Typing
 * takes up the first two thirds — the hands in an uneven rhythm, as typing
 * has — and the last third is for thinking: the eyes go up and away, the head
 * tilts, and the lamp on the antenna flickers with it.
 */
export const KEYFRAMES: Partial<Record<Part, [number, Pose][]>> = {
	'hand-l': [0, 11, 21, 29, 43, 52].flatMap(tap),
	'hand-r': [5, 16, 25, 36, 48, 57].flatMap(tap),
	// Down at the keys while typing, then up and away, one side and the other.
	eyes: [[0, { y: 0.5 }], [62, { y: 0.5 }], [66, { x: 1.2, y: -1.1 }], [78, { x: 1.2, y: -1.1 }], [82, { x: -1.2, y: -1.1 }], [93, { x: -1.2, y: -1.1 }], [97, {}]],
	head: [[62, {}], [67, { rot: -5, x: -0.4 }], [94, { rot: -5, x: -0.4 }], [99, {}]],
	lamp: [[64, {}], [67, { scale: 1.5 }], [70, {}], [73, { scale: 1.5 }], [76, {}], [79, { scale: 1.5 }], [82, {}]],
};

function transform(p: Pose): string {
	return `translate(${p.x ?? 0}px,${p.y ?? 0}px) rotate(${p.rot ?? 0}deg) scale(${p.scale ?? 1})`;
}

/**
 * The animation as CSS: a keyframes rule per moving part, and the rule that
 * plays it on that part of a `.tps-bot` that is `.tps-busy`. Turning and
 * scaling are about the part's own box, which `transform-box: fill-box` gives
 * an SVG group.
 */
export function animationCss(): Record<string, string | Record<string, string>> {
	const css: Record<string, string | Record<string, string>> = {};
	for (const [part, frames] of Object.entries(KEYFRAMES)) {
		const name = `tps-bot-${part}`;
		const steps: Record<string, string> = {};
		for (const [at, p] of [[0, {}] as [number, Pose], ...frames, [100, {}] as [number, Pose]]) steps[`${at}%`] = `transform: ${transform(p)}; opacity: ${p.opacity ?? 1};`;
		css[`@keyframes ${name}`] = steps;
		css[`.tps-bot.tps-busy .${name}`] = `transform-box: fill-box; transform-origin: center; animation: ${name} ${CYCLE}s linear infinite;`;
	}
	return css;
}

/** A part's pose at `t` seconds into the cycle: what the CSS above would show. Used to preview the animation. */
export function poseAt(part: Part, t: number): Pose {
	const frames = [[0, {}] as [number, Pose], ...(KEYFRAMES[part] ?? []), [100, {}] as [number, Pose]];
	const pct = ((t % CYCLE) / CYCLE) * 100;
	let i = 0;
	while (i + 1 < frames.length && frames[i + 1][0] <= pct) i++;
	const [a0, p0] = frames[i], [a1, p1] = frames[Math.min(i + 1, frames.length - 1)];
	const f = a1 > a0 ? (pct - a0) / (a1 - a0) : 0;
	const mix = (k: keyof Pose, d: number) => (p0[k] ?? d) + ((p1[k] ?? d) - (p0[k] ?? d)) * f;
	return { x: mix('x', 0), y: mix('y', 0), rot: mix('rot', 0), scale: mix('scale', 1), opacity: mix('opacity', 1) };
}
