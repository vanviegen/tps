/**
 * As much of a terminal as a service log needs: the SGR sequences that colour
 * output, and the cursor moves that progress lines are made of — a carriage
 * return back to the start of the line, a backspace, an erased line, the odd
 * cursor move. Everything else a terminal knows (an alternate screen, scroll
 * regions, absolute rows) needs a screen of a known size, which a log that is
 * read as a scroll of text does not have; those sequences are swallowed rather
 * than printed, so what is left reads as the terminal would have shown it.
 *
 * The log arrives whole (the daemon sends the last 32k of it on every change),
 * so this is a pure function of the text: cheap enough to run again on each
 * update, and stateless between them.
 */

/** How a run of characters looks, as it stands while they are written. */
interface Style {
	fg?: string;
	bg?: string;
	bold?: boolean;
	dim?: boolean;
	italic?: boolean;
	underline?: boolean;
	strike?: boolean;
	inverse?: boolean;
	hidden?: boolean;
}

/** One line of the log, a cell per character, each with the CSS it was written in. */
interface Line { chars: string[]; css: string[] }

/**
 * The sixteen ANSI colours, picked to sit on the dashboard's dark surfaces:
 * the terminal defaults (a black that is invisible here, a blue that is barely
 * legible) are made for a white background or a terminal's own palette.
 */
const COLORS = [
	'#5c6370', '#e06c75', '#98c379', '#d19a66', '#61afef', '#c678dd', '#56b6c2', '#abb2bf',
	'#7f848e', '#ff7b86', '#b5e08c', '#e5c07b', '#7fc6ff', '#d8a0e8', '#6fdbe8', '#ffffff',
];

/** The levels of xterm's 6×6×6 colour cube, in the 256-colour palette. */
const CUBE = [0, 95, 135, 175, 215, 255];

const rgb = (r: number, g: number, b: number) => `rgb(${r & 255},${g & 255},${b & 255})`;

/** A colour of the 256-colour palette: the sixteen above, the cube, then the greys. */
function color256(n: number): string {
	if (!(n >= 0 && n <= 255)) return '';
	if (n < 16) return COLORS[n];
	if (n < 232) {
		n -= 16;
		return rgb(CUBE[(n / 36 | 0) % 6], CUBE[(n / 6 | 0) % 6], CUBE[n % 6]);
	}
	const v = 8 + 10 * (n - 232);
	return rgb(v, v, v);
}

const ESCAPES: Record<string, string> = { '&': '&amp;', '<': '&lt;', '>': '&gt;' };
const escape = (text: string) => text.replace(/[&<>]/g, c => ESCAPES[c]);

/** The style as one CSS declaration, shared by every cell written in it. */
function toCss(style: Style): string {
	let { fg, bg } = style;
	// Inverse without colours of its own swaps what the console itself uses.
	if (style.inverse) [fg, bg] = [bg || 'var(--s-bg)', fg || 'var(--s-text)'];
	if (style.hidden) fg = 'transparent';
	const out: string[] = [];
	if (fg) out.push('color:' + fg);
	if (bg) out.push('background:' + bg);
	if (style.bold) out.push('font-weight:600');
	if (style.dim) out.push('opacity:.65');
	if (style.italic) out.push('font-style:italic');
	const deco = [style.underline ? 'underline' : '', style.strike ? 'line-through' : ''].filter(Boolean);
	if (deco.length) out.push('text-decoration:' + deco.join(' '));
	return out.join(';');
}

/**
 * The log as HTML: one `<span>` per run of equally styled characters, the text
 * escaped, lines separated by newlines — for a `<pre>`, in other words. Cursor
 * moves are played out before rendering, so a progress line that wrote itself
 * ten times over shows only what it says now.
 */
