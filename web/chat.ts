import A from 'aberdeen';
import { Marked } from 'marked';
import * as S from 'staffa';
import { chatLog, watchTask } from './conn.ts';
import { ELLIPSIS } from './util.ts';

const ESCAPES: Record<string, string> = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' };
const escape = (text: string) => text.replace(/[&<>"]/g, c => ESCAPES[c]);
const SAFE_URL = /^(?:https?:|mailto:|#|\/|\.\.?\/)/i;

// Marked emits raw HTML and any URL scheme as-is, so both are filtered here.
// Links open in a new tab, leaving the dashboard alone.
const md = new Marked({
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
 * The task's chat log: user and assistant messages as markdown; thinking, tool
 * calls (call and result on one line) and TPS notes as compact one-liners that
 * open a detail panel next to the chat.
 */
export function drawChat(pid: string, tid: string, $panel: S.Panel): void {
	const $chat = chatLog(pid, tid);
	let el!: HTMLElement;
	S.box({
		attrs: 'flex:1 min-height:0',
		// Content-sized grid rows: as flex or auto-row items, the one-liners (which
		// clip their overflow) would be squashed to nothing once the log overflows.
		contentAttrs: 'flex:1 min-height:0 overflow-y:auto display:grid grid-auto-rows:max-content gap:$2',
		content: () => {
			el = A() as HTMLElement;
			A.onEach($chat, ($e: any, i: number) => drawEntry($e, () => void $panel.open(`/p/${pid}/t/${tid}/agent/log/${i}`)));
		},
	});
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

/** A one-liner with a detail panel: the whole row opens it. */
const openable = A.insertCss({ '&': 'cursor:pointer', '&:hover': 'fg:$s-text' });

/** Draw an entry; `open` pushes its detail panel. */
function drawEntry($e: any, open: () => void): void {
	const line = (draw: () => void, opens = false) =>
		opens ? A(`small ${ELLIPSIS}`, openable, 'click=', open, draw) : A(`small ${ELLIPSIS}`, draw);
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

/** The pushed panel showing one log entry in full: a box per section. */
export function drawLogDetail($panel: S.Panel<{ pid: string; tid: string; i: number }>): void {
	const { pid, tid, i } = $panel.params;
	$panel.maxWidth = 'medium';
	watchTask(pid, tid);
	A(() => {
		const $e = chatLog(pid, tid)[i];
		$panel.loading = !$e; // the log may still be streaming in
		if (!$e) return;
		const title = $e.k === 'tool' ? $e.name : $e.k === 'note' ? 'tps: ' + $e.text : 'Thinking';
		$panel.title = title;
		const sections: [string, string | undefined][] =
			$e.k === 'tool' ? [['Request', $e.detail], [$e.error ? 'Response (error)' : 'Response', $e.resDetail]]
			: [[title, $e.detail]];
		for (const [header, text] of sections) {
			if (text === undefined) continue;
			S.box({ header, contentAttrs: 'p:0', content: () => A('pre r:0 white-space:pre-wrap overflow-wrap:anywhere text=', text) });
		}
	});
}
