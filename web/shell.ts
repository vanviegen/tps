/**
 * A one-line shell command laid out over lines for reading: a command per
 * line, the bodies of loops, conditionals and brace groups indented, and the
 * links of an '&&' or '||' chain and of a pipeline each on a line of their
 * own. Whitespace between commands is all it changes, so the result runs as
 * the original did. Anything it is not sure of — a command already spanning
 * lines, a case statement, unbalanced quotes — comes back as it was.
 */
export function formatShell(cmd: string): string {
	if (cmd.includes('\n')) return cmd;
	const pieces = split(cmd);
	if (!pieces) return cmd;
	let out = '', depth = 0, cond = false, chained = false, prev = '';
	const line = (d: number, s: string) => out += (out ? '\n' : '') + '  '.repeat(d) + s;
	for (let { text, op } of pieces) {
		if (prev !== '' && prev !== ';' && prev !== '&') {
			// The next link of a chain or pipeline.
			if (KEYWORDS.has(firstWord(text))) return cmd;
			if (cond) out += ` ${prev} ${text}`;
			else if (prev === '|' || prev === '|&') {
				out += ' \\';
				line(depth + (chained ? 2 : 1), `${prev} ${text}`);
			} else {
				out += ` ${prev}`;
				line(depth + 1, text);
				chained = true;
			}
		} else for (let glue = prev; ;) {
			// A command of its own, after any keywords it starts with.
			chained = false;
			const w = firstWord(text), rest = text.slice(w.length).trim();
			if (w === 'do' || w === 'then') {
				if (glue !== ';' || !rest) return cmd;
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
				if (--depth < 0) return cmd;
				line(depth, text);
			} else if (w === 'else' || w === '{') {
				if (!rest || (w === 'else' && !depth)) return cmd;
				line(w === 'else' ? depth - 1 : depth++, w);
				text = rest;
				glue = '';
				continue;
			} else {
				if (w === 'elif' && --depth < 0) return cmd;
				if (CONDITIONS.has(w)) cond = true;
				line(depth, text);
			}
			break;
		}
		prev = op;
	}
	if (prev === '&') out += ' &';
	return depth || cond || !out.includes('\n') ? cmd : out;
}

const CONDITIONS = new Set(['if', 'elif', 'while', 'until']);
const KEYWORDS = new Set([...CONDITIONS, 'for', 'do', 'then', 'else', 'done', 'fi', '{', '}', 'case']);

function firstWord(text: string): string {
	return /^\S*/.exec(text)![0];
}

/**
 * The command cut at the operators between its commands — ';', '&', '&&',
 * '||', '|' and '|&' — leaving alone whatever is quoted, substituted, in a
 * subshell or in a [[ test ]]. Undefined where it cannot tell.
 */
function split(cmd: string): { text: string; op: string }[] | undefined {
	const pieces: { text: string; op: string }[] = [];
	const closers: string[] = []; // what the open quotes and brackets await
	let start = 0;
	const cut = (op: string, end: number, next: number) => {
		pieces.push({ text: cmd.slice(start, end).trim(), op });
		start = next;
	};
	for (let i = 0; i < cmd.length;) {
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
		} else if (c === '(' || (c === '$' && next === '{')) {
			closers.push(c === '(' ? ')' : '}'), i += c === '(' ? 1 : 2;
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
	if (closers.length) return;
	cut('', cmd.length, cmd.length);
	// A trailing ';' or '&' ends the last command rather than leaving an empty one.
	const end = pieces[pieces.length - 2];
	if (!pieces[pieces.length - 1].text && (end?.op === ';' || end?.op === '&')) {
		pieces.pop();
		if (end.op === ';') end.op = '';
	}
	return pieces.some(p => !p.text) ? undefined : pieces;
}
