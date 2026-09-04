import { spawn, type ChildProcess } from 'node:child_process';
import type { Container } from './container.ts';

/**
 * A live `claude` process inside a task's container, talking stream-json on
 * both ends: user/steering messages go in over stdin, events come out and are
 * condensed into chat entries. The process stays alive between turns, so
 * follow-up messages are instant; after it dies, `resume` (--continue) picks
 * the conversation back up from the state in the mounted claude dir.
 */

const START_SCRIPT = `
mkdir -p "$CLAUDE_CONFIG_DIR"
if [ -f /tps-host-claude-credentials.json ] && [ ! -f "$CLAUDE_CONFIG_DIR/.credentials.json" ]; then
	cp /tps-host-claude-credentials.json "$CLAUDE_CONFIG_DIR/.credentials.json" && chmod 600 "$CLAUDE_CONFIG_DIR/.credentials.json"
fi
echo $$ >/tmp/tps-agent.pid
exec claude -p --input-format stream-json --output-format stream-json --verbose \\
	--dangerously-skip-permissions --model "$TPS_MODEL" --append-system-prompt "$TPS_SYSTEM" \\
	\${TPS_BUDGET:+--max-budget-usd "$TPS_BUDGET"} $TPS_EXTRA
`;

const KILL_SCRIPT = `
p=$(cat /tmp/tps-agent.pid 2>/dev/null) && [ -n "$p" ] || exit 0
kill -TERM "$p" 2>/dev/null || exit 0
for i in $(seq 1 30); do kill -0 "$p" 2>/dev/null || exit 0; sleep 0.1; done
kill -KILL "$p" 2>/dev/null || true
`;

/**
 * One condensed line of a task's chat log. A tool call and its result share a
 * single entry: the result is merged in later, addressed by `id`.
 */
export interface ChatEntry {
	k: 'user' | 'text' | 'thinking' | 'tool' | 'note' | 'result';
	id?: string; // tool_use id; an entry re-sent with a known id replaces the original
	text?: string; // markdown (user/text/note), or a one-line brief (thinking/tool/result)
	name?: string; // tool name
	arg?: string; // tool: the main argument, one line
	res?: string; // tool: the result, one line
	detail?: string; // full request (tool) or full text (thinking/note)
	resDetail?: string; // full result text
	error?: boolean;
	t: number;
}

export function entry(partial: Omit<ChatEntry, 't'>): ChatEntry {
	return { t: Date.now(), ...partial };
}

export interface SessionOpts {
	container: Container;
	model: string;
	system: string;
	resume: boolean; // --continue the task's most recent session
	budget?: number; // USD this session may spend (--max-budget-usd)
	onEntry: (e: ChatEntry) => void;
	onUpdate: (e: ChatEntry) => void; // an earlier entry (matched by id) changed
	onTurnEnd: (costDelta: number) => void; // a result event arrived; costDelta = USD spent since the previous one
	onExit: (code: number, errTail: string) => void;
}

export class ChatSession {
	turnActive = false;
	private child: ChildProcess;
	private buf = '';
	private errTail = '';
	private costReported = 0; // cumulative session cost of the last result event
	private pending = new Map<string, ChatEntry>(); // tool calls awaiting their result
	private opts: SessionOpts;
	private exitResolve!: () => void;
	private exited = new Promise<void>(r => { this.exitResolve = r; });

	constructor(opts: SessionOpts) {
		this.opts = opts;
		this.child = spawn('podman', [
			'exec', '-i',
			'-e', `TPS_MODEL=${opts.model}`,
			'-e', `TPS_SYSTEM=${opts.system}`,
			'-e', `TPS_EXTRA=${opts.resume ? '--continue' : ''}`,
			'-e', `TPS_BUDGET=${opts.budget !== undefined ? opts.budget.toFixed(2) : ''}`,
			opts.container.name, 'bash', '-lc', START_SCRIPT,
		], { stdio: ['pipe', 'pipe', 'pipe'] });
		this.child.stdout!.on('data', d => this.onData(d.toString()));
		this.child.stderr!.on('data', d => { this.errTail = (this.errTail + d).slice(-4000); });
		this.child.on('error', e => { this.errTail += `\n${e.message}`; });
		this.child.on('close', code => {
			this.turnActive = false;
			opts.onExit(code ?? -1, this.errTail.trim());
			this.exitResolve();
		});
	}

	send(text: string): void {
		this.turnActive = true;
		this.write({ type: 'user', message: { role: 'user', content: [{ type: 'text', text }] } });
	}

	/** Interrupt the current turn, then make sure the process is gone.
	 * Resolves only after the exit was processed (onExit has run). */
	async stop(): Promise<void> {
		this.write({ type: 'control_request', request_id: 'stop', request: { subtype: 'interrupt' } });
		for (let i = 0; i < 40 && this.turnActive; i++) await new Promise(r => setTimeout(r, 100));
		// The podman exec client can't signal into the container; kill from within.
		this.opts.container.exec(KILL_SCRIPT).done.catch(() => {});
		await new Promise(r => setTimeout(r, 300));
		this.kill();
		await this.exited;
	}

