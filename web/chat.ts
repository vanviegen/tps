import A from 'aberdeen';
import * as route from 'aberdeen/route';
import { Marked } from 'marked';
import * as S from 'staffa';
import { copy, copyCheck, gitFork, pencil, undo2 } from 'staffa/icons.js';
import { $state, chatLog } from './conn.ts';
import { serviceDialog } from './services.ts';
import { formatShell } from './shell.ts';
import { cmd, ELLIPSIS, hasWorkspace, isJob, pathTo } from './util.ts';

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
 * Turns far enough back are folded (see drawTurn). Above it all, the task's
 * `settings`.
 */
export function drawChat(pid: string, tid: string, $t: any, settings: () => void): void {
	const $chat = chatLog(pid, tid);
	// The turns before this one are folded: the newest that together hold
	// FOLD_AT entries stay open, the one crossing it included.
	const $foldBelow = A.derive(() => {
		let n = 0;
		for (let i = $chat.length - 1; i > 0; i--) {
			n += $chat[i].length;
			if (n >= FOLD_AT) return i;
		}
		return 0;
	});
	const $open = A.proxy({} as Record<number, boolean>);
	// Content-sized grid rows: as flex or auto-row items, the one-liners (which
	// clip their overflow) would be squashed to nothing once the log overflows.
	// The single column is capped at the space available, so nothing in the log
	// can push the left column wider than it is.
	const el = A('div flex:1 min-width:0 min-height:0 overflow-y:auto display:grid grid-template-columns:minmax(0,1fr) grid-auto-rows:max-content gap:$2', () => {
		// What the task is set to, where it is in reach when it matters most: as
		// it starts. A job has no settings of its own.
		if (!isJob($t)) A('div display:flex flex-direction:column gap:$2 pb:$3 border-bottom: 1px solid $s-faint;', settings);
		A(() => drawRules(pid, tid, $t));
		A.onEach($chat, ($turn: any[], i: number) => drawTurn($chat, $turn, i, $foldBelow, $open, pid, tid, $t));
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

/** How many of the newest entries are never folded away. */
const FOLD_AT = 200;

/**
 * One turn of the log. Folded, it keeps what the user said and the save points
 * its runs ended on, each run between them standing as a single line; opening
 * it loads what any gaps in it stand for (see hub.fold), and only then is the
 * rest of it drawn. The first turn, what came before the user said anything,
 * holds the point a task starts on, and is never folded.
 */
function drawTurn($chat: any[][], $turn: any[], i: number, $foldBelow: { value: number }, $open: Record<number, boolean>, pid: string, tid: string, $t: any): void {
	const load = async () => {
		const entries = await cmd('chatTurn', { pid, tid, turn: i });
		if (entries && A.unproxy($chat)[i] === A.unproxy($turn)) $chat[i] = entries;
	};
	const open = () => {
		$open[i] = true;
		if ($turn.some(e => e.k === 'gap')) void load();
	};
	const $folded = A.derive(() => i > 0 && i < $foldBelow.value && !$open[i]);
	A(() => {
		if (!$folded.value) return void A.onEach($turn, ($e: any) => $e.k === 'gap' ? drawHidden($e.n, load) : drawEntry($e, pid, tid, $t, i));
		let hidden = 0;
		const flush = () => {
			if (hidden) drawHidden(hidden, open);
			hidden = 0;
		};
		for (const e of $turn) {
			if (e.k === 'user' || e.k === 'mark') {
				flush();
				drawEntry(e, pid, tid, $t, i);
			} else hidden += e.k === 'gap' ? e.n : 1;
		}
		flush();
	});
}

/** A run of entries left out of the log, which a click brings in. */
function drawHidden(n: number, show: () => void): void {
	A(`small ${ELLIPSIS} fg:$s-muted`, openable, 'click=', show, 'text=', `⋯ ${n} step${n === 1 ? '' : 's'}`);
}

/**
 * The one thing the agent is told that the log itself never holds: the rules
 * TPS puts above its whole conversation (see daemon/prompts.go). They open
 * like any note, and are drawn rather than logged — so every task has them,
 * and always as the ones its next turn will run under.
 */
function drawRules(pid: string, tid: string, $t: any): void {
	const prompt = $state.projects[pid]?.agentPrompt ?? $state.hosts[$state.projects[pid]?.host]?.agentPrompt;
	if (!prompt || isJob($t)) return; // no agent works in a job
	drawEntry({ k: 'note', text: 'System message', detail: prompt }, pid, tid, $t);
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

/**
 * A message hides its buttons until it is hovered. They have a column of
 * their own, so they neither cover the text nor make it wrap.
 */
const copyable = A.insertCss({
	'&': 'min-width:0 display:grid grid-template-columns: minmax(0,1fr) auto; gap:$1',
	'& > .tools': 'align-self:start display:flex opacity:0 transition: opacity 0.15s;',
	'&:hover > .tools, & > .tools:focus-within': 'opacity:1',
});

/**
 * A message as markdown, with a button in its corner that copies its source,
 * and one that edits it where it can be.
 */
function drawMessage(attrs: string, text: string, edit?: () => void): void {
	A(`div ${attrs}`, copyable, () => {
		A('div', markdown, 'html=', md.parse(text) as string);
		A('div.tools', () => {
			if (edit) S.iconButton({ icon: pencil, tooltip: 'Edit', attrs: '.small fg:$s-muted', click: edit });
			const $done = A.proxy(false);
			A(() => S.iconButton({
				icon: $done.value ? copyCheck : copy, tooltip: 'Copy as markdown', attrs: '.small fg:$s-muted',
				click: async () => {
					await navigator.clipboard.writeText(text);
					$done.value = true;
					setTimeout(() => { $done.value = false; }, 1500);
				},
			}));
		});
	});
}

/** An entry of the log, in the turn it is in: the rules above the log have none. */
function drawEntry($e: any, pid: string, tid: string, $t: any, turn?: number): void {
	// A note about a run of a service opens that service's console instead,
	// which holds the output of its latest run alone.
	const open = () => !$e.service ? detailDialog($e, pid, tid, $t)
		: ($t.services ?? []).some((s: any) => s.name === $e.service && s.started === $e.run) ? serviceDialog(pid, tid, $t, $e.service)
		: S.alert(`The output of this run of ${$e.service} is gone: only its latest run is kept.`);
	const line = (draw: () => void, opens = false) =>
		opens ? A(`small ${ELLIPSIS}`, openable, 'click=', open, draw) : A(`small ${ELLIPSIS}`, draw);
	// The reviewer works in the task's own log, so what it leaves there wears a
	// colour of its own: everything between the note that starts a review and
	// the save point that ends it is what that second mind read and did.
	const prefix = (text: string) => A(`b fg:${$e.rev ? '$s-link' : '$s-accent'} text=`, text);
	switch ($e.k) {
		case 'user':
			// Editing a message is reverting to the point just above it, which
			// hands the message back to the composer; what a user message heads
			// is a turn of its own, so that point ends one of the turns before.
			A(() => drawMessage('.s-s.primary.outlined pv:$1 ph:$3 justify-self:start', $e.text, hasWorkspace($t) && turn ? () => {
				const point = chatLog(pid, tid).slice(0, turn).flat().findLast(e => e.k === 'mark');
				if (point) pointDialog(pid, tid, point, true);
			} : undefined));
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
	const row = (): void => {
		A('span flex:none display:flex', () => gitFork({ size: '1em' }));
		A('small flex:none #Save point');
		A('small flex:none', () => A('code text=', shortSha($e)));
		A(`small ${ELLIPSIS} min-width:0 text=`, $e.text);
		A('div flex:1 min-width:$3 h:1px bg:$s-faint');
	};
	const attrs = 'div display:flex align-items:center gap:$2 mt:$1 fg:$s-muted';
	A(() => {
		// Nothing to put back or to fork from once the workspace is gone
		// (merged, or closed): the line stays, as the record of the run.
		if (!hasWorkspace($t)) return void A(attrs, row);
		A(attrs, openable, 'click=', () => pointDialog(pid, tid, $e, false), row);
	});
}

const shortSha = ($e: any) => ($e.mark?.commit ?? '').slice(0, 7);

/** The code choices that name no point of their own (see pointDialog). */
const SAME_POINT = '';
const CODE_NOW = 'now';

/**
 * What to do with save points. Two things can be put back and each goes to a
 * point of its own: the conversation — the log and what the agent remembers —
 * and the working tree. The same point is the plain undo; two apart are the
 * interesting cases, a new mind on newer code or the whole discussion of code
 * that is gone. Forking does it to a copy and leaves this task be, and is
 * where the dialog starts unless asked otherwise, being the safe one.
 *
 * This is also the confirmation a revert needs: what it costs is written where
 * it is chosen, and the button that does it is red.
 */
function pointDialog(pid: string, tid: string, $e: any, revert: boolean): void {
	const points = A.peek(() => chatLog(pid, tid).flat().filter(e => e.k === 'mark').map(e => ({ value: e.id as string, label: `${shortSha(e)} · ${e.text}` })));
	const $use = A.proxy({ chat: $e.id as string, work: SAME_POINT, how: revert ? 'revert' : 'fork' });
	const $revert = A.derive(() => $use.how === 'revert');
	void S.dialog({
		header: 'Save point',
		attrs: 'w:34rem',
		content: close => S.form({
			submit: async () => {
				const { chat, work, how } = $use;
				const landed = await cmd('usePoint', { pid, tid, entry: chat, work: work === SAME_POINT ? chat : work === CODE_NOW ? '' : work, fork: how === 'fork' });
				close();
				if (landed && landed !== tid) route.go(pathTo(pid, landed as string));
			},
			content: () => {
				S.select({
					label: 'Conversation', options: points, bind: A.ref($use, 'chat'),
					help: 'The chat log and what the agent remembers end at this point. Whatever you said after it comes back to the message box.',
				});
				S.select({
					label: 'Code',
					options: [{ value: SAME_POINT, label: 'Same as conversation' }, { value: CODE_NOW, label: 'As it is now' }, ...points],
					bind: A.ref($use, 'work'),
					help: 'The working tree goes back to the files exactly as they were at this point.',
				});
				A('div.s-field', () => {
					S.buttonChooser({ attrs: 'align-self:flex-start', options: { fork: 'Fork', revert: 'Revert' }, bind: A.ref($use, 'how') });
					A(() => A('small fg:$s-muted text=', $revert.value
						? 'Do it to this task. What it undoes is gone for good.'
						: 'Do it to a second task in this project, and leave this one exactly as it is.'));
				});
			},
			actions: () => {
				S.button({ content: 'Cancel', attrs: '.neutral fg:$s-muted', click: close });
				A(() => $revert.value
					? S.button({ content: 'Revert', icon: undo2, type: 'submit', attrs: '.danger' })
					: S.button({ content: 'Fork', icon: gitFork, type: 'submit' }));
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
function detailDialog($e: any, pid: string, tid: string, $t: any): void {
	const title = $e.k === 'tool' ? $e.name : $e.k === 'note' ? 'tps' : 'Thinking';
	const section = (header: string, content: () => void) =>
		S.box({ header, attrs: 'flex-shrink:0', contentAttrs: 'p:0', content });
	// Wider than a staffa dialog goes by itself: these hold code, not prose.
	// A note carrying nothing but its own sentence is prose, and reads better
	// at the width a dialog has of its own.
	const wide = $e.k !== 'note' || !!$e.detail;
	const attrs = wide ? 'w:min(94vw,84rem) max-width:min(94vw,84rem)' : undefined;
	void S.dialog({ header: title, attrs, content: close => {
		if ($e.k === 'tool') {
			const fields = $e.req ?? ($e.detail ? oldFields($e.detail) : []);
			section('Request', () => {
				if (!fields.length) return void A('p p:$3 fg:$s-muted #(No input)');
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
			// A file the agent read (an image, often) opens in the task's VS Code.
			const path = /^read$/i.test($e.name) && fields.find((f: any) => f.k === 'file_path' || f.k === 'path')?.v;
			if (path && $t.codePort) S.button({
				content: 'Open in VS Code', attrs: '.outlined align-self:start',
				click: () => { close(); void cmd('openInCode', { pid, tid, path }); },
			});
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
