import A from 'aberdeen';
import * as route from 'aberdeen/route';
import { Marked } from 'marked';
import * as S from 'staffa';
import { copy, copyCheck, gitFork, undo2 } from 'staffa/icons.js';
import { $state, chatLog } from './conn.ts';
import { formatShell } from './shell.ts';
import { cmd, ELLIPSIS, hasWorkspace, pathTo, restoreDraft } from './util.ts';

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
 * Once the log grows long, every turn but the last is folded into a block of
 * its own (see drawTurn).
 */
export function drawChat(pid: string, tid: string, $t: any): void {
	const $chat = chatLog(pid, tid);
	const $last = A.derive(() => $chat.length - 1);
	const $long = A.derive(() => $chat.reduce((n, turn) => n + turn.length, 0) > FOLD_AT);
	const $open = A.proxy({} as Record<number, boolean>);
	// Content-sized grid rows: as flex or auto-row items, the one-liners (which
	// clip their overflow) would be squashed to nothing once the log overflows.
	// The single column is capped at the space available, so nothing in the log
	// can push the left column wider than it is.
	const el = A('div flex:1 min-width:0 min-height:0 overflow-y:auto display:grid grid-template-columns:minmax(0,1fr) grid-auto-rows:max-content gap:$2', () => {
		A(() => drawRules(pid, tid, $t));
		A.onEach($chat, ($turn: any[], i: number) => drawTurn($chat, $turn, i, $last, $long, $open, pid, tid, $t));
	}) as HTMLElement;
	// Follow new entries unless the user scrolled up to read something.
	let stick = true;
	el.addEventListener('scroll', () => {
		stick = el.scrollHeight - el.scrollTop - el.clientHeight < 60;
	});
	A(() => {
		void $chat[$chat.length - 1].length;
		if (stick) requestAnimationFrame(() => { el.scrollTop = el.scrollHeight; });
	});
}

/** How many entries make a log long enough to fold. */
const FOLD_AT = 150;

/**
 * One turn of the log. Before the last, it folds into a single block — what
 * the user said, and the save points its run ended on — once the log is long;
 * opening it loads what any gaps in it stand for (see hub.fold), and only
 * then is the rest of it drawn. The first turn, what came before the user said
 * anything, holds the point a task starts on, and is never folded.
 */
function drawTurn($chat: any[][], $turn: any[], i: number, $last: { value: number }, $long: { value: boolean }, $open: Record<number, boolean>, pid: string, tid: string, $t: any): void {
	const hasGap = () => $turn.some(e => e.k === 'gap');
	const load = async () => {
		const entries = await cmd('chatTurn', { pid, tid, turn: i });
		if (entries && A.unproxy($chat)[i] === A.unproxy($turn)) $chat[i] = entries;
	};
	const $folded = A.derive(() => i > 0 && i < $last.value && !$open[i] && $long.value);
	A(() => {
		if (!$folded.value) return void A.onEach($turn, ($e: any) => $e.k === 'gap' ? drawGap($e, load) : drawEntry($e, pid, tid, $t));
		const marks = $turn.filter(e => e.k === 'mark');
		const steps = $turn.reduce((n, e) => n + (e.k === 'gap' ? e.n : e.k === 'user' || e.k === 'mark' ? 0 : 1), 0);
		A('div.s-s.primary.outlined pv:$1 ph:$3 display:grid', openable, 'click=', () => {
			$open[i] = true;
			if (hasGap()) void load();
		}, () => {
			A(`span ${ELLIPSIS} text=`, $turn[0].text);
			A(`small fg:$s-muted ${ELLIPSIS} text=`, [`${steps} step${steps === 1 ? '' : 's'}`, ...marks.map(m => m.text)].join(' · '));
		});
	});
}

/** Entries the server holds no longer (see hub.fold): a click loads their turn in full. */
function drawGap($e: any, load: () => void): void {
	A(`small ${ELLIPSIS} fg:$s-muted`, openable, 'click=', load, 'text=', `⋯ ${$e.n} earlier entr${$e.n === 1 ? 'y' : 'ies'}; click to load them`);
}

/**
 * The one thing the agent is told that the log itself never holds: the rules
 * TPS puts above its whole conversation (see daemon/prompts.go). They open
 * like any note, and are drawn rather than logged — so every task has them,
 * and always as the ones its next turn will run under.
 */