	/** Kill just the client process (the container side dies with the container). */
	kill(): void {
		this.child.kill('SIGKILL');
	}

	private write(msg: unknown): void {
		try { this.child.stdin!.write(JSON.stringify(msg) + '\n'); } catch {}
	}

	private onData(chunk: string): void {
		this.buf += chunk;
		let nl;
		while ((nl = this.buf.indexOf('\n')) >= 0) {
			const line = this.buf.slice(0, nl);
			this.buf = this.buf.slice(nl + 1);
			if (!line.trim()) continue;
			try { this.onEvent(JSON.parse(line)); } catch {}
		}
	}

	// --- stream-json events → chat entries ---

	private onEvent(ev: any): void {
		switch (ev.type) {
			case 'assistant':
				for (const block of ev.message?.content ?? []) {
					if (block.type === 'text' && block.text.trim()) {
						this.opts.onEntry(entry({ k: 'text', text: block.text.trim() }));
					} else if (block.type === 'thinking' && block.thinking?.trim()) {
						const text = block.thinking.trim();
						this.opts.onEntry(entry({ k: 'thinking', text: oneLine(text, 110), detail: clip(text) }));
					} else if (block.type === 'tool_use') {
						const e = entry({
							k: 'tool', id: block.id, name: block.name, ...toolBits(block.name, block.input),
							detail: clip(prettyInput(block.input)),
						});
						if (block.id) this.pending.set(block.id, e);
						this.opts.onEntry(e);
					}
				}
				break;
			case 'user':
				for (const block of ev.message?.content ?? []) {
					if (block.type !== 'tool_result') continue;
					const text = Array.isArray(block.content)
						? block.content.map((c: any) => c.type === 'text' ? c.text : `[${c.type}]`).join(' ')
						: String(block.content ?? '');
					const e = block.tool_use_id && this.pending.get(block.tool_use_id);
					if (e) {
						this.pending.delete(block.tool_use_id);
						e.res = oneLine(text || '(no output)', 140);
						e.resDetail = clip(text);
						e.error = !!block.is_error;
						this.opts.onUpdate(e);
					} else { // result without a tracked call (shouldn't normally happen)
						this.opts.onEntry(entry({
							k: 'tool', name: 'result', res: oneLine(text || '(no output)', 140),
							resDetail: clip(text), error: !!block.is_error,
						}));
					}
				}
				break;
			case 'result': {
				this.turnActive = false;
				const total = ev.total_cost_usd ?? this.costReported;
				const delta = Math.max(0, total - this.costReported);
				this.costReported = total;
				const secs = ev.duration_ms ? `${Math.round(ev.duration_ms / 1000)}s` : '';
				const cost = delta ? ` · $${delta.toFixed(2)}` : '';
				const why = ev.is_error && typeof ev.subtype === 'string' ? ` (${ev.subtype.replace(/^error_/, '').replace(/_/g, ' ')})` : '';
				this.opts.onEntry(entry({ k: 'result', error: !!ev.is_error, text: `${ev.is_error ? 'turn failed' + why : 'turn finished'} · ${secs}${cost}` }));
				this.opts.onTurnEnd(delta);
				break;
			}
		}
	}
}

function oneLine(value: unknown, max: number): string {
	let s = typeof value === 'string' ? value : JSON.stringify(value) ?? '';
	s = s.replace(/\s+/g, ' ').trim();
	return s.length > max ? s.slice(0, max - 1) + '…' : s;
}

function clip(s: string, max = 16_000): string {
	return s.length > max ? s.slice(0, max) + `\n… (${s.length - max} more characters)` : s;
}

/** A tool call's description and main argument, for the one-line rendering. */
function toolBits(name: string, input: any): { text: string; arg: string } {
	const text = typeof input?.description === 'string' ? input.description : '';
	let arg: unknown;
	switch (name) {
		case 'Bash': arg = input?.command; break;
		case 'Edit': arg = `${input?.file_path ?? ''}: ${oneLine(input?.old_string, 40)} → ${oneLine(input?.new_string, 40)}`; break;
		case 'Write': arg = `${input?.file_path ?? ''} ← ${oneLine(input?.content, 60)}`; break;
		case 'Grep': arg = `${input?.pattern ?? ''}${input?.path ? ' in ' + input.path : ''}`; break;
		default:
			arg = input?.file_path ?? input?.path ?? input?.pattern ?? input?.url ?? input?.query ?? input?.prompt
				?? Object.entries(input ?? {}).filter(([k]) => k !== 'description')
					.map(([k, v]) => `${k}: ${oneLine(v, 40)}`).join(', ');
	}
	return { text, arg: oneLine(arg ?? '', 110) };
}

/** Tool input as readable 'key: value' blocks (for the detail dialog). */
function prettyInput(input: any): string {
	return Object.entries(input ?? {}).map(([k, v]) => {
		const s = typeof v === 'string' ? v : JSON.stringify(v, null, 2) ?? String(v);
		return s.includes('\n') || s.length > 80 ? `${k}:\n${s}` : `${k}: ${s}`;
	}).join('\n\n') || '(no input)';
}
