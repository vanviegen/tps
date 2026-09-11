import A from 'aberdeen';

/**
 * The split down the middle: what to do on the left (a task's chat, a plan's
 * settings), the thing itself on the right (VS Code, the description) — and
 * the line between them, which is a handle. Drag it to move the split,
 * double-click it to put it back, and with it focused the arrow keys nudge it.
 *
 * The left column's width is a CSS variable rather than a style on the column
 * itself, because the frames VS Code lives in are not in the right column at
 * all: they are fixed-positioned against that variable (see code.ts). Moving
 * the split moves both, in the same frame.
 *
 * A width that was dragged is a preference, so it is kept in localStorage; one
 * that never was stays the default, a third of the window, and follows the
 * window as it is resized.
 */

const WIDTH_KEY = 'tps.splitWidth';

/** Where the split sits by default: a third of the window, and never a wall of text. */
const DEFAULT = 'min(33.3vw, 600px)';
/** Narrower than this the chat is unusable; wider than this fraction of the window, VS Code is. */
const MIN = 260;
const MAX_PART = 0.7;

/** The dragged width in pixels, or 0 for "never dragged": the default. */
const $split = A.proxy({ px: read() });

A.insertGlobalCss({
	'.tps-grip': 'flex:none w:1px min-width:0 bg:$s-faint position:relative z-index:1 cursor:col-resize touch-action:none',
	// The line is a hairline, the grip a comfortable target: it reaches out
	// over the columns on either side, VS Code's frame included, which is what
	// the z-index above is for (the frame is fixed, so it paints over the flow).
	'.tps-grip::before': "content:'' position:absolute top:0 bottom:0 left:-4px right:-4px",
	'.tps-grip:hover, .tps-grip:focus-visible': 'bg:$s-accent',
	// While dragging, the pointer belongs to the grip alone: no text is selected
	// behind it, the frames don't swallow it, and they follow it without the
	// easing that makes a move to a new column look smooth but a drag look late.
	'body.tps-resizing': 'user-select:none cursor:col-resize',
	'body.tps-resizing .tps-code': 'pointer-events:none transition:none',
});

A(() => { A.cssVars.leftw = $split.px ? `min(${$split.px}px, ${MAX_PART * 100}vw)` : DEFAULT; });

/** Where the frame of a task's VS Code starts: after the sidebar and the left column, and the grip. */
export const TASK_CODE_LEFT = 'calc(var(--sidew) + var(--leftw) + 1px)';
/** Where a full-width VS Code starts: right after the sidebar. */
export const WIDE_CODE_LEFT = 'var(--sidew)';

/** The two columns, with the handle between them. */
export function drawSplit(left: () => void, right: () => void): void {
	A('div display:flex flex-direction:column gap:$3 w:var(--leftw) flex:none min-width:0 p:$3 overflow:hidden', left);
	drawGrip();
	A('div flex:1 min-width:0 overflow:auto p:$3', right);
}

function drawGrip(): void {
	A('div.tps-grip role=separator aria-orientation=vertical tabindex=0 aria-label="Resize the left column"',
		'pointerdown=', drag,
		'dblclick=', () => { setWidth(0); persist(); },
		'keydown=', (e: KeyboardEvent) => {
			const step = e.key === 'ArrowLeft' ? -16 : e.key === 'ArrowRight' ? 16 : 0;
			if (!step) return;
			e.preventDefault();
			setWidth(columnWidth(e.currentTarget as HTMLElement) + step);
			persist();
		});
}

/** Follow the pointer until it lets go. */
function drag(e: PointerEvent): void {
	if (e.button) return;
	const grip = e.currentTarget as HTMLElement;
	// Widths are measured from where the pointer took hold, so the split keeps
	// its distance to it rather than jumping under it on the first move.
	const origin = e.clientX - columnWidth(grip);
	e.preventDefault();
	grip.focus();
	// The capture is what keeps the moves coming while the pointer is over a
	// frame; the class is what keeps the frame from reacting to them anyway.
	grip.setPointerCapture(e.pointerId);
	document.body.classList.add('tps-resizing');
	const move = (ev: PointerEvent) => setWidth(ev.clientX - origin);
	const done = () => {
		grip.removeEventListener('pointermove', move);
		document.body.classList.remove('tps-resizing');
		persist();
	};
	grip.addEventListener('pointermove', move);
	grip.addEventListener('pointerup', done, { once: true });
	grip.addEventListener('pointercancel', done, { once: true });
}

/** What the left column is wide right now, dragged or not. */
function columnWidth(grip: HTMLElement): number {
	return (grip.previousElementSibling as HTMLElement | null)?.getBoundingClientRect().width ?? MIN;
}

/**
 * Move the split to `px` — 0 puts it back to the default — within what is
 * usable. Keeping it is the caller's to do, once it is done moving.
 */
function setWidth(px: number): void {
	$split.px = px ? Math.round(Math.max(MIN, Math.min(px, window.innerWidth * MAX_PART))) : 0;
}

function persist(): void {
	const px = A.peek($split, 'px');
	try {
		if (px) localStorage.setItem(WIDTH_KEY, String(px));
		else localStorage.removeItem(WIDTH_KEY);
	} catch {} // storage off or full: the split stays where it is for as long as this tab lives
}

function read(): number {
	try {
		const px = Number(localStorage.getItem(WIDTH_KEY));
		return px >= MIN ? px : 0;
	} catch {
		return 0;
	}
}