function drawRules(pid: string, tid: string, $t: any): void {
	const prompt = $state.projects[pid]?.agentPrompt ?? $state.hosts[$state.projects[pid]?.host]?.agentPrompt;
	if (!prompt) return;
	drawEntry({ k: 'note', text: 'the rules the agent works under, above this whole conversation', detail: prompt }, pid, tid, $t);
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

/** A message hides its copy button until it is hovered. */
const copyable = A.insertCss({
	'&': 'position:relative min-width:0',
	'& > .copy': 'position:absolute top:$1 right:$1 bg:$s-bg opacity:0 transition: opacity 0.15s;',
	'&:hover > .copy, & > .copy:focus-visible': 'opacity:1',
});

/** A message as markdown, with a button in its corner that copies its source. */
function drawMessage(attrs: string, text: string): void {
	A(`div ${attrs}`, copyable, () => {
		A('div', markdown, 'html=', md.parse(text) as string);
		const $done = A.proxy(false);
		A(() => S.iconButton({
			icon: $done.value ? copyCheck : copy, tooltip: 'Copy as markdown', attrs: '.small .copy fg:$s-muted',
			click: async () => {
				await navigator.clipboard.writeText(text);
				$done.value = true;
				setTimeout(() => { $done.value = false; }, 1500);
			},
		}));
	});
}

function drawEntry($e: any, pid: string, tid: string, $t: any): void {
	const line = (draw: () => void, opens = false) =>
		opens ? A(`small ${ELLIPSIS}`, openable, 'click=', () => detailDialog($e), draw) : A(`small ${ELLIPSIS}`, draw);
	// The reviewer works in the task's own log, so what it leaves there wears a
	// colour of its own: everything between the note that starts a review and
	// the save point that ends it is what that second mind read and did.
	const prefix = (text: string) => A(`b fg:${$e.rev ? '$s-link' : '$s-accent'} text=`, text);
	switch ($e.k) {
		case 'user':
			drawMessage('.s-s.primary.outlined pv:$1 ph:$3 justify-self:start', $e.text);
			break;
		case 'text':
			// The reviewer's messages stand among the agent's own, so they are
			// set apart the way the user's are, in the colour the rest of its
			// run wears.
			drawMessage($e.rev ? '.s-s.link.outlined pv:$1 ph:$3' : '', $e.text);
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
			// A note clips like the rest, and some carry an error behind them:
			// either way the dialog is where the whole of it is read.
			line(() => {
				prefix('tps');
				A('text=', ' · ' + $e.text);
			}, true);
			break;
		case 'result':
			line(() => A(`span ${$e.error ? 'fg:$s-danger' : ''} text=`, $e.text));
			break;
		case 'mark':
			drawMark($e, pid, tid, $t);
			break;
	}
}

/**
 * A save point: the rule between two runs, wearing the commit it holds and
 * what the run that ended there changed. The whole line is the button — what
 * there is to do with a point is a choice inside the dialog it opens, not a
 * row of verbs each needing explaining in the width of a chat log. A task
 * whose workspace is gone (merged, or closed) has no point to use.
 */
function drawMark($e: any, pid: string, tid: string, $t: any): void {
	const row = (rule: boolean) => (): void => {
		A('span flex:none display:flex', () => gitFork({ size: '1em' }));
		A('small flex:none #Save point');
		A('small flex:none', () => A('code text=', shortSha($e)));
		A(`small ${ELLIPSIS} min-width:0 text=`, $e.text);
		if (rule) A('div flex:1 min-width:$3 h:1px bg:$s-faint');
	};
	const attrs = 'div display:flex align-items:center gap:$2 mt:$1 fg:$s-muted';
	A(() => {
		// Nothing to put back or to fork from once the workspace is gone
		// (merged, or closed): the line stays, as the record of the run.
		if (!hasWorkspace($t)) return void A(attrs, row(true));
		A(attrs, openable, 'click=', () => pointDialog(pid, tid, $e, row(false)), row(true));
	});
}

const shortSha = ($e: any) => ($e.mark?.commit ?? '').slice(0, 7);

/**
 * What to do with a save point. Two things can be put back to it and they are
 * separate: the conversation — the log and what claude remembers — and the
 * working tree. Both is the plain undo; either alone is the interesting half,
 * a new mind on today's code or the whole discussion of code that is gone.
 * Forking does the same to a copy and leaves this task be, which is the only
 * difference between the two buttons.
 *
 * This is also the confirmation a revert needs: what it costs is written where
 * it is chosen, and the button that does it is red.
 */
function pointDialog(pid: string, tid: string, $e: any, title: () => void): void {
	const $use = A.proxy({ chat: true, work: true, fork: false });
	const nothing = () => !$use.chat && !$use.work;
	const box = (bind: 'chat' | 'work', label: string, help: string) => S.checkbox({
		label, help, checked: A.peek($use, bind),
		change: (e: Event) => { $use[bind] = (e.target as HTMLInputElement).checked; },
	});
	void S.dialog({
		attrs: 'w:34rem',
		header: () => A('div display:flex align-items:center gap:$2 min-width:0', title),
		content: close => S.form({
			submit: async () => {
				const done = await cmd('usePoint', { pid, tid, entry: $e.id, fork: $use.fork, chat: $use.chat, work: $use.work });
				close();
				if (!done) return;
				restoreDraft(pid, done.tid as string, done.draft as string);
				if (done.tid !== tid) route.go(pathTo(pid, done.tid as string));
			},
			content: () => {
				box('chat', 'Rewind the conversation',
					'The chat log and what the agent remembers both end here. Whatever you said after it comes back to the message box.');
				box('work', 'Revert the code',
					'The working tree goes back to the files exactly as they were at this point.');
			},
			actions: () => {
				const go = (fork: boolean) => { $use.fork = fork; };
				// Quietest of the three, and first, so the two that do something
				// sit together against the corner.
				S.button({ content: 'Cancel', attrs: '.neutral fg:$s-muted', click: close });
				A(() => S.button({
					content: 'Fork', icon: gitFork, type: 'submit', attrs: '.outlined', disabled: nothing(),
					tooltip: nothing() ? 'Pick at least one of the two' : 'Do it to a second task in this project, and leave this one exactly as it is',
					click: () => go(true),
				}));
				A(() => S.button({
					content: 'Revert', icon: undo2, type: 'submit', attrs: '.danger', disabled: nothing(),
					tooltip: nothing() ? 'Pick at least one of the two' : 'Do it to this task. What it undoes is gone for good',
					click: () => go(false),
				}));
			},
		}),
	});
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
	const title = $e.k === 'tool' ? $e.name : $e.k === 'note' ? 'tps' : 'Thinking';
	const section = (header: string, content: () => void) =>
		S.box({ header, attrs: 'flex-shrink:0', contentAttrs: 'p:0', content });
	// Wider than a staffa dialog goes by itself: these hold code, not prose.
	// A note carrying nothing but its own sentence is prose, and reads better
	// at the width a dialog has of its own.
	const wide = $e.k !== 'note' || !!$e.detail;
	const attrs = wide ? 'w:min(94vw,84rem) max-width:min(94vw,84rem)' : undefined;
	void S.dialog({ header: title, attrs, content: () => {
		if ($e.k === 'tool') {
			const fields = $e.req ?? ($e.detail ? oldFields($e.detail) : []);
			section('Request', () => {
				if (!fields.length) return void A('p p:$3 fg:$s-muted #(no input)');
				A('table', reqTable, () => {
					for (const f of fields) A('tr', () => {
						A('td text=', f.k);
						A('td', () => codeBlock(/^bash$/i.test($e.name) && f.k === 'command' ? formatShell(f.v) : f.v));
					});
				});
			});
			if ($e.resDetail !== undefined) {
				section($e.error ? 'Response (error)' : 'Response', () => codeBlock($e.resDetail));
			}
		} else if ($e.k === 'note') {
			// The note is prose and wraps under the header; what it carries (a
			// build log, an error) is not, and keeps its own lines in a box.
			A('p m:0 overflow-wrap:anywhere text=', $e.text);
			if ($e.detail) section('Details', () => codeBlock($e.detail));
		} else if ($e.detail !== undefined) {
			section(title, () => codeBlock($e.detail));
		}
	}});
}
