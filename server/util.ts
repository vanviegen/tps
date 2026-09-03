import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';

export interface RunResult { code: number; out: string; err: string; }

export interface RunOpts {
	cwd?: string;
	env?: Record<string, string>;
	input?: string;
	check?: boolean; // throw on non-zero exit (default true)
}

/** Run a command, capturing stdout/stderr. */
export function run(argv: string[], opts: RunOpts = {}): Promise<RunResult> {
	const [cmd, ...args] = argv;
	const child = spawn(cmd, args, {
		cwd: opts.cwd,
		env: opts.env ? { ...process.env, ...opts.env } : process.env,
		stdio: [opts.input === undefined ? 'ignore' : 'pipe', 'pipe', 'pipe'],
	});
	if (opts.input !== undefined) child.stdin!.end(opts.input);
	let out = '', err = '';
	child.stdout!.on('data', d => out += d);
	child.stderr!.on('data', d => err += d);
	return new Promise((resolve, reject) => {
		child.on('error', reject);
		child.on('close', code => {
			if (code !== 0 && opts.check !== false) {
				reject(new Error(`\`${argv.join(' ')}\` failed (${code}): ${(err || out).trim().slice(0, 4000)}`));
			} else {
				resolve({ code: code ?? -1, out, err });
			}
		});
	});
}

export function sha256(text: string): string {
	return createHash('sha256').update(text).digest('hex');
}

export function slugify(text: string): string {
	return text.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40) || 'x';
}

/** Sortable-by-creation, url/branch-safe id. */
export function newId(): string {
	return Date.now().toString(36) + Math.random().toString(36).slice(2, 5);
}

export function log(...args: unknown[]): void {
	console.log(new Date().toISOString().slice(11, 19), ...args);
}

export class Debouncer {
	private timer?: NodeJS.Timeout;
	private ms: number;
	private fn: () => void;
	constructor(ms: number, fn: () => void) {
		this.ms = ms;
		this.fn = fn;
	}
	touch(): void {
		clearTimeout(this.timer);
		this.timer = setTimeout(this.fn, this.ms);
	}
	cancel(): void {
		clearTimeout(this.timer);
	}
}
