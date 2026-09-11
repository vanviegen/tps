import A from 'aberdeen';
import { Marked } from 'marked';
import * as S from 'staffa';
import { chatLog } from './conn.ts';
import { ELLIPSIS } from './util.ts';

const ESCAPES: Record<string, string> = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' };
const escape = (text: string) => text.replace(/[&<>"]/g, c => ESCAPES[c]);
const SAFE_URL = /^(?:https?:|mailto:|#|\/|\.\.?\/)/i;

// Marked emits raw HTML and any URL scheme as-is, so both are filtered here.
// Links open in a new tab, leaving the dashboard alone.
export const md = new Marked({
	gfm: true,
	breaks: true,
	renderer: {
		html: token => escape(token.raw),
		link(token) {
			const text = this.parser.parseInline(token.tokens);
			if (!SAFE_URL.test(token.href.trim())) return text;
			return `<a href="${escape(token.href)}" target="_blank">${text}</a>`;
		},
		image: token => SAFE_URL.test(token.href.trim())
			? `<img src="${escape(token.href)}" alt="${escape(token.text ?? '')}">`
			: escape(token.text ?? ''),
	},
});

/**
 * The task's chat log, filling its flex column and scrolling: user and assistant
 * messages as markdown; thinking, tool calls (call and result on one line)
 * and TPS notes as compact one-liners that open a dialog with the whole thing.
 */
export function drawChat(pid: string, tid: string): void {
	const $chat = chatLog(pid, tid);
	// Content-sized grid rows: as flex or auto-row items, the one-liners (which
	// clip their overflow) would be squashed to nothing once the log overflows.
	// The single column is capped at the space available, so nothing in the log
	// can push the left column wider than it is.
	const el = A('div flex:1 min-width:0 min-height:0 overflow-y:auto display:grid grid-template-columns:minmax(0,1fr) grid-auto-rows:max-content gap:$2', () => {
		A.onEach($chat, ($e: any) => drawEntry($e));
	}) as HTMLElement;
	// Follow new entries unless the user scrolled up to read something.
	let stick = true;
	el.addEventListener('scroll', () => {
		stick = el.scrollHeight - el.scrollTop - el.clientHeight < 60;
	});
	A(() => {
		void $chat.length;
		if (stick) requestAnimationFrame(() => { el.scrollTop = el.scrollHeight; });
	});
}

/** A one-liner with more behind it: the whole row opens the detail dialog. */
const openable = A.insertCss({ '&': 'cursor:pointer', '&:hover': 'fg:$s-text' });

/**
 * Rendered markdown, kept within the column: long words and URLs break, and
 * what cannot break (code blocks, tables, images) scrolls or scales itself.
 */
const markdown = A.insertCss({
	'&': 'min-width:0 overflow-wrap:anywhere',
	pre: 'max-width:100% overflow-x:auto',
	table: 'display:block max-width:100% overflow-x:auto',
	img: 'max-width:100% height:auto',
});

function drawEntry($e: any): void {
	const line = (draw: () => void, opens = false) =>
		opens ? A(`small ${ELLIPSIS}`, openable, 'click=', () => detailDialog($e), draw) : A(`small ${ELLIPSIS}`, draw);
	const prefix = (text: string) => A('b fg:$s-accent text=', text);
	switch ($e.k) {
		case 'user':
			A('div.s-s.primary.outlined pv:$1 ph:$3 justify-self:start', markdown, 'html=', md.parse($e.text) as string);
			break;
		case 'text':
			A('div', markdown, 'html=', md.parse($e.text) as string);
			break;
		case 'thinking':
			line(() => {
				prefix('Thinking');
				A('text=', ' · ' + $e.text);
			}, true);
			break;
		case 'tool':
			line(() => {
				prefix($e.name);
				if ($e.text) A('text=', ' · ' + $e.text);
				if ($e.arg) { A('text=', ' · '); A('code text=', $e.arg); }
				if ($e.res !== undefined) A(`span ${$e.error ? 'fg:$s-danger' : ''} text=`, ' · ' + $e.res);
			}, true);
			break;
		case 'note':
			line(() => {
				prefix('tps');
				A('text=', ' · ' + $e.text);
			}, !!$e.detail);
			break;
		case 'result':
			line(() => A(`span ${$e.error ? 'fg:$s-danger' : ''} text=`, $e.text));
			break;
	}
}

/**
 * A tool call's request: one row per field, its value in a code block that fills
 * the cell. The key column is as narrow as its words, so the values get all the
 * width there is; its padding matches the code block's, so the two line up.
 */
const reqTable = A.insertCss({
	'&': 'width:100% border-collapse:collapse',
	td: 'p:0 vertical-align:top border-bottom:0 border-top: 1px solid $s-faint;',
	'tr:first-child td': 'border-top:0',
	'td:first-child': 'w:1% white-space:nowrap p:$3 fg:$s-accent font-weight:600',
});

/** A block of verbatim text: wrapped, never clipped, and never a scroller of its own. */
function codeBlock(text: string): void {
	A('pre m:0 r:0 white-space:pre-wrap overflow-wrap:anywhere', () => A('code text=', text));
}

/**
 * Tool requests logged before they were sent field by field: 'key: value' or
 * 'key:\nvalue' blocks, a blank line apart. A block that opens with no key of
 * its own belongs to the value above it. Best effort — that old format cannot
 * tell a key from a line inside a value that looks like one — but nothing is
 * dropped: at worst a field ends up in the row above.
 */
function oldFields(detail: string): { k: string; v: string }[] {
	const fields: { k: string; v: string }[] = [];
	for (const part of detail.split('\n\n')) {
		const m = /^(\w+):[ \n]/.exec(part);
		if (m) fields.push({ k: m[1], v: part.slice(m[0].length) });
		else if (fields.length) fields[fields.length - 1].v += '\n\n' + part;
		else fields.push({ k: '', v: part });
	}
	return fields;
}

/**
 * One log entry in full: a box per section, each as tall as it needs to be —
 * the dialog is the only thing that scrolls.
 */
function detailDialog($e: any): void {
	const title = $e.k === 'tool' ? $e.name : $e.k === 'note' ? 'tps: ' + $e.text : 'Thinking';
	const section = (header: string, content: () => void) =>
		S.box({ header, attrs: 'flex-shrink:0', contentAttrs: 'p:0', content });
	// Wider than a staffa dialog goes by itself: these hold code, not prose.
	void S.dialog({ header: title, attrs: 'w:min(94vw,84rem) max-width:min(94vw,84rem)', content: () => {
		if ($e.k === 'tool') {
			const fields = $e.req ?? ($e.detail ? oldFields($e.detail) : []);
			section('Request', () => {
				if (!fields.length) return void A('p p:$3 fg:$s-muted #(no input)');
				A('table', reqTable, () => {
					for (const f of fields) A('tr', () => {
						A('td text=', f.k);
						A('td', () => codeBlock(f.v));
					});
				});
			});
			if ($e.resDetail !== undefined) {
				section($e.error ? 'Response (error)' : 'Response', () => codeBlock($e.resDetail));
			}
		} else if ($e.detail !== undefined) {
			section(title, () => codeBlock($e.detail));
		}
	}});
}
