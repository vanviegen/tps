import type { Branch } from './branch.ts';

/**
 * Running the `claude` CLI inside a task's container, non-interactively.
 *
 * Claude's config dir is CLAUDE_CONFIG_DIR=/work/.tps/claude, i.e. inside the
 * task worktree: session history survives container rebuilds, travels with the
 * branch, and `--continue` picks the conversation right back up. Host OAuth
 * credentials are mounted read-only and copied in on first use (they are kept
 * out of git by the task's .tps/.gitignore).
 *
 * Output is requested as stream-json and rendered server-side into a compact
 * ANSI-colored terminal stream for the browser's xterm view.
 */

const RUN_SCRIPT = `
mkdir -p "$CLAUDE_CONFIG_DIR"
if [ -f /tps-host-claude-credentials.json ] && [ ! -f "$CLAUDE_CONFIG_DIR/.credentials.json" ]; then
	cp /tps-host-claude-credentials.json "$CLAUDE_CONFIG_DIR/.credentials.json" && chmod 600 "$CLAUDE_CONFIG_DIR/.credentials.json"
fi
echo $$ >/tmp/tps-agent.pid
exec claude -p "$TPS_PROMPT" --model "$TPS_MODEL" --output-format stream-json --verbose --dangerously-skip-permissions \\
	\${TPS_SYSTEM:+--append-system-prompt "$TPS_SYSTEM"} $TPS_EXTRA
`;

const KILL_SCRIPT = `
p=$(cat /tmp/tps-agent.pid 2>/dev/null) && [ -n "$p" ] || exit 0
kill -TERM "$p" 2>/dev/null || exit 0
for i in $(seq 1 50); do kill -0 "$p" 2>/dev/null || exit 0; sleep 0.1; done
kill -KILL "$p" 2>/dev/null || true
`;

export interface ClaudeOpts {
	prompt: string;
	model: string;
	system?: string; // appended to claude's system prompt
	resume?: boolean; // continue the branch's most recent session
	onData: (data: string) => void;
}

export async function runClaude(branch: Branch, opts: ClaudeOpts): Promise<number> {
	let buf = '';
	const handle = branch.exec(RUN_SCRIPT, {
		env: {
			TPS_PROMPT: opts.prompt,
			TPS_MODEL: opts.model || 'sonnet',
			TPS_SYSTEM: opts.system ?? '',
			TPS_EXTRA: opts.resume ? '--continue' : '',
		},
		onData: chunk => {
			buf += chunk;
			let nl;
			while ((nl = buf.indexOf('\n')) >= 0) {
				opts.onData(renderEvent(buf.slice(0, nl)));
				buf = buf.slice(nl + 1);
			}
		},
	});
	const code = await handle.done;
	if (buf.trim()) opts.onData(renderEvent(buf));
	return code;
}

export async function killClaude(branch: Branch): Promise<void> {
	if (!branch.container) return;
	await branch.exec(KILL_SCRIPT).done;
}

// --- stream-json → ANSI rendering ---

const dim = (s: string) => `\x1b[2m${s}\x1b[22m`;
const bold = (s: string) => `\x1b[1m${s}\x1b[22m`;
const cyan = (s: string) => `\x1b[36m${s}\x1b[39m`;
const green = (s: string) => `\x1b[32m${s}\x1b[39m`;
const red = (s: string) => `\x1b[31m${s}\x1b[39m`;
const crlf = (s: string) => s.replace(/\r?\n/g, '\r\n');

function oneLine(value: unknown, max: number): string {
	let s = typeof value === 'string' ? value : JSON.stringify(value) ?? '';
	s = s.replace(/\s+/g, ' ').trim();
	return s.length > max ? s.slice(0, max - 1) + '…' : s;
}

function renderEvent(line: string): string {
	if (!line.trim()) return '';
	let ev: any;
	try { ev = JSON.parse(line); } catch { return crlf(line) + '\r\n'; }
	switch (ev.type) {
		case 'system':
			if (ev.subtype === 'init') return dim(`· session ${ev.session_id} (${ev.model})`) + '\r\n';
			return '';
		case 'assistant': {
			let out = '';
			for (const block of ev.message?.content ?? []) {
				if (block.type === 'text' && block.text.trim()) out += crlf(block.text.trim()) + '\r\n';
				else if (block.type === 'thinking' && block.thinking?.trim()) out += dim(crlf(block.thinking.trim())) + '\r\n';
				else if (block.type === 'tool_use') out += `${cyan('⏺ ' + block.name)} ${dim(oneLine(block.input, 130))}\r\n`;
			}
			return out;
		}
		case 'user': {
			let out = '';
			for (const block of ev.message?.content ?? []) {
				if (block.type === 'tool_result') {
					const text = Array.isArray(block.content)
						? block.content.map((c: any) => c.type === 'text' ? c.text : `[${c.type}]`).join(' ')
						: block.content;
					out += dim(`  ⎿ ${oneLine(text ?? '(no output)', 160)}`) + '\r\n';
				}
			}
			return out;
		}
		case 'result': {
			const cost = ev.total_cost_usd ? ` · $${ev.total_cost_usd.toFixed(2)}` : '';
			const time = ev.duration_ms ? ` in ${Math.round(ev.duration_ms / 1000)}s` : '';
			return (ev.is_error ? red(bold(`✗ agent failed${time}`)) : green(bold(`✔ agent finished${time}${cost}`))) + '\r\n';
		}
		default:
			return '';
	}
}
