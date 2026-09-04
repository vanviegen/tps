import { spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
import { copyFile, cp, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { homedir, tmpdir } from 'node:os';
import { join } from 'node:path';
import { run, sha256 } from './util.ts';

/**
 * VS Code state (settings, keybindings, snippets, extensions, UI state) lives
 * in one shared host directory, mounted into every task container. Configure
 * once, and every task of every project has it; it also survives container
 * rebuilds. Seeded from the host's code-server or desktop VS Code settings on
 * first use, defaulting to a dark theme. Trade-off of sharing the whole data
 * dir: workspace UI state is keyed by folder path (/work everywhere), so open
 * tabs can bleed between tasks.
 */
const SHARED_VSCODE_DIR = join(homedir(), '.local', 'share', 'tps', 'code-server');
let sharedVscodeReady = false;

async function ensureSharedVscodeDir(): Promise<string> {
	if (sharedVscodeReady) return SHARED_VSCODE_DIR;
	const userDir = join(SHARED_VSCODE_DIR, 'User');
	await mkdir(join(SHARED_VSCODE_DIR, 'extensions'), { recursive: true });
	await mkdir(userDir, { recursive: true });
	const settingsFile = join(userDir, 'settings.json');
	if (!existsSync(settingsFile)) {
		for (const src of [join(homedir(), '.local', 'share', 'code-server', 'User'), join(homedir(), '.config', 'Code', 'User')]) {
			if (!existsSync(join(src, 'settings.json'))) continue;
			for (const name of ['settings.json', 'keybindings.json']) {
				if (existsSync(join(src, name))) await copyFile(join(src, name), join(userDir, name));
			}
			if (existsSync(join(src, 'snippets'))) await cp(join(src, 'snippets'), join(userDir, 'snippets'), { recursive: true });
			break;
		}
	}
	if (!existsSync(settingsFile)) {
		await writeFile(settingsFile, JSON.stringify({ 'workbench.colorTheme': 'Default Dark Modern' }, null, '\t') + '\n');
	} else {
		// Add a dark theme to seeded settings that don't pick one. Settings with
		// comments (JSONC) don't parse; leave those untouched.
		try {
			const settings = JSON.parse(await readFile(settingsFile, 'utf8'));
			if (!settings['workbench.colorTheme']) {
				settings['workbench.colorTheme'] = 'Default Dark Modern';
				await writeFile(settingsFile, JSON.stringify(settings, null, '\t') + '\n');
			}
		} catch {}
	}
	sharedVscodeReady = true;
	return SHARED_VSCODE_DIR;
}

/**
 * Podman wrapper. Images are tagged by content hash of their Containerfile, so
 * identical Containerfiles (across tasks and projects) share one image and a
 * change triggers a rebuild exactly when needed. Builds get the task's repo
 * clone as their only context, so they cannot pull in files from elsewhere.
 */

export function imageTag(containerfile: string): string {
	return 'localhost/tps:' + sha256(containerfile).slice(0, 12);
}

export async function imageExists(tag: string): Promise<boolean> {
	return (await run(['podman', 'image', 'exists', tag], { check: false })).code === 0;
}

export async function buildImage(tag: string, containerfile: string, contextDir: string, onLog: (data: string) => void): Promise<void> {
	// Written to a temp file: podman can't read a Containerfile from a node
	// stdio pipe, and writing it into the context dir would dirty the worktree.
	const dir = await mkdtemp(join(tmpdir(), 'tps-build-'));
	const file = join(dir, 'Containerfile');
	await writeFile(file, containerfile);
	try {
		await new Promise<void>((resolve, reject) => {
			const child = spawn('podman', ['build', '-t', tag, '-f', file, contextDir], { stdio: ['ignore', 'pipe', 'pipe'] });
			const fwd = (d: Buffer) => onLog(d.toString());
			child.stdout.on('data', fwd);
			child.stderr.on('data', fwd);
			child.on('error', reject);
			child.on('close', code => code === 0 ? resolve() : reject(new Error(`podman build failed (${code})`)));
		});
	} finally {
		await rm(dir, { recursive: true, force: true });
	}
}

export interface ExecHandle {
	done: Promise<number>;
}

const CODE_PORT = 9000; // code-server inside the container
const APP_PORT = 8080; // $PORT, for whatever the task itself serves

export class Container {
	readonly name: string;
	readonly codePort: number;
	readonly appPort: number;
	private constructor(name: string, codePort: number, appPort: number) {
		this.name = name;
		this.codePort = codePort;
		this.appPort = appPort;
	}

	/**
	 * Make sure a container by this name, based on this image, is running with
	 * the task's repo clone mounted at /work and its claude state dir at
	 * /claude. Reuses a running match; otherwise replaces.
	 */
	static async ensure(opts: { name: string; image: string; repoDir: string; claudeDir: string }): Promise<Container> {
		const { name, image } = opts;
		const inspect = await run(
			['podman', 'inspect', '--format', '{{index .Config.Labels "tps.config"}}\t{{.State.Running}}', name],
			{ check: false });
		// Bump the version when the run command/args below change, so existing
		// containers are recycled instead of reused.
		const config = JSON.stringify([6, image]);
		if (inspect.code === 0) {
			const [label, running] = inspect.out.trim().split('\t');
			if (label === config && running === 'true') {
				const ports = await Container.getPorts(name);
				if (ports) return new Container(name, ports.code, ports.app);
			}
			await run(['podman', 'rm', '-f', '-t', '2', name]);
		}
		const creds = join(homedir(), '.claude', '.credentials.json');
		const args = [
			'run', '-d', '--init', '--name', name, '--label', 'tps.config=' + config,
			'--userns=keep-id:uid=1000,gid=1000', '--user', '1000:1000',
			// SELinux separation is off so the mounts stay usable without
			// relabeling the user's real files.
			'--security-opt', 'label=disable',
			'-v', `${opts.repoDir}:/work`,
			'-v', `${opts.claudeDir}:/claude`,
			'-e', 'CLAUDE_CONFIG_DIR=/claude',
			'-e', `PORT=${APP_PORT}`,
			'-p', `127.0.0.1::${CODE_PORT}`,
			'-p', `127.0.0.1::${APP_PORT}`,
			'-w', '/work',
			'-v', `${await ensureSharedVscodeDir()}:/home/dev/.local/share/code-server`,
		];
		if (existsSync(creds)) args.push('-v', `${creds}:/tps-host-claude-credentials.json:ro`);
		if (process.env.ANTHROPIC_API_KEY) args.push('-e', 'ANTHROPIC_API_KEY');
		// The explicit --port keeps code-server from picking up $PORT, which is
		// meant for whatever the task itself serves.
		args.push(image, 'code-server', '--bind-addr', `0.0.0.0:${CODE_PORT}`, '--port', `${CODE_PORT}`,
			'--auth', 'none', '--disable-workspace-trust', '/work');
		await run(['podman', ...args]);
		const ports = await Container.getPorts(name);
		if (!ports) throw new Error(`Container ${name} has no published ports`);
		const container = new Container(name, ports.code, ports.app);
		await container.waitReady();
		return container;
	}

	private static async getPorts(name: string): Promise<{ code: number; app: number } | undefined> {
		const get = async (cport: number) => {
			const out = await run(['podman', 'port', name, `${cport}/tcp`], { check: false });
			const match = out.out.match(/:(\d+)\s*$/m);
			return match ? parseInt(match[1]) : undefined;
		};
		const code = await get(CODE_PORT), app = await get(APP_PORT);
		return code && app ? { code, app } : undefined;
	}

	private async waitReady(): Promise<void> {
		for (let i = 0; i < 120; i++) {
			try {
				await fetch(`http://127.0.0.1:${this.codePort}/healthz`, { signal: AbortSignal.timeout(1000) });
				return;
			} catch {
				await new Promise(r => setTimeout(r, 500));
			}
		}
		throw new Error(`code-server in ${this.name} did not come up`);
	}

	/** Run a bash script in the container. */
	exec(script: string): ExecHandle {
		const child = spawn('podman', ['exec', this.name, 'bash', '-lc', script], { stdio: 'ignore' });
		const done = new Promise<number>((resolve, reject) => {
			child.on('error', reject);
			child.on('close', code => resolve(code ?? -1));
		});
		return { done };
	}

	async rm(): Promise<void> {
		await run(['podman', 'rm', '-f', '-t', '2', this.name], { check: false });
	}
}
