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
	const el = A('div flex:1 min-height:0 overflow-y:auto display:grid grid-auto-rows:max-content gap:$2', () => {
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

function drawEntry($e: any): void {
	const line = (draw: () => void, opens = false) =>
		opens ? A(`small ${ELLIPSIS}`, openable, 'click=', () => detailDialog($e), draw) : A(`small ${ELLIPSIS}`, draw);
	const prefix = (text: string) => A('b fg:$s-accent text=', text);
	switch ($e.k) {
		case 'user':
			A('div.s-s.primary.outlined pv:$1 ph:$3 justify-self:start', 'html=', md.parse($e.text) as string);
			break;
		case 'text':
			A('div', 'html=', md.parse($e.text) as string);
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

/** One log entry in full: a box per section. */
function detailDialog($e: any): void {
	const title = $e.k === 'tool' ? $e.name : $e.k === 'note' ? 'tps: ' + $e.text : 'Thinking';
	const sections: [string, string | undefined][] =
		$e.k === 'tool' ? [['Request', $e.detail], [$e.error ? 'Response (error)' : 'Response', $e.resDetail]]
		: [[title, $e.detail]];
	void S.dialog({ header: title, attrs: 'w:60rem', content: () => {
		for (const [header, text] of sections) {
			if (text === undefined) continue;
			S.box({ header, contentAttrs: 'p:0', content: () => A('pre r:0 max-height:60dvh white-space:pre-wrap overflow-wrap:anywhere text=', text) });
		}
	}});
}