export function ansiToHtml(text: string): string {
	const lines: Line[] = [];
	let row = 0, col = 0;
	let style: Style = {};
	let css = '';
	let saved = { row: 0, col: 0 };

	/** The line the cursor is on, brought into being if the text has not reached it yet. */
	const line = (): Line => {
		while (lines.length <= row) lines.push({ chars: [], css: [] });
		return lines[row];
	};

	const put = (ch: string) => {
		const l = line();
		while (l.chars.length < col) { l.chars.push(' '); l.css.push(''); }
		l.chars[col] = ch;
		l.css[col] = css;
		col++;
	};

	const eraseLine = (mode: number) => {
		const l = line();
		if (mode === 0) { l.chars.length = Math.min(l.chars.length, col); l.css.length = l.chars.length; }
		else if (mode === 1) for (let i = 0; i <= col && i < l.chars.length; i++) { l.chars[i] = ' '; l.css[i] = ''; }
		else if (mode === 2) { l.chars.length = 0; l.css.length = 0; }
	};

	const eraseDisplay = (mode: number) => {
		if (mode === 0) { eraseLine(0); lines.length = row + 1; }
		else if (mode === 1) { for (let i = 0; i < row; i++) lines[i] = { chars: [], css: [] }; eraseLine(1); }
		else { lines.length = 0; row = col = 0; } // 2 and 3: `clear` starts over
	};

	/** A single SGR sequence: the colours and attributes, in the order they were given. */
	const sgr = (nums: number[]) => {
		for (let i = 0; i < nums.length; i++) {
			const n = nums[i] || 0; // an omitted parameter means 0
			if (n === 38 || n === 48) {
				const key = n === 38 ? 'fg' : 'bg';
				if (nums[i + 1] === 5) { style[key] = color256(nums[i + 2]); i += 2; }
				else if (nums[i + 1] === 2) { style[key] = rgb(nums[i + 2], nums[i + 3], nums[i + 4]); i += 4; }
				continue;
			}
			if (n === 0) style = {};
			else if (n === 1) style.bold = true;
			else if (n === 2) style.dim = true;
			else if (n === 3) style.italic = true;
			else if (n === 4) style.underline = true;
			else if (n === 7) style.inverse = true;
			else if (n === 8) style.hidden = true;
			else if (n === 9) style.strike = true;
			else if (n === 22) { style.bold = style.dim = false; }
			else if (n === 23) style.italic = false;
			else if (n === 24) style.underline = false;
			else if (n === 27) style.inverse = false;
			else if (n === 28) style.hidden = false;
			else if (n === 29) style.strike = false;
			else if (n >= 30 && n <= 37) style.fg = COLORS[n - 30];
			else if (n === 39) style.fg = undefined;
			else if (n >= 40 && n <= 47) style.bg = COLORS[n - 40];
			else if (n === 49) style.bg = undefined;
			else if (n >= 90 && n <= 97) style.fg = COLORS[n - 82];
			else if (n >= 100 && n <= 107) style.bg = COLORS[n - 92];
		}
		css = toCss(style);
	};

	/** A CSI sequence: its parameters, and the letter that says what to do with them. */
	const csi = (params: string, final: string) => {
		if (params[0] === '?' || params[0] === '>' || params[0] === '<' || params[0] === '=') return; // private: DECTCEM and friends
		const nums = params.split(';').map(p => parseInt(p, 10) || 0);
		const n = nums[0];
		switch (final) {
			case 'm': sgr(nums); break;
			case 'A': row = Math.max(0, row - (n || 1)); break;
			case 'B': row += n || 1; break;
			case 'C': col += n || 1; break;
			case 'D': col = Math.max(0, col - (n || 1)); break;
			case 'E': row += n || 1; col = 0; break;
			case 'F': row = Math.max(0, row - (n || 1)); col = 0; break;
			case 'G': case '`': col = Math.max(0, (n || 1) - 1); break;
			// An absolute position wants a screen; only its column means anything here.
			case 'H': case 'f': col = Math.max(0, (nums[1] || 1) - 1); break;
			case 'J': eraseDisplay(n); break;
			case 'K': eraseLine(n); break;
			case 'X': { const l = line(); for (let i = col; i < col + (n || 1) && i < l.chars.length; i++) { l.chars[i] = ' '; l.css[i] = ''; } break; }
			case 's': saved = { row, col }; break;
			case 'u': ({ row, col } = saved); break;
		}
	};

	for (let i = 0; i < text.length; i++) {
		const ch = text[i];
		if (ch === '\x1b') {
			const kind = text[i + 1];
			if (kind === '[') {
				// Parameter and intermediate bytes run up to the final one, 0x40-0x7e.
				let j = i + 2;
				while (j < text.length && text.charCodeAt(j) >= 0x20 && text.charCodeAt(j) <= 0x3f) j++;
				if (j < text.length) csi(text.slice(i + 2, j), text[j]);
				i = j;
			} else if (kind === ']') {
				// An OSC sets the window title and such: swallowed, payload and all.
				let j = i + 2;
				while (j < text.length && text[j] !== '\x07' && !(text[j] === '\x1b' && text[j + 1] === '\\')) j++;
				i = text[j] === '\x1b' ? j + 1 : j;
			} else if (kind === '(' || kind === ')' || kind === '#') i += 2;
			else i += 1; // whatever else it was: two characters, neither of them text
			continue;
		}
		if (ch === '\n') { row++; col = 0; line(); }
		else if (ch === '\r') col = 0;
		else if (ch === '\b') col = Math.max(0, col - 1);
		else if (ch === '\t') col = (col / 8 | 0) * 8 + 8;
		else if (ch >= ' ') {
			// A cell holds a whole character, surrogate pair and all.
			const code = text.charCodeAt(i);
			if (code >= 0xd800 && code < 0xdc00 && i + 1 < text.length) { put(text.slice(i, i + 2)); i++; }
			else put(ch);
		}
	}

	return lines.map(l => {
		// Trailing blanks are invisible, and only the HTML would be longer for them.
		let end = l.chars.length;
		while (end > 0 && l.chars[end - 1] === ' ' && !l.css[end - 1]) end--;
		let html = '', run = '', runCss = '';
		const flush = () => {
			if (!run) return;
			html += runCss ? `<span style="${runCss}">${escape(run)}</span>` : escape(run);
			run = '';
		};
		for (let i = 0; i < end; i++) {
			if (l.css[i] !== runCss) { flush(); runCss = l.css[i]; }
			run += l.chars[i];
		}
		flush();
		return html;
	}).join('\n');
}
