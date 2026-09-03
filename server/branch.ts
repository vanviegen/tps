import { existsSync, watch, type FSWatcher } from 'node:fs';
import { mkdir, readFile, rm } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { buildImage, Container, imageTag, type ExecHandle } from './container.ts';
import { Repo, TMP_COMMIT_MARKER } from './git.ts';
import { Debouncer, run } from './util.ts';

export type WorkStatus = 'down' | 'starting' | 'building' | 'up' | 'stopping' | 'error';

export interface BranchEvents {
	/** Workspace lifecycle updates. */
	status?: (status: WorkStatus, detail?: string) => void;
	/** Files under .tps/ changed on disk (only fires while a worktree exists). */
	tpsChanged?: (files: Set<string>) => void;
	/** Terminal-style output: container build logs and the like. */
	log?: (data: string) => void;
}

/**
 * A git branch that can materialize into a *workspace* (worktree + podman
 * container) on demand, and dissolve back into pure git state.
 *
 * - Files are always readable, cheaply: from the worktree while one exists,
 *   via `git show` otherwise.
 * - `down()` commits any uncommitted work as a clearly marked temporary
 *   commit, which `up()` soft-resets away on restore.
 * - While a worktree exists, changes to its .tps/ directory fire `tpsChanged`.
 *
 * The worktree is a local clone (with hardlinked objects, so nearly free)
 * rather than a `git worktree`: a worktree's .git file would point at a host
 * path that doesn't exist inside the container, and bind-mounting the real
 * .git read-write would hand the agent the whole repo. Every commit TPS makes
 * in the clone is immediately pushed back to the project repo's branch ref.
 */
export class Branch {
	status: WorkStatus = 'down';
	statusDetail = '';
	container?: Container;
	private watcher?: FSWatcher;
	private pendingTps = new Set<string>();
	private tpsDebounce = new Debouncer(300, () => {
		const files = this.pendingTps;
		this.pendingTps = new Set();
		this.events.tpsChanged?.(files);
	});
	private queue: Promise<unknown> = Promise.resolve();
	private lastTag?: string;

	readonly repo: Repo;
	readonly ref: string;
	readonly worktreeDir: string;
	readonly containerName: string;
	/** Containerfile to use when the branch itself doesn't carry one. */
	private fallbackContainerfile: () => Promise<string | undefined>;
	private events: BranchEvents;

	constructor(repo: Repo, ref: string, worktreeDir: string, containerName: string,
			fallbackContainerfile: () => Promise<string | undefined>, events: BranchEvents = {}) {
		this.repo = repo;
		this.ref = ref;
		this.worktreeDir = worktreeDir;
		this.containerName = containerName;
		this.fallbackContainerfile = fallbackContainerfile;
		this.events = events;
	}

	get hasWorktree(): boolean {
		return existsSync(join(this.worktreeDir, '.git'));
	}

	/** Read a file from the branch: live worktree state if present, else git. */
	async read(path: string): Promise<string | undefined> {
		if (this.hasWorktree) {
			try { return await readFile(join(this.worktreeDir, path), 'utf8'); } catch { return undefined; }
		}
		return this.repo.readFile(this.ref, path);
	}

	private setStatus(status: WorkStatus, detail = ''): void {
		this.status = status;
		this.statusDetail = detail;
		this.events.status?.(status, detail);
	}

	/** Serialize workspace operations. */
	private locked<T>(fn: () => Promise<T>): Promise<T> {
		const p = this.queue.then(fn);
		this.queue = p.catch(() => {});
		return p;
	}

	/** Create the local clone if it doesn't exist yet (no container involved). */
	private async ensureWorktreeRaw(): Promise<void> {
		if (this.hasWorktree) return;
		await mkdir(dirname(this.worktreeDir), { recursive: true });
		await run(['git', 'clone', '--quiet', '-b', this.ref, this.repo.dir, this.worktreeDir]);
		const lastMsg = await this.wtGit(['log', '-1', '--format=%s']);
		if (lastMsg.startsWith(TMP_COMMIT_MARKER)) await this.wtGit(['reset', '--soft', 'HEAD~1']);
	}

	/** Bring the workspace up (idempotent). */
	up(): Promise<void> {
		return this.locked(async () => {
			try {
				if (!this.hasWorktree) {
					this.setStatus('starting', 'creating worktree');
					await this.ensureWorktreeRaw();
				}
				const containerfile = await this.read('.tps/Containerfile') ?? await this.fallbackContainerfile();
				if (!containerfile) throw new Error('No .tps/Containerfile found for this branch or the default branch');
				const tag = imageTag(containerfile);
				if (this.status === 'up' && this.container && this.lastTag === tag) return;
				this.setStatus('building', 'building container image');
				await buildImage(tag, containerfile, this.worktreeDir, d => this.events.log?.(d));
				this.setStatus('starting', 'starting container');
				this.container = await Container.ensure({
					name: this.containerName, image: tag, worktree: this.worktreeDir,
				});
				this.lastTag = tag;
				this.startWatcher();
				this.setStatus('up');
			} catch (e) {
				this.setStatus('error', (e as Error).message);
				throw e;
			}
		});
	}

