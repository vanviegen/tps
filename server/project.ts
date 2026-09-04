import { existsSync, realpathSync } from 'node:fs';
import { appendFile, mkdir, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import { basename, dirname, join, resolve } from 'node:path';
import { ChatSession, entry, type ChatEntry } from './agent.ts';
import { buildImage, Container, imageExists, imageTag } from './container.ts';
import { detectTools, generateContainerfile } from './containerfile.ts';
import type { Hub } from './hub.ts';
import { conflictPrompt, DONE_FILE, initialPrompt, RELOADED_PROMPT, SYSTEM } from './prompts.ts';
import { git, log, run, slugify } from './util.ts';

export type Phase = 'plan' | 'agent' | 'human' | 'merge';
export const PHASES: Phase[] = ['plan', 'agent', 'human', 'merge'];
export type WorkStatus = 'down' | 'building' | 'starting' | 'up' | 'stopping' | 'error';

const IDLE_SHUTDOWN_MS = 15 * 60_000;
const CONTAINERFILE = 'Containerfile.dev';

/** Task metadata, persisted in ~/.config/tps/projects.json. */
interface TaskInfo {
	title: string;
	description: string;
	model: string;
	phase: Phase;
	started?: boolean; // a claude session exists in the task's claude dir
	commitMessage?: string; // proposed by the agent, awaiting the user's merge
	spent?: number; // USD spent on agent runs so far
	budget?: number; // USD limit; the task is parked when spending reaches it
	autoMerge?: boolean; // overrides the project setting when set
	dependencies?: string[]; // tids that must be merged or deleted first
	waiting?: boolean; // assigned to the agent, parked until the dependencies resolve
}

interface ProjectInfo {
	dir: string;
	name: string;
	autoMerge?: boolean; // merge without confirmation when the agent reports ready
	nextTask?: number;
	tasks?: Record<string, TaskInfo>;
}

/**
 * One task. Its workspace is a plain clone of the project repo (git hardlinks
 * the object store, so cloning is nearly free and safe: object files are never
 * modified in place), created when the task leaves the plan phase and kept
 * until the task is deleted or discarded back to plan. A sibling dir holds the
 * claude conversation state, another file the condensed chat log.
 */
export class Task {
	info: TaskInfo;
	status: WorkStatus = 'down';
	statusDetail = '';
	container?: Container;
	viewers = 0;
	lastActivity = Date.now();

	private session?: ChatSession;
	private sessionPromise?: Promise<ChatSession>;
	private sessionBudget?: number; // the budget setting the running claude was started under
	private upPromise?: Promise<Container>;
	private lastTag?: string;
	private stopping = false;
	private live = false;
	private livePoll?: NodeJS.Timeout;

	readonly project: Project;
	readonly tid: string;

	constructor(project: Project, tid: string, info: TaskInfo) {
		this.project = project;
		this.tid = tid;
		this.info = info;
	}

	get key(): string { return `${this.project.pid}/${this.tid}`; }
	get phase(): Phase { return this.info.phase; }
	get dir(): string { return join(this.project.tasksDir, this.tid); }
	get repoDir(): string { return join(this.dir, 'repo'); }
	get claudeDir(): string { return join(this.dir, 'claude'); }
	get chatFile(): string { return join(this.dir, 'chat.jsonl'); }
	get containerName(): string { return `tps-${this.project.pid}-${this.tid}`; }
	get working(): boolean { return !!this.session?.turnActive; }
	touch(): void { this.lastActivity = Date.now(); }

	private pub(field: string, value: unknown): void {
		this.project.hub.set(['projects', this.project.pid, 'tasks', this.tid, field], value);
	}

	publish(): void {
		this.pub('title', this.info.title);
		this.pub('description', this.info.description);
		this.pub('model', this.info.model);
		this.pub('phase', this.info.phase);
		this.pub('commitMessage', this.info.commitMessage);
		this.pub('spent', this.info.spent);
		this.pub('budget', this.info.budget);
		this.pub('autoMerge', this.info.autoMerge);
		this.pub('dependencies', this.info.dependencies);
		this.pub('waiting', this.info.waiting);
		this.pub('status', this.status);
		this.pub('statusDetail', this.statusDetail);
		this.pub('working', this.working);
		this.pub('live', this.live);
		this.pub('appPort', this.status === 'up' ? this.container?.appPort : undefined);
	}

	private setStatus(status: WorkStatus, detail = ''): void {
		this.status = status;
		this.statusDetail = detail;
		this.publish();
		this.updateLivePoll();
	}

	private setPhase(phase: Phase): void {
		this.info.phase = phase;
		if (phase !== 'agent') this.info.waiting = undefined;
		void this.project.save();
		this.publish();
	}

	// --- chat log ---

	addEntry(e: ChatEntry): void {
		this.project.hub.chat(this.key, e);
		this.appendChat(e);
	}

	/** A changed entry (a tool call that got its result); replaces by id. */
	updateEntry(e: ChatEntry): void {
		this.project.hub.chatUpdate(this.key, e);
		this.appendChat(e); // appended again; loadChat keeps the last version per id
	}

	private appendChat(e: ChatEntry): void {
		if (existsSync(this.dir)) void appendFile(this.chatFile, JSON.stringify(e) + '\n').catch(() => {});
	}

	note(text: string, detail?: string): void {
		this.addEntry(entry({ k: 'note', text, detail }));
	}

	async loadChat(): Promise<void> {
		try {
			const lines = (await readFile(this.chatFile, 'utf8')).split('\n').filter(Boolean);
			const entries: ChatEntry[] = [];
			const byId = new Map<string, number>();
			for (const line of lines) {
				try {
					const e = JSON.parse(line);
					const i = e.id !== undefined ? byId.get(e.id) : undefined;
					if (i !== undefined) {
						entries[i] = e;
					} else {
						if (e.id !== undefined) byId.set(e.id, entries.length);
						entries.push(e);
					}
				} catch {}
			}
			this.project.hub.setChat(this.key, entries.slice(-500));
		} catch {}
	}

	// --- settings ---

	async update(partial: Record<string, unknown>): Promise<void> {
		if (this.info.phase === 'plan') { // fixed once the conversation has started
			if (typeof partial.title === 'string' && partial.title.trim()) this.info.title = partial.title.trim();
			if (typeof partial.description === 'string') this.info.description = partial.description;
		}
		if (typeof partial.model === 'string') this.info.model = partial.model;
		if (typeof partial.autoMerge === 'boolean') this.info.autoMerge = partial.autoMerge;
		if (partial.budget !== undefined) {
			const budget = Number(partial.budget);
			this.info.budget = Number.isFinite(budget) && budget > 0 ? budget : undefined;
		}
		if (Array.isArray(partial.dependencies)) {
			const deps = [...new Set(partial.dependencies)].filter(d =>
				typeof d === 'string' && d !== this.tid && this.project.tasks.has(d)
				&& !this.project.dependsOn(d, this.tid)); // dropping cycles keeps waiting tasks startable
			this.info.dependencies = deps.length ? deps as string[] : undefined;
		}
		await this.project.save();
		this.publish();
		if (this.info.waiting) this.project.startUnblocked();
	}

	/** Dependencies that still stand in the way: existing, unmerged tasks. */
	blockedOn(): string[] {
		return (this.info.dependencies ?? []).filter(tid => {
			const dep = this.project.tasks.get(tid);
			return dep && dep.phase !== 'merge';
		});
	}

	/** Whether the budget (if any) leaves no meaningful room for another run. */
	private overBudget(): boolean {
		return this.info.budget !== undefined && this.info.budget - (this.info.spent ?? 0) < 0.01;
	}

	private noteBudget(): void {
		this.note(`the task budget ($${this.info.budget}) is used up`
			+ ` (spent $${(this.info.spent ?? 0).toFixed(2)}); raise it in the task settings to continue`);
	}

	// --- phase transitions ---

	/**
	 * Leave the plan phase: create the workspace and hand the task over. With
	 * unresolved dependencies the task just parks in the agent column; once they
	 * are all merged or deleted, startUnblocked() calls this again. The clone is
	 * made only then, so it includes the dependencies' merged work.
	 */
	async assign(to: 'agent' | 'human'): Promise<void> {
		if (!this.info.title.trim()) throw new Error('Give the task a title first');
		this.touch();
		if (to === 'agent' && this.blockedOn().length) {
			this.info.waiting = true;
			this.setPhase('agent');
			return;
		}
		this.info.waiting = undefined;
		await this.ensureClone();
		if (to === 'agent') {
			const prompt = initialPrompt(this.info.title, this.info.description);
			this.addEntry(entry({ k: 'user', text: prompt }));
			this.kick(prompt);
		} else {
			this.setPhase('human');
			this.bgUp();
		}
	}

	/** A user chat message: shown in the log, then fed to (or starting) claude. */
	async sendChat(text: string): Promise<void> {
		text = text.trim();
		if (!text) return;
		if (this.phase === 'plan') throw new Error('Assign the task to the agent first');
		if (this.info.waiting) throw new Error('This task is waiting for its dependencies; remove them in the settings to start it now');
		this.touch();
		this.addEntry(entry({ k: 'user', text }));
		this.kick(text);
	}

	/** Make sure a claude session is running and feed it `text`, in the background.
	 * With `fresh`, any current session is stopped and a new context is started. */
	private kick(text: string, fresh = false): void {
		if (this.overBudget()) {
			this.noteBudget();
			if (this.phase === 'agent') this.setPhase('human');
			return;
		}
		this.setPhase('agent');
		void (async () => {
			// A running claude has its spending cap fixed at start; a changed budget needs a new process.
			if (this.session && (fresh || this.sessionBudget !== this.info.budget)) {
				this.stopping = true;
				try { await this.session.stop(); } finally { this.stopping = false; }
			}
			const session = await this.ensureSession(fresh);
			session.send(text);
			this.publish();
		})().catch(e => {
			this.note(`agent start failed: ${(e as Error).message}`);
			if (this.phase === 'agent') this.setPhase('human');
		});
	}

	async stopAgent(): Promise<void> {
		const session = this.session;
		if (session) {
			this.stopping = true;
			try { await session.stop(); } finally { this.stopping = false; }
		}
		if (this.phase === 'agent') this.setPhase('human');
	}

	/** Board drags map onto the real actions. */
	async moveTo(phase: Phase): Promise<void> {
		if (!PHASES.includes(phase) || phase === this.phase) return;
		switch (phase) {
			case 'plan': return this.discard();
			case 'agent':
				if (this.phase === 'plan') return this.assign('agent');
				return this.sendChat('Please continue working on the task.');
			case 'human':
				if (this.phase === 'plan' || this.info.waiting) return this.assign('human'); // a waiting task has no clone yet
				return this.stopAgent();
			case 'merge': return this.merge(this.info.commitMessage ?? '');
		}
	}

	/**
	 * Merge: commit the working tree as one commit, rebase onto the latest
	 * default branch if it moved, and fast-forward the project repo.
	 */
	async merge(message: string): Promise<void> {
		if (!existsSync(this.repoDir)) throw new Error('The task has no work to merge yet');
		if (this.midRebase()) throw new Error('A rebase is still in progress in the workspace; let the agent finish it (or resolve it in VS Code) first');
		message = (message || this.info.commitMessage || this.info.title).trim();
		await this.stopAgent();
		const { defaultBranch } = this.project;
		await git(this.repoDir, ['add', '-A']);
		if ((await run(['git', '-C', this.repoDir, 'diff', '--cached', '--quiet'], { check: false })).code !== 0) {
			await git(this.repoDir, ['commit', '--no-verify', '-m', message]);
		}
		await git(this.repoDir, ['fetch', '--quiet', 'origin']);
		const target = await git(this.repoDir, ['rev-parse', `origin/${defaultBranch}`]);
		if ((await run(['git', '-C', this.repoDir, 'merge-base', '--is-ancestor', target, 'HEAD'], { check: false })).code !== 0) {
			try {
				await git(this.repoDir, ['rebase', target]);
			} catch {
				if (this.midRebase()) {
					// Hand the conflicts to a fresh agent; when it reports ready, the merge runs again.
					this.info.commitMessage = message;
					void this.project.save();
					this.note(`rebasing onto the latest ${defaultBranch} hit conflicts; sending in a fresh agent to resolve them`);
					this.kick(conflictPrompt(defaultBranch, message), true);
					return;
				}
				throw new Error(`rebase onto ${defaultBranch} failed`);
			}
		}
		if (await git(this.repoDir, ['rev-parse', 'HEAD']) !== target) {
			await this.project.fastForward(this.repoDir);
			this.note(`merged into ${defaultBranch} ✔`);
		} else {
			this.note('nothing to merge; task closed');
		}
		this.info.commitMessage = undefined;
		this.setPhase('merge');
		this.project.startUnblocked();
		void this.down().catch(() => {});
	}

	private midRebase(): boolean {
		return existsSync(join(this.repoDir, '.git', 'rebase-merge')) || existsSync(join(this.repoDir, '.git', 'rebase-apply'));
	}

	/** Back to plan: all work is thrown away (the UI asks for confirmation). */
	async discard(): Promise<void> {
		await this.stopAgent();
		await this.down();
		await rm(this.dir, { recursive: true, force: true });
		this.info.started = false;
		this.info.commitMessage = undefined;
		this.project.hub.setChat(this.key, []);
		this.setPhase('plan');
	}

	async delete(): Promise<void> {
		await this.stopAgent();
		await this.down();
		await rm(this.dir, { recursive: true, force: true });
		delete this.project.info.tasks![this.tid];
		this.project.tasks.delete(this.tid);
		this.project.hub.set(['projects', this.project.pid, 'tasks', this.tid], undefined);
		this.project.hub.setChat(this.key, []);
		await this.project.save();
		this.project.startUnblocked();
	}

	// --- workspace (clone + container) ---

	private async ensureClone(): Promise<void> {
		if (existsSync(this.repoDir)) return;
		await mkdir(this.claudeDir, { recursive: true });
		// A local git clone hardlinks the object store (objects are immutable,
		// so sharing them is safe): nearly free even for big repos.
		await run(['git', 'clone', '--quiet', '-b', this.project.defaultBranch, this.project.dir, this.repoDir]);
		await git(this.repoDir, ['config', 'user.name', 'TPS']);
		await git(this.repoDir, ['config', 'user.email', 'tps@localhost']);
	}

	/** Called when a user opens the task's page: bring the workspace up. */
	open(): void {
		this.touch();
		if (this.phase !== 'plan' && existsSync(this.repoDir)) this.bgUp();
	}

	private bgUp(): void {
		this.up().catch(e => this.note(`workspace failed: ${(e as Error).message}`));
	}

	up(): Promise<Container> {
		return this.upPromise ??= this.doUp().finally(() => { this.upPromise = undefined; });
	}

	private async doUp(): Promise<Container> {
		if (!existsSync(this.repoDir)) throw new Error('The task has no workspace (still in plan?)');
		const containerfile = await readFile(join(this.repoDir, CONTAINERFILE), 'utf8').catch(() => {
			throw new Error(`The repository has no committed ${CONTAINERFILE}; create one from the project page`);
		});
		const tag = imageTag(containerfile);
		// Don't recycle a container out from under a live claude session.
		if (this.container && this.status === 'up' && (this.lastTag === tag || this.session)) return this.container;
		try {
			this.setStatus('building', 'building container image');
			if (!(await imageExists(tag))) this.note('building the dev container image; the first build takes a few minutes…');
			let buildLog = '';
			await buildImage(tag, containerfile, this.repoDir, d => { buildLog += d; });
			this.setStatus('starting', 'starting container');
			this.container = await Container.ensure({
				name: this.containerName, image: tag, repoDir: this.repoDir, claudeDir: this.claudeDir,
			});
			this.lastTag = tag;
			this.setStatus('up');
			return this.container;
		} catch (e) {
			this.setStatus('error', (e as Error).message); // callers report the error
			throw e;
		}
	}

	async down(): Promise<void> {
		if (this.status === 'down' && !this.container) return;
		this.setStatus('stopping');
		this.session?.kill();
		await this.container?.rm();
		this.container = undefined;
		this.setStatus('down');
	}

	// --- the claude session ---

	private ensureSession(fresh = false): Promise<ChatSession> {
		if (this.session) return Promise.resolve(this.session);
		return this.sessionPromise ??= (async () => {
			const container = await this.up();
			this.sessionBudget = this.info.budget;
			this.session = new ChatSession({
				container, model: this.info.model, system: SYSTEM, resume: !!this.info.started && !fresh,
				budget: this.info.budget !== undefined
					? Math.max(0.01, this.info.budget - (this.info.spent ?? 0)) : undefined,
				onEntry: e => this.addEntry(e),
				onUpdate: e => this.updateEntry(e),
				onTurnEnd: costDelta => void this.onTurnEnd(costDelta).catch(e => log(`turn end ${this.key}:`, e)),
				onExit: (code, errTail) => this.onSessionExit(code, errTail),
			});
			if (!this.info.started) {
				this.info.started = true;
				void this.project.save();
			}
			return this.session;
		})().finally(() => { this.sessionPromise = undefined; });
	}

	/** A claude turn finished: account the cost, consume the done file, move the task along. */
	private async onTurnEnd(costDelta: number): Promise<void> {
		this.touch();
		if (costDelta > 0) {
			this.info.spent = Math.round(((this.info.spent ?? 0) + costDelta) * 10000) / 10000;
			void this.project.save();
		}
		this.publish();
		if (this.session?.turnActive) return; // the user already sent a follow-up
		const doneFile = join(this.repoDir, DONE_FILE);
		const content = await readFile(doneFile, 'utf8').catch(() => undefined);
		await rm(doneFile, { force: true });
		if (this.phase !== 'agent') return; // stopped or dragged elsewhere meanwhile
		const nl = content?.indexOf('\n') ?? -1;
		const word = (nl >= 0 ? content!.slice(0, nl) : content ?? '').trim();
		if (word === 'reload' && !this.overBudget()) {
			this.note('the agent asked for a container rebuild; recreating the workspace');
			const session = this.session;
			if (session) {
				this.stopping = true;
				try { await session.stop(); } finally { this.stopping = false; }
			}
			await this.down();
			this.kick(RELOADED_PROMPT);
			return;
		}
		if (word === 'ready') {
			this.info.commitMessage = (nl >= 0 ? content!.slice(nl + 1) : '').trim() || this.info.title;
			await this.project.save();
			if (this.info.autoMerge ?? this.project.info.autoMerge) {
				this.note('the agent reports the task is ready; merging');
				await this.merge(this.info.commitMessage).catch(e => {
					this.note(`automatic merge failed: ${(e as Error).message}`);
					this.setPhase('human');
				});
				return;
			}
			this.note('the agent reports the task is ready to merge');
		}
		if (this.overBudget()) this.noteBudget();
		this.setPhase('human');
	}

	private onSessionExit(code: number, errTail: string): void {
		this.session = undefined;
		this.publish();
		if (this.phase === 'agent' && !this.stopping) {
			if (code !== 0) this.note(`claude exited unexpectedly (${code})`, errTail || undefined);
			this.setPhase('human');
		}
	}

	// --- the 'live' indicator: is something answering on the task's $PORT? ---

	private updateLivePoll(): void {
		const port = this.status === 'up' ? this.container?.appPort : undefined;
		if (port && !this.livePoll) {
			this.livePoll = setInterval(() => void this.checkLive(), 2_000);
			this.livePoll.unref();
		} else if (!port && this.livePoll) {
			clearInterval(this.livePoll);
			this.livePoll = undefined;
			this.setLive(false);
		}
	}

	private async checkLive(): Promise<void> {
		const port = this.container?.appPort;
		if (!port) return this.setLive(false);
		try {
			await fetch(`http://127.0.0.1:${port}/`, { signal: AbortSignal.timeout(1500) });
			this.setLive(true); // any HTTP response counts
		} catch {
			this.setLive(false);
		}
	}

	private setLive(live: boolean): void {
		if (live === this.live) return;
		this.live = live;
		this.pub('live', live);
	}
}

/** A registered project: a git repo plus its tasks. */
export class Project {
	tasks = new Map<string, Task>();
	defaultBranch = 'main';

	readonly manager: ProjectManager;
	readonly pid: string;
	readonly info: ProjectInfo;

	constructor(manager: ProjectManager, pid: string, info: ProjectInfo) {
		this.manager = manager;
		this.pid = pid;
		this.info = info;
		info.tasks ??= {};
		info.nextTask ??= Math.max(0, ...Object.keys(info.tasks).map(Number)) + 1;
	}

	get hub(): Hub { return this.manager.hub; }
	get dir(): string { return this.info.dir; }
	get tasksDir(): string { return join(this.manager.dataDir, 'tasks', this.pid); }
	save(): Promise<void> { return this.manager.save(); }
	private pub(field: string, value: unknown): void { this.hub.set(['projects', this.pid, field], value); }

	async init(): Promise<void> {
		this.defaultBranch = await git(this.dir, ['symbolic-ref', '--short', 'HEAD']).catch(() => '')
			|| ((await run(['git', '-C', this.dir, 'rev-parse', '--verify', '-q', 'main'], { check: false })).code === 0 ? 'main' : 'master');
		this.hub.set(['projects', this.pid], { name: this.info.name, dir: this.dir, autoMerge: !!this.info.autoMerge, tasks: {} });
		for (const [tid, info] of Object.entries(this.info.tasks!)) {
			const task = new Task(this, tid, info);
			this.tasks.set(tid, task);
			await task.loadChat();
			if (info.phase === 'agent' && !info.waiting) { // the server restarted mid-turn
				task.note('TPS restarted while the agent was working; send a message to continue');
				info.phase = 'human';
			}
			task.publish();
		}
		await this.refreshMeta();
		this.startUnblocked();
	}

	/** True when task `tid` (transitively) depends on task `on`. */
	dependsOn(tid: string, on: string, seen = new Set<string>()): boolean {
		if (tid === on) return true;
		if (seen.has(tid)) return false;
		seen.add(tid);
		return (this.tasks.get(tid)?.info.dependencies ?? []).some(d => this.dependsOn(d, on, seen));
	}

	/** Start any task that was waiting on dependencies now merged or gone. */
	startUnblocked(): void {
		for (const task of this.tasks.values()) {
			if (task.info.waiting && !task.blockedOn().length) {
				task.assign('agent').catch(e => log(`autostart ${task.key}:`, e));
			}
		}
	}

	async close(): Promise<void> {
		for (const task of this.tasks.values()) await task.down().catch(() => {});
	}

	async refreshMeta(): Promise<void> {
		const needsSetup = !existsSync(join(this.dir, CONTAINERFILE));
		this.pub('defaultBranch', this.defaultBranch);
		this.pub('needsSetup', needsSetup);
		this.pub('dirty', (await git(this.dir, ['status', '--porcelain']).catch(() => '')) !== '');
		if (needsSetup) {
			this.pub('detected', detectTools(await readdir(this.dir).catch(() => [])));
		}
	}

	async setConfig(partial: Record<string, unknown>): Promise<void> {
		if (partial.autoMerge !== undefined) this.info.autoMerge = !!partial.autoMerge;
		await this.save();
		this.pub('autoMerge', !!this.info.autoMerge);
	}

	/** Generate Containerfile.dev in the repo root and commit it. */
	async createContainerfile(toolIds: string[]): Promise<void> {
		if (await git(this.dir, ['symbolic-ref', '--short', 'HEAD']).catch(() => '') !== this.defaultBranch) {
			throw new Error(`Check out the ${this.defaultBranch} branch first (or commit a ${CONTAINERFILE} yourself)`);
		}
		await writeFile(join(this.dir, CONTAINERFILE), generateContainerfile(toolIds));
		await git(this.dir, ['add', CONTAINERFILE]);
		await git(this.dir, ['commit', '--no-verify', '-m', `Add ${CONTAINERFILE} (TPS dev container)`, '--', CONTAINERFILE]);
		await this.refreshMeta();
	}

	/** Tasks start empty; the plan page is where they get their title. */
	async createTask(): Promise<string> {
		const tid = String(this.info.nextTask!++);
		const info: TaskInfo = { title: '', description: '', model: 'sonnet', phase: 'plan' };
		this.info.tasks![tid] = info;
		const task = new Task(this, tid, info);
		this.tasks.set(tid, task);
		await this.save();
		task.publish();
		return tid;
	}

	/** Fast-forward the project repo's default branch to the task clone's HEAD. */
	async fastForward(fromDir: string): Promise<void> {
		await git(this.dir, ['fetch', '--quiet', fromDir, this.defaultBranch]);
		const sha = await git(this.dir, ['rev-parse', 'FETCH_HEAD']);
		if (await git(this.dir, ['symbolic-ref', '--short', 'HEAD']).catch(() => '') === this.defaultBranch) {
			// The user's checkout: a real merge keeps their working tree in sync.
			await git(this.dir, ['merge', '--ff-only', sha]);
		} else {
			await git(this.dir, ['update-ref', `refs/heads/${this.defaultBranch}`, sha]);
		}
		await this.refreshMeta();
	}
}

/** The registry of projects and tasks, persisted to ~/.config/tps/projects.json. */
export class ProjectManager {
	projects = new Map<string, Project>();
	readonly dataDir = join(homedir(), '.local', 'share', 'tps');
	readonly configFile = join(homedir(), '.config', 'tps', 'projects.json');

	readonly hub: Hub;
	private saveQueue: Promise<void> = Promise.resolve();

	constructor(hub: Hub) {
		this.hub = hub;
		hub.onWatch = (key, count) => {
			const [pid, tid] = key.split('/');
			const task = this.projects.get(pid)?.tasks.get(tid);
			if (task) {
				task.viewers = count;
				task.touch();
			}
		};
		setInterval(() => void this.idleSweep(), 60_000).unref();
	}

	async start(): Promise<void> {
		let saved: ProjectInfo[] = [];
		try { saved = JSON.parse(await readFile(this.configFile, 'utf8')).projects; } catch {}
		for (const info of saved) {
			try {
				await this.load(info);
			} catch (e) {
				log(`Failed to load project ${info.dir}:`, (e as Error).message);
			}
		}
		this.hub.set(['ready'], true);
	}

	save(): Promise<void> {
		return this.saveQueue = this.saveQueue.then(async () => {
			await mkdir(dirname(this.configFile), { recursive: true });
			const projects = [...this.projects.values()].map(p => p.info);
			await writeFile(this.configFile, JSON.stringify({ projects }, null, '\t') + '\n');
		});
	}

	private async load(info: ProjectInfo): Promise<Project> {
		let pid = slugify(info.name);
		while (this.projects.has(pid)) pid += 'x';
		const project = new Project(this, pid, info);
		this.projects.set(pid, project);
		try {
			await project.init();
		} catch (e) {
			this.projects.delete(pid);
			this.hub.set(['projects', pid], undefined);
			throw e;
		}
		return project;
	}

	async add(dir: string, name?: string): Promise<{ pid: string }> {
		dir = realpathSync(resolve(dir.replace(/^~(?=\/|$)/, homedir())));
		if (!existsSync(dir)) throw new Error(`No such directory: ${dir}`);
		await git(dir, ['rev-parse', '--git-dir']).catch(() => { throw new Error(`${dir} is not a git repository`); });
		if ((await run(['git', '-C', dir, 'rev-parse', '--verify', '-q', 'HEAD'], { check: false })).code !== 0) {
			throw new Error(`${dir} has no commits yet`);
		}
		for (const p of this.projects.values()) {
			if (p.dir === dir) throw new Error(`${dir} is already registered as '${p.info.name}'`);
		}
		const project = await this.load({ dir, name: name?.trim() || basename(dir) });
		await this.save();
		return { pid: project.pid };
	}

	async remove(pid: string): Promise<void> {
		const project = this.getProject(pid);
		await project.close();
		this.projects.delete(pid);
		this.hub.set(['projects', pid], undefined);
		await this.save();
	}

	getProject(pid: string): Project {
		const project = this.projects.get(pid);
		if (!project) throw new Error(`Unknown project: ${pid}`);
		return project;
	}

	getTask(pid: string, tid: string): Task {
		const task = this.getProject(pid).tasks.get(tid);
		if (!task) throw new Error(`Unknown task: ${pid}/${tid}`);
		return task;
	}

	private async idleSweep(): Promise<void> {
		for (const project of this.projects.values()) {
			await project.refreshMeta().catch(() => {}); // keep the 'dirty' flag current
			for (const task of project.tasks.values()) {
				if (task.status === 'up' && !task.working && task.viewers === 0
						&& Date.now() - task.lastActivity > IDLE_SHUTDOWN_MS) {
					task.note('workspace idle, shutting down (your work is untouched)');
					await task.down().catch(e => log(`idle shutdown ${task.key}:`, e));
				}
			}
		}
	}

	async shutdown(): Promise<void> {
		await Promise.all([...this.projects.values()].map(p => p.close()));
	}
}
