/**
 * A one-line shell command laid out over lines for reading: a command per
 * line, the bodies of loops, conditionals, brace groups and subshells
 * indented, and the links of an '&&' or '||' chain and of a pipeline each on
 * a line of their own. Whitespace between commands is all it changes, so the
 * result runs as the original did. Anything it is not sure of — a command
 * already spanning lines, a case statement, unbalanced quotes — comes back as
 * it was.
 */
export function formatShell(cmd: string): string {
	const parsed = !cmd.includes('\n') && parse(cmd, 0, false);
	const out = parsed && parsed.end === cmd.length && layout(parsed.pieces);
	return out && out.includes('\n') ? out : cmd;
}

/** A command and the operator after it; a subshell keeps its own pieces, and what follows its ')'. */
interface Piece { text: string; op: string; group?: Piece[]; after?: string }

function layout(pieces: Piece[]): string | undefined {
	let out = '', depth = 0, cond = false, prev = '';
	const line = (d: number, s: string) => {
		const pad = '  '.repeat(d);
		out += (out ? '\n' : '') + pad + s.replaceAll('\n', '\n' + pad);
	};
	for (let piece of pieces) {
		let text = piece.text;
		const inner = piece.group && layout(piece.group);
		const grouped = inner?.includes('\n') ? `(\n  ${inner.replaceAll('\n', '\n  ')}\n)${piece.after ? ' ' + piece.after : ''}` : undefined;
		if (prev !== '' && prev !== ';' && prev !== '&') {
			// The next link of a chain or pipeline: '&&' and '||' bind alike, a pipe tighter.
			if (KEYWORDS.has(firstWord(text))) return;
			if (cond) out += ` ${prev} ${text}`;
			else {
				out += ' \\';
				line(prev === '&&' || prev === '||' ? depth : depth + 1, `${prev} ${grouped ?? text}`);
			}
		} else for (let glue = prev; ;) {
			// A command of its own, after any keywords it starts with.
			const w = firstWord(text), rest = text.slice(w.length).trim();
			if (w === 'do' || w === 'then') {
				if (glue !== ';' || !rest) return;
				out += `; ${w}`;
				depth++;
				cond = false;
				text = rest;
				glue = '';
				continue;
			}
			if (glue === '&') out += ' &';
			if (cond && glue === ';') {
				out += `; ${text}`;
			} else if (w === 'done' || w === 'fi' || w === '}') {
				if (--depth < 0) return;
				line(depth, text);
			} else if (w === 'else' || w === '{') {
				if (!rest || (w === 'else' && !depth)) return;
				line(w === 'else' ? depth - 1 : depth++, w);
				text = rest;
				glue = '';
				continue;
			} else {
				if (w === 'elif' && --depth < 0) return;
				if (CONDITIONS.has(w)) cond = true;
				line(depth, grouped ?? text);
			}
			break;
		}
		prev = piece.op;
	}
	if (prev === '&') out += ' &';
	return depth || cond ? undefined : out;
}

const CONDITIONS = new Set(['if', 'elif', 'while', 'until']);
const KEYWORDS = new Set([...CONDITIONS, 'for', 'do', 'then', 'else', 'done', 'fi', '{', '}', 'case']);

function firstWord(text: string): string {
	return /^\S*/.exec(text)![0];
}

/**
 * The command from position i cut at the operators between its commands —
 * ';', '&', '&&', '||', '|' and '|&' — leaving alone whatever is quoted,
 * substituted or in a [[ test ]]. A subshell where a command starts is cut
 * the same way, by a parse of its own that ends at its ')'. Undefined where
 * it cannot tell.
 */
function parse(cmd: string, i: number, inGroup: boolean): { pieces: Piece[]; end: number } | undefined {
	const pieces: Piece[] = [];
	const closers: string[] = []; // what the open quotes and brackets await
	let start = i, group: Piece[] | undefined, groupEnd = 0;
	const cut = (op: string, end: number, next: number) => {
		const text = cmd.slice(start, end).trim();
		pieces.push(group ? { text, op, group, after: cmd.slice(groupEnd, end).trim() } : { text, op });
		start = next;
		group = undefined;
	};
	while (i < cmd.length) {
		const c = cmd[i], next = cmd[i + 1], top = closers[closers.length - 1];
		const wordStart = i === 0 || /[\s;&|()]/.test(cmd[i - 1]);
		if (c === '\\') {
			i += 2;
		} else if (top === '"' || top === '`') {
			if (c === top) closers.pop();
			else if (top === '"' && c === '`') closers.push('`');
			else if (top === '"' && c === '$' && (next === '(' || next === '{')) closers.push(next === '(' ? ')' : '}'), i++;
			i++;
		} else if (c === "'") {
			// $'...' knows backslash escapes, plain '...' does not.
			const re = cmd[i - 1] === '$' ? /^(?:[^'\\]|\\.)*'/ : /^[^']*'/;
			const m = re.exec(cmd.slice(i + 1));
			if (!m) return;
			i += 1 + m[0].length;
		} else if (c === '"' || c === '`') {
			closers.push(c), i++;
		} else if (!top && c === '(' && next !== '(' && !cmd.slice(start, i).trim()) {
			const sub = parse(cmd, i + 1, true);
			if (!sub) return;
			group = sub.pieces;
			i = groupEnd = sub.end + 1;
		} else if (c === '(' || (c === '$' && next === '{')) {
			closers.push(c === '(' ? ')' : '}'), i += c === '(' ? 1 : 2;
		} else if (!top && c === ')' && inGroup) {
			break;
		} else if (c === ')' || (c === '}' && top === '}')) {
			if (closers.pop() !== c) return;
			i++;
		} else if (wordStart && c === '[' && next === '[' && /\s/.test(cmd[i + 2] ?? '')) {
			closers.push(']]'), i += 2;
		} else if (wordStart && top === ']]' && c === ']' && next === ']') {
			closers.pop(), i += 2;
		} else if (wordStart && c === '#') {
			i = cmd.length;
		} else if (top || !(c === ';' || c === '&' || c === '|')) {
			i++;
		} else if (c === ';') {
			if (next === ';' || next === '&') return; // a case statement
			cut(';', i, ++i);
		} else if (/[<>]/.test(cmd[i - 1] ?? '') || (c === '&' && next === '>')) {
			i++; // part of a redirection: >&, <&, &>, >|
		} else {
			const op = next === c || (c === '|' && next === '&') ? c + next : c;
			cut(op, i, i += op.length);
		}
	}
	if (closers.length || inGroup !== (cmd[i] === ')')) return;
	cut('', i, i);
	// A trailing ';' or '&' ends the last command rather than leaving an empty one.
	const end = pieces[pieces.length - 2];
	if (!pieces[pieces.length - 1].text && (end?.op === ';' || end?.op === '&')) {
		pieces.pop();
		if (end.op === ';') end.op = '';
	}
	return pieces.some(p => !p.text) ? undefined : { pieces, end: i };
}