	/** Tear the workspace down, preserving uncommitted work in a tmp commit. */
	down(): Promise<void> {
		return this.locked(async () => {
			if (this.status === 'down' && !this.hasWorktree) return;
			this.setStatus('stopping');
			this.watcher?.close();
			this.watcher = undefined;
			this.tpsDebounce.cancel();
			await this.container?.rm();
			this.container = undefined;
			if (this.hasWorktree) {
				await this.commitAllRaw(TMP_COMMIT_MARKER);
				await rm(this.worktreeDir, { recursive: true, force: true });
			}
			this.setStatus('down');
		});
	}

	/** Commit all changes in the worktree, if any. Returns whether one was made. */
	commitAll(message: string): Promise<boolean> {
		return this.locked(() => this.commitAllRaw(message));
	}

	/** Uncommitted or untracked files outside .tps/ (agent-internal churn doesn't count). */
	async hasUncommitted(): Promise<boolean> {
		if (!this.hasWorktree) return false;
		const r = await run(['git', '-C', this.worktreeDir, 'status', '--porcelain', '--', '.', ':(exclude).tps'], { check: false });
		return r.out.trim() !== '';
	}

	/** Rebase the branch onto `onto`; abort and return false on conflicts. */
	tryRebase(onto: string): Promise<boolean> {
		return this.locked(async () => {
			await this.ensureWorktreeRaw();
			await this.commitAllRaw('tps: checkpoint');
			await this.wtGit(['fetch', '--quiet', 'origin']); // make sure `onto` is present in the clone
			try {
				await this.wtGit(['rebase', onto]);
			} catch {
				await run(['git', '-C', this.worktreeDir, 'rebase', '--abort'], { check: false });
				return false;
			}
			await this.push();
			return true;
		});
	}

	private async commitAllRaw(message: string): Promise<boolean> {
		if (!this.hasWorktree) return false;
		await this.wtGit(['add', '-A']);
		const { code } = await run(['git', '-C', this.worktreeDir, 'diff', '--cached', '--quiet'], { check: false });
		if (code !== 0) await this.wtGit(['commit', '--no-verify', '-m', message]);
		await this.push();
		return code !== 0;
	}

	/** Sync the clone's HEAD back to the project repo's branch ref. */
	private async push(): Promise<void> {
		await this.wtGit(['push', '--quiet', '--force', 'origin', `HEAD:refs/heads/${this.ref}`]);
	}

	/** Point the branch at another commit, updating the worktree if present. */
	setTip(sha: string): Promise<void> {
		return this.locked(async () => {
			await this.repo.updateRef(this.ref, sha);
			if (this.hasWorktree) {
				await this.wtGit(['fetch', '--quiet', 'origin']); // pull in objects created repo-side
				await this.wtGit(['reset', '--hard', sha]);
			}
		});
	}

	/** Whether the running container still matches the branch's Containerfile. */
	async imageCurrent(): Promise<boolean> {
		const containerfile = await this.read('.tps/Containerfile') ?? await this.fallbackContainerfile();
		return !!containerfile && imageTag(containerfile) === this.lastTag;
	}

	async tip(): Promise<string> {
		const sha = await this.repo.tip(this.ref);
		if (!sha) throw new Error(`No such ref: ${this.ref}`);
		return sha;
	}

	/** Run a script in the workspace container. */
	exec(script: string, opts: Parameters<Container['exec']>[1] = {}): ExecHandle {
		if (!this.container) throw new Error('Workspace is not up');
		return this.container.exec(script, opts);
	}

	wtGit(args: string[]): Promise<string> {
		return Repo.gitIn(this.worktreeDir, args);
	}

	private startWatcher(): void {
		if (this.watcher) return;
		const tpsDir = join(this.worktreeDir, '.tps');
		this.watcher = watch(tpsDir, { recursive: true }, (_event, filename) => {
			const f = filename?.toString();
			if (!f || f === 'claude' || f.startsWith('claude/')) return; // agent-internal churn
			this.pendingTps.add(f);
			this.tpsDebounce.touch();
		});
		this.watcher.on('error', () => {}); // dir may vanish during teardown
	}
}
