import { existsSync, realpathSync, watch, type FSWatcher } from 'node:fs';
import { mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import { basename, dirname, join, resolve } from 'node:path';
import { killClaude, runClaude } from './agent.ts';
import { Branch } from './branch.ts';
import { detectTools, generateContainerfile } from './containerfile.ts';
import { Repo } from './git.ts';
import type { Hub } from './hub.ts';
import {
	continuePrompt, implementerPrompt, plannerPrompt, rebasePrompt, reviewerPrompt,
	urgeCommitPrompt, urgeReviewEmptyPrompt, workflowSystem,
} from './prompts.ts';
import { Debouncer, log, newId, run, slugify } from './util.ts';

export type Phase = 'plan' | 'implement' | 'review' | 'done';
export const PHASES: Phase[] = ['plan', 'implement', 'review', 'done'];
type Role = 'planner' | 'implementer' | 'reviewer' | 'merge';
type StageRole = 'implementer' | 'reviewer';

const TPS_GITIGNORE = `# TPS: keep claude runtime state out of git, except session history
/claude/*
!/claude/projects/
`;

const REVIEW_TEMPLATE = `<!-- One "## title" section per review item; the implementer removes each as it is addressed. An empty file means approved. -->
`;

const IDLE_SHUTDOWN_MS = 15 * 60_000;
const CONTAINERFILE_SETTLE_MS = 5_000;
const MAX_URGES = 2; // extra "commit your work" / "empty REVIEW.md" continues per run

/** All task metadata, stored as .tps/state.json on the task branch. */
interface TaskState {
	phase: Phase;
	implementModel: string; // 'none': a human implements
	reviewModel: string; // 'none': no agent review
	skipHumanReview: boolean; // merge to done automatically when implementation is finished
	maxReviewCycles: number; // implementer re-runs allowed to address agent review feedback
	dependencies: string[]; // branch names of tasks that must be done first
	startWhenDepsDone: boolean; // auto-move to implement once all dependencies are done
	reviewBase?: string; // commit the agent reviewer diffs against; set when implement begins
	cycles: number; // implementer re-runs started this implement phase
}

const DEFAULT_STATE: TaskState = {
	phase: 'plan', implementModel: 'sonnet', reviewModel: 'none',
	skipHumanReview: false, maxReviewCycles: 1,
	dependencies: [], startWhenDepsDone: false, cycles: 0,
};

/** One task: a `tps/<tid>` branch plus its on-demand workspace and agent runs. */
export class Task {
	branch: Branch;
	title = '';
	state: TaskState = { ...DEFAULT_STATE };
	viewers = 0;
	lastActivity = Date.now();

	private role?: Role;
	private agentPromise?: Promise<void>;
	private stopRequested = false;
	private moving = false;
	private live = false;
	private livePoll?: NodeJS.Timeout;
	private cfSettle = new Debouncer(CONTAINERFILE_SETTLE_MS, () =>
		void this.onContainerfileSettled().catch(e => this.termNote(`container rebuild failed: ${e.message}`)));

	readonly project: Project;
	readonly tid: string;

	constructor(project: Project, tid: string) {
		this.project = project;
		this.tid = tid;
		this.branch = new Branch(
			project.repo, `tps/${tid}`,
			join(project.worktreesDir, tid), `tps-${project.pid}-${tid}`,
			() => project.defaultContainerfile(),
			{
				status: (status, detail) => {
					this.pub('status', status);
					this.pub('statusDetail', detail);
					this.updateLivePoll();
				},
				tpsChanged: files => void this.onTps(files).catch(e => log(`tps event ${this.key}:`, e)),
				log: data => this.term(data),
			});
	}

	get key(): string { return `${this.project.pid}/${this.tid}`; }
	get phase(): Phase { return this.state.phase; }
	get agentBusy(): boolean { return !!this.role; }
	touch(): void { this.lastActivity = Date.now(); }
	term(data: string): void { this.project.hub.term(this.key, data); }
	termNote(text: string): void { this.term(`\r\n\x1b[35m[tps]\x1b[0m ${text}\r\n`); }
	private pub(field: string, value: unknown): void {
		this.project.hub.set(['projects', this.project.pid, 'tasks', this.tid, field], value);
	}

	/** Re-read task metadata from the branch and publish it. */
	async refresh(): Promise<void> {
		const taskMd = await this.branch.read('.tps/TASK.md') ?? '';
		this.title = taskMd.split('\n')[0].replace(/^#+\s*/, '').trim() || this.tid;
		this.state = { ...DEFAULT_STATE, ...(await this.readState()) };
		this.pub('title', this.title);
		this.pub('phase', this.state.phase);
		this.pub('implementModel', this.state.implementModel);
		this.pub('reviewModel', this.state.reviewModel);
		this.pub('skipHumanReview', this.state.skipHumanReview);
		this.pub('maxReviewCycles', this.state.maxReviewCycles);
		this.pub('dependencies', this.state.dependencies);
		this.pub('startWhenDepsDone', this.state.startWhenDepsDone);
		this.pub('hasQuestions', /^##\s*questions/im.test(taskMd));
		this.pub('status', this.branch.status);
		this.pub('agent', this.role ?? 'idle');
		this.pub('appPort', this.branch.container?.appPort);
		this.pub('live', this.live);
	}

	private async readState(): Promise<Partial<TaskState>> {
		try {
			const state = JSON.parse(await this.branch.read('.tps/state.json') ?? '');
			if (!PHASES.includes(state.phase)) delete state.phase;
			return state;
		} catch {}
		// Legacy tasks kept their metadata in .tps/phase and .tps/config.json.
		const legacy: Partial<TaskState> = {};
		const phase = (await this.branch.read('.tps/phase'))?.trim() as Phase;
		if (PHASES.includes(phase)) legacy.phase = phase;
		try {
			const cfg = JSON.parse(await this.branch.read('.tps/config.json') ?? '');
			if (cfg.implementModel ?? cfg.model) legacy.implementModel = cfg.implementModel ?? cfg.model;
			if (cfg.reviewModel) legacy.reviewModel = cfg.reviewModel;
			if (cfg.autoDone !== undefined) legacy.skipHumanReview = !!cfg.autoDone;
		} catch {}
		return legacy;
	}

	/** Merge into the task state, commit it to the branch, and publish. */
	async writeState(partial: Partial<TaskState>, message = 'tps: state change'): Promise<void> {
		Object.assign(this.state, partial);
		await this.writeTps({ '.tps/state.json': JSON.stringify(this.state, null, '\t') + '\n' }, message);
		await this.refresh();
	}

	/** Called when a user opens the task's page: bring the workspace up. */
	async open(): Promise<void> {
		this.touch();
		await this.project.ensureReady();
		await this.branch.up();
	}

	async setConfig(partial: Record<string, unknown>): Promise<void> {
		const patch: Partial<TaskState> = {};
		if (typeof partial.implementModel === 'string') patch.implementModel = partial.implementModel;
		if (typeof partial.reviewModel === 'string') patch.reviewModel = partial.reviewModel;
		if (partial.skipHumanReview !== undefined) patch.skipHumanReview = !!partial.skipHumanReview;
		if (partial.startWhenDepsDone !== undefined) patch.startWhenDepsDone = !!partial.startWhenDepsDone;
		if (partial.maxReviewCycles !== undefined) patch.maxReviewCycles = Math.max(0, parseInt(String(partial.maxReviewCycles)) || 0);
		if (Array.isArray(partial.dependencies)) {
			patch.dependencies = partial.dependencies.filter((d): d is string => typeof d === 'string' && d !== this.branch.ref);
		}
		await this.writeState(patch, 'tps: config change');
		this.project.depsChanged();
	}

	async writeTps(files: Record<string, string>, message: string): Promise<void> {
		if (this.branch.hasWorktree) {
			for (const [path, content] of Object.entries(files)) {
				await writeFile(join(this.branch.worktreeDir, path), content);
			}
			await this.branch.commitAll(message);
		} else {
			await this.project.repo.writeFilesToRef(this.branch.ref, files, message);
		}
	}

	/** User-initiated phase transition (drag on the board, or the dropdown). */
	async moveTo(target: Phase): Promise<void> {
		this.touch();
		if (!PHASES.includes(target)) throw new Error(`Invalid phase: ${target}`);
		if (target === this.phase) return;
		if (this.moving) throw new Error('Another phase transition is in progress');
		this.moving = true;
		try {
			await this.stopAgent();
			switch (target) {
				case 'plan':
					await this.branch.commitAll('tps: checkpoint');
					await this.writeState({ phase: 'plan' });
					break;
				case 'implement':
					await this.startImplementPhase();
					break;
				case 'review':
					if (this.state.reviewModel !== 'none' && this.phase === 'implement') {
						// The agent review is part of the implement phase; it decides
						// itself whether the task then lands in review or done.
						this.termNote('agent review first; the task stays in Implement until that concludes');
						this.startAgent('implement', { startRole: 'reviewer' });
					} else {
						await this.branch.commitAll('tps: checkpoint');
						await this.enterReview();
					}
					break;
				case 'done':
					this.startAgent('merge');
					break;
			}
		} finally {
			this.moving = false;
		}
		this.project.depsChanged();
	}

	/** Enter the implement phase: rebase onto a fresh base when possible, mark the review base. */
	private async startImplementPhase(): Promise<void> {
		const { repo, defaultBranch } = this.project;
		await this.project.ensureReady();
		await this.branch.commitAll('tps: checkpoint');
		const mainTip = await repo.tip(defaultBranch);
		if (mainTip && !(await repo.isAncestor(mainTip, await this.branch.tip()))) {
			if (await this.branch.tryRebase(mainTip)) {
				this.termNote(`rebased onto the current ${defaultBranch}`);
			} else {
				this.termNote(`rebasing onto ${defaultBranch} would conflict; continuing from the current base`);
			}
		}
		await this.writeState({ phase: 'implement', reviewBase: await this.branch.tip(), cycles: 0 }, 'tps: start implementation');
		if (this.state.implementModel !== 'none') this.startAgent('implement');
		else this.bgUp(); // the human implements; make sure VS Code is usable
	}

	/** Park the task for human review, making sure REVIEW.md is there to write in. */
	private async enterReview(): Promise<void> {
		if (!(await this.branch.read('.tps/REVIEW.md'))) {
			await this.writeTps({ '.tps/REVIEW.md': REVIEW_TEMPLATE }, 'tps: start review');
		}
		await this.writeState({ phase: 'review' });
	}

	/** Let the agent iterate on TASK.md while in the plan phase. */
	async runPlanAgent(): Promise<void> {
		if (this.phase !== 'plan') throw new Error('Task is not in the plan phase');
		if (this.state.implementModel === 'none') throw new Error('No implementation model configured for this task');
		this.startAgent('plan');
	}

	/** Bring the workspace up in the background, reporting failure to the terminal. */
	private bgUp(): void {
		this.branch.up().catch(e => this.termNote(`workspace start failed: ${e.message}`));
	}

	async stopAgent(): Promise<void> {
		if (!this.role) return;
		this.stopRequested = true;
		this.termNote('stopping agent…');
		try { await killClaude(this.branch); } catch {}
		await this.agentPromise;
	}

	async delete(): Promise<void> {
		await this.stopAgent();
		await this.branch.down();
		await run(['podman', 'rm', '-f', '-t', '2', `${this.branch.containerName}-done`], { check: false });
		await rm(this.branch.worktreeDir + '-done', { recursive: true, force: true });
		await this.project.repo.tryGit(['update-ref', '-d', `refs/heads/${this.branch.ref}`]);
		await this.project.repo.tryGit(['update-ref', '-d', `refs/heads/tps-done/${this.tid}`]);
		this.project.tasks.delete(this.tid);
		this.project.hub.set(['projects', this.project.pid, 'tasks', this.tid], undefined);
		this.project.depsChanged();
	}

	// --- the agent driver ---

	/** Kick off agent work of the given kind; runs in the background. */
	startAgent(kind: 'plan' | 'implement' | 'merge', opts: { startRole?: StageRole; resume?: boolean } = {}): void {
		if (this.role) return;
		this.stopRequested = false;
		this.agentPromise = this.agentLoop(kind, opts)
			.catch(e => {
				this.termNote(`error: ${e.message}`);
				log(`agent ${this.key}:`, e);
			})
			.finally(() => {
				this.role = undefined;
				this.pub('agent', 'idle');
				this.touch();
				this.project.depsChanged();
			});
	}

	private setRole(role: Role): void {
		this.role = role;
		this.pub('agent', role);
		this.touch();
	}

	private async agentLoop(kind: 'plan' | 'implement' | 'merge', opts: { startRole?: StageRole; resume?: boolean }): Promise<void> {
		if (kind === 'merge') {
			this.setRole('merge');
			await this.project.finishTask(this);
			return;
		}
		await this.branch.up();
		await this.branch.commitAll('tps: checkpoint');
		if (kind === 'plan') {
			this.setRole('planner');
			await this.runAgent(plannerPrompt(), this.state.implementModel);
			return;
		}
		const merge = await this.implementStage(opts.startRole ?? 'implementer', opts.resume);
		if (merge && !this.stopRequested) {
			this.setRole('merge');
			await this.project.finishTask(this);
		}
	}

	/**
	 * The implement phase: implementer runs and agent reviews alternate until the
	 * review comes back clean or the cycle budget runs out. Returns whether the
	 * task should be merged (implementation finished and human review skipped).
	 */
	private async implementStage(startRole: StageRole, resume = false): Promise<boolean> {
		let skipImplementer = startRole === 'reviewer';
		while (!this.stopRequested) {
			const { implementModel, reviewModel } = this.state;
			if (!skipImplementer && implementModel !== 'none') {
				const hadReview = await this.hasReviewItems();
				const hadQuestions = await this.hasQuestions();
				this.setRole('implementer');
				await this.runAgent(resume ? continuePrompt() : implementerPrompt(hadReview), implementModel, resume);
				resume = false;
				if (hadReview) {
					for (let i = 0; i < MAX_URGES && !this.stopRequested && await this.hasReviewItems(); i++) {
						this.termNote('.tps/REVIEW.md still has items; sending the implementer back in');
						await this.runAgent(urgeReviewEmptyPrompt(), implementModel, true);
					}
				}
				if (this.stopRequested) break;
				if (!hadQuestions && await this.hasQuestions()) {
					this.termNote('the implementer has questions for you (see TASK.md); parking the task in Plan');
					await this.writeState({ phase: 'plan' });
					break;
				}
			}
			skipImplementer = false;
			if (reviewModel === 'none') return this.implementationDone();
			this.setRole('reviewer');
			const mergeBase = await this.project.repo.mergeBase(this.project.defaultBranch, this.branch.ref)
				?? await this.branch.tip();
			await this.runAgent(
				resume ? continuePrompt() : reviewerPrompt(this.state.reviewBase ?? mergeBase, mergeBase),
				reviewModel, resume);
			resume = false;
			if (this.stopRequested) break;
			if (!(await this.hasReviewItems())) return this.implementationDone();
			if (implementModel === 'none' || this.state.cycles >= this.state.maxReviewCycles) {
				this.termNote('review items remain; handing over to you');
				await this.enterReview();
				break;
			}
			await this.writeState({ cycles: this.state.cycles + 1 }, 'tps: next implement cycle');
		}
		return false;
	}

	/** Implementation finished and the agent review (if any) passed. */
	private async implementationDone(): Promise<boolean> {
		if (this.state.skipHumanReview) {
			this.termNote('implementation finished; human review is skipped, merging');
			return true;
		}
		await this.enterReview();
		this.termNote('implementation finished; ready for your review (.tps/REVIEW.md)');
		return false;
	}

	/** One claude run in the current role, making sure all work ends up committed. */
	private async runAgent(prompt: string, model: string, resume = false): Promise<void> {
		const code = await this.claude(prompt, { model, resume });
		if (code !== 0 && !this.stopRequested) this.termNote(`claude exited with code ${code}`);
		for (let i = 0; i < MAX_URGES && !this.stopRequested && await this.branch.hasUncommitted(); i++) {
			this.termNote('uncommitted work left behind; urging the agent to commit it');
			await this.claude(urgeCommitPrompt(), { model, resume: true });
		}
		await this.branch.commitAll('tps: checkpoint'); // whatever still floats (agent session state etc.)
		await this.refresh();
	}

	private claude(prompt: string, opts: { model?: string; resume?: boolean } = {}): Promise<number> {
		if (this.stopRequested) return Promise.resolve(130); // stop landed between runs
		const model = opts.model && opts.model !== 'none' ? opts.model : this.anyModel;
		return runClaude(this.branch, {
			prompt, model, resume: opts.resume,
			system: this.role && this.role !== 'merge' ? workflowSystem(this.role) : undefined,
			onData: d => this.term(d),
		});
	}

	/** Some model, for runs that must happen regardless of configuration (merges). */
	get anyModel(): string {
		return this.state.implementModel !== 'none' ? this.state.implementModel
			: this.state.reviewModel !== 'none' ? this.state.reviewModel : 'sonnet';
	}

	private async hasReviewItems(): Promise<boolean> {
		const content = await this.branch.read('.tps/REVIEW.md');
		return !!content && !!content.replace(/<!--[\s\S]*?-->/g, '').trim();
	}

	private async hasQuestions(): Promise<boolean> {
		return /^##\s*questions/im.test(await this.branch.read('.tps/TASK.md') ?? '');
	}

	// --- the 'live' indicator: is something answering on the task's $PORT? ---

	private updateLivePoll(): void {
		const port = this.branch.status === 'up' ? this.branch.container?.appPort : undefined;
		this.pub('appPort', port);
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
		const port = this.branch.container?.appPort;
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

	// --- reacting to .tps/ file changes (inotify via Branch) ---

	private async onTps(files: Set<string>): Promise<void> {
		this.touch();
		await this.refresh();
		if (files.has('Containerfile')) this.cfSettle.touch();
	}

	private async onContainerfileSettled(): Promise<void> {
		if (this.branch.status !== 'up') return;
		if (await this.branch.imageCurrent()) return; // e.g. our own rebase/reset touched the file
		this.termNote('Containerfile changed; recreating the container');
		const interrupted = this.role === 'implementer' || this.role === 'reviewer' ? this.role : undefined;
		await this.stopAgent();
		await this.branch.commitAll('tps: checkpoint (Containerfile changed)');
		await this.branch.up();
		if (interrupted && this.phase === 'implement') {
			this.startAgent('implement', { startRole: interrupted, resume: true });
		}
	}
}

/** A registered project: its repo, its tasks, and the merge-to-done flow. */
export class Project {
	repo: Repo;
	tasks = new Map<string, Task>();
	defaultBranch = 'main';
	private watchers: FSWatcher[] = [];
	private rescan = new Debouncer(500, () => void this.scan().catch(e => log(`scan ${this.pid}:`, e)));
	private depsTimer?: NodeJS.Timeout;

	readonly manager: ProjectManager;
	readonly pid: string;
	readonly name: string;
	readonly dir: string;

	constructor(manager: ProjectManager, pid: string, name: string, dir: string) {
		this.manager = manager;
		this.pid = pid;
		this.name = name;
		this.dir = dir;
		this.repo = new Repo(dir);
	}

	get hub(): Hub { return this.manager.hub; }
	get worktreesDir(): string { return join(this.manager.dataDir, 'worktrees', this.pid); }
	private pub(field: string, value: unknown): void { this.hub.set(['projects', this.pid, field], value); }

	async init(): Promise<void> {
		this.defaultBranch = await this.repo.headBranch()
			?? (await this.repo.tip('main') ? 'main' : 'master');
		this.hub.set(['projects', this.pid], { name: this.name, dir: this.dir, tasks: {} });
		await this.refreshMeta();
		await this.scan();
		// React to external ref changes (commits, branch updates) in real time.
		// Only ref-related files count: reacting to e.g. the index (which our own
		// `git status` calls touch) would make scanning feed itself forever.
		const gitDir = await this.repo.git(['rev-parse', '--absolute-git-dir']);
		const onChange = () => this.rescan.touch();
		this.watchers.push(watch(gitDir, (_event, f) => {
			if (f === 'HEAD' || f === 'packed-refs' || f?.toString().startsWith('refs')) onChange();
		}));
		if (existsSync(join(gitDir, 'refs/heads'))) {
			this.watchers.push(watch(join(gitDir, 'refs/heads'), { recursive: true }, onChange));
		}
		for (const w of this.watchers) w.on('error', () => {});
	}

	async close(): Promise<void> {
		for (const w of this.watchers) w.close();
		for (const task of this.tasks.values()) {
			await task.stopAgent();
			await task.branch.down();
		}
	}

	async refreshMeta(): Promise<void> {
		const hasContainerfile = !!(await this.defaultContainerfile());
		this.pub('defaultBranch', this.defaultBranch);
		this.pub('needsSetup', !hasContainerfile);
		this.pub('dirty', await this.repo.isDirty());
		if (!hasContainerfile) {
			this.pub('detected', detectTools(await this.repo.listRoot(this.defaultBranch)));
		}
	}

	defaultContainerfile(): Promise<string | undefined> {
		return this.repo.readFile(this.defaultBranch, '.tps/Containerfile');
	}

	async ensureReady(): Promise<void> {
		if (!(await this.defaultContainerfile())) {
			throw new Error('This project has no .tps/Containerfile yet; create one from the project board first');
		}
	}

	/** (Re)discover task branches; called on startup and on git ref changes. */
	async scan(): Promise<void> {
		const ids = (await this.repo.branches('tps/')).map(ref => ref.slice('tps/'.length));
		for (const tid of ids) {
			if (!this.tasks.has(tid)) this.tasks.set(tid, new Task(this, tid));
		}
		for (const [tid, task] of this.tasks) {
			if (!ids.includes(tid)) { // branch removed externally
				await task.branch.down();
				this.tasks.delete(tid);
				this.hub.set(['projects', this.pid, 'tasks', tid], undefined);
			}
		}
		await Promise.all([...this.tasks.values()].map(task => task.refresh()));
		await this.refreshMeta();
		this.depsChanged();
	}

	async createContainerfile(toolIds: string[]): Promise<void> {
		const content = generateContainerfile(toolIds);
		if (await this.repo.headBranch() === this.defaultBranch) {
			await mkdir(join(this.dir, '.tps'), { recursive: true });
			await writeFile(join(this.dir, '.tps', 'Containerfile'), content);
			await this.repo.git(['add', '.tps/Containerfile']);
			await this.repo.git(['commit', '--no-verify', '-m', 'tps: add Containerfile', '--', '.tps/Containerfile']);
		} else {
			await this.repo.writeFilesToRef(this.defaultBranch, { '.tps/Containerfile': content }, 'tps: add Containerfile');
		}
		await this.refreshMeta();
	}

	async createTask(title: string, description = ''): Promise<string> {
		await this.ensureReady();
		if (await this.repo.isDirty()) {
			throw new Error(`The project worktree (${this.dir}) has uncommitted changes; commit or stash them first`);
		}
		const mainTip = await this.repo.tip(this.defaultBranch);
		if (!mainTip) throw new Error(`Branch '${this.defaultBranch}' has no commits`);
		title = title.trim() || 'Untitled task';
		const tid = newId();
		const sha = await this.repo.makeCommit({
			base: mainTip,
			set: {
				'.tps/TASK.md': `# ${title}\n\n${description.trim()}\n`,
				'.tps/state.json': JSON.stringify(DEFAULT_STATE, null, '\t') + '\n',
				'.tps/.gitignore': TPS_GITIGNORE,
			},
			parents: [mainTip],
			message: `tps: create task '${title}'`,
		});
		await this.repo.updateRef(`tps/${tid}`, sha);
		const task = new Task(this, tid);
		this.tasks.set(tid, task);
		await task.refresh();
		return tid;
	}

	/** Something dependency-relevant changed; re-check auto-start conditions soon.
	 * Coalesces rather than debounces, so busy periods can't starve the check. */
	depsChanged(): void {
		this.depsTimer ??= setTimeout(() => {
			this.depsTimer = undefined;
			void this.autoStartReady().catch(e => log(`deps ${this.pid}:`, e));
		}, 1_000);
	}

	/** Start implementation for plan tasks whose dependencies are all done. */
	private async autoStartReady(): Promise<void> {
		for (const task of this.tasks.values()) {
			const s = task.state;
			if (s.phase !== 'plan' || !s.startWhenDepsDone || !s.dependencies.length || task.agentBusy) continue;
			const ready = s.dependencies.every(ref => {
				const dep = this.tasks.get(ref.replace(/^tps\//, ''));
				return !dep || dep.state.phase === 'done'; // a deleted dependency no longer blocks
			});
			if (!ready) continue;
			task.termNote('all dependencies are done; starting implementation');
			await task.moveTo('implement').catch(e => task.termNote(`auto-start failed: ${e.message}`));
		}
	}

	/**
	 * Move a task to done: squash its work (minus .tps/, keeping Containerfile)
	 * into a single commit on top of the merge base, rebase that onto the
	 * default branch (letting claude resolve conflicts when needed), and
	 * fast-forward the default branch. The task branch itself is kept.
	 */
	async finishTask(task: Task): Promise<void> {
		const { repo, defaultBranch } = this;
		await task.branch.commitAll('tps: checkpoint (finish)');
		const targetTip = await repo.tip(defaultBranch);
		if (!targetTip) throw new Error(`Branch '${defaultBranch}' has no commits`);
		const tip = await task.branch.tip();
		const base = await repo.mergeBase(targetTip, tip);
		let message = `${task.title}\n\nTPS-Task: tps/${task.tid}`;
		let finalSha: string | undefined;
		if (base) {
			// The squash commit's body lists what the individual commits did.
			const details = (await repo.git(['log', '--reverse', '--format=%s', `${base}..${tip}`]))
				.split('\n').filter(s => s && !s.startsWith('tps:') && !s.startsWith('tps-tmp:'));
			if (details.length > 1) {
				message = `${task.title}\n\n${details.map(s => '- ' + s).join('\n')}\n\nTPS-Task: tps/${task.tid}`;
			}
			const squash = await repo.makeCommit({
				base: tip,
				removeMatching: path => path.startsWith('.tps/') && path !== '.tps/Containerfile',
				parents: [base],
				message,
			});
			if (await repo.git(['rev-parse', `${squash}^{tree}`]) !== await repo.git(['rev-parse', `${base}^{tree}`])) {
				task.termNote(`merging into ${defaultBranch}…`);
				const mt = await run(['git', '-C', repo.dir, 'merge-tree', '--write-tree', targetTip, squash], { check: false });
				if (mt.code === 0) {
					finalSha = await repo.git(['commit-tree', mt.out.trim().split('\n')[0], '-p', targetTip, '-m', message]);
				} else if (mt.code === 1) {
					task.termNote('merge conflicts; spinning up a container so claude can resolve the rebase');
					finalSha = await this.resolveConflicts(task, squash, targetTip);
				} else {
					throw new Error(`merge-tree failed: ${mt.err.trim()}`);
				}
			}
		}
		if (finalSha) await this.fastForward(targetTip, finalSha);
		await task.branch.down();
		await task.writeState({ phase: 'done' }, 'tps: done');
		await this.refreshMeta();
		task.termNote(finalSha ? `merged into ${defaultBranch} ✔` : 'nothing to merge; task closed ✔');
	}

	/** Rebase `squash` onto the target tip in a temporary branch + container, with claude's help. */
	private async resolveConflicts(task: Task, squash: string, targetTip: string): Promise<string> {
		const { repo } = this;
		const doneRef = `tps-done/${task.tid}`;
		await repo.git(['update-ref', `refs/heads/${doneRef}`, squash]);
		const branch = new Branch(
			repo, doneRef, task.branch.worktreeDir + '-done', task.branch.containerName + '-done',
			() => this.defaultContainerfile(),
			{ log: d => task.term(d) });
		try {
			await branch.up();
			for (let attempt = 0; ; attempt++) {
				try {
					await branch.wtGit(['rebase', targetTip]);
				} catch {
					await runClaude(branch, {
						prompt: rebasePrompt(this.defaultBranch), model: task.anyModel,
						onData: d => task.term(d),
					});
				}
				const gitDir = await branch.wtGit(['rev-parse', '--absolute-git-dir']);
				const rebasing = existsSync(join(gitDir, 'rebase-merge')) || existsSync(join(gitDir, 'rebase-apply'));
				// Untracked files (claude's own .tps/claude/ state) can't break the merge; only tracked changes count.
				const clean = (await run(['git', '-C', branch.worktreeDir, 'diff', '--quiet'], { check: false })).code === 0
					&& (await run(['git', '-C', branch.worktreeDir, 'diff', '--cached', '--quiet'], { check: false })).code === 0;
				const onTarget = (await run(['git', '-C', branch.worktreeDir, 'merge-base', '--is-ancestor', targetTip, 'HEAD'], { check: false })).code === 0;
				if (!rebasing && clean && onTarget) {
					await branch.wtGit(['push', '--quiet', '--force', 'origin', `HEAD:refs/heads/${doneRef}`]);
					return branch.wtGit(['rev-parse', 'HEAD']);
				}
				if (attempt >= 1) throw new Error('claude could not resolve the rebase conflicts; task left in review');
				task.termNote('resolution attempt failed, retrying once');
				try { await branch.wtGit(['rebase', '--abort']); } catch {}
				await branch.wtGit(['reset', '--hard', squash]);
			}
		} finally {
			await branch.down();
			await repo.tryGit(['update-ref', '-d', `refs/heads/${doneRef}`]);
		}
	}

	private async fastForward(oldTip: string, newSha: string): Promise<void> {
		if (await this.repo.headBranch() === this.defaultBranch) {
			// The user's own checkout has the branch checked out: a real merge
			// keeps their working tree in sync.
			await this.repo.git(['merge', '--ff-only', newSha]);
		} else {
			await this.repo.updateRef(this.defaultBranch, newSha, oldTip);
		}
	}
}

interface SavedProject { dir: string; name: string; }

/** The project registry, persisted to ~/.config/tps/projects.json. */
export class ProjectManager {
	projects = new Map<string, Project>();
	readonly dataDir = join(homedir(), '.local', 'share', 'tps');
	readonly configFile = join(homedir(), '.config', 'tps', 'projects.json');

	readonly hub: Hub;

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
		for (const saved of await this.loadConfig()) {
			try {
				await this.load(saved);
			} catch (e) {
				log(`Failed to load project ${saved.dir}:`, (e as Error).message);
			}
		}
		this.hub.set(['ready'], true);
	}

	private async loadConfig(): Promise<SavedProject[]> {
		try { return JSON.parse(await readFile(this.configFile, 'utf8')).projects; } catch { return []; }
	}

	private async saveConfig(): Promise<void> {
		await mkdir(dirname(this.configFile), { recursive: true });
		const projects = [...this.projects.values()].map(p => ({ dir: p.dir, name: p.name }));
		await writeFile(this.configFile, JSON.stringify({ projects }, null, '\t') + '\n');
	}

	private async load(saved: SavedProject): Promise<Project> {
		let pid = slugify(saved.name);
		while (this.projects.has(pid)) pid += 'x';
		const project = new Project(this, pid, saved.name, saved.dir);
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
		const repo = new Repo(dir);
		await repo.git(['rev-parse', '--git-dir']).catch(() => { throw new Error(`${dir} is not a git repository`); });
		if (!(await repo.tip('HEAD'))) throw new Error(`${dir} has no commits yet`);
		for (const p of this.projects.values()) {
			if (p.dir === dir) throw new Error(`${dir} is already registered as '${p.name}'`);
		}
		const project = await this.load({ dir, name: name?.trim() || basename(dir) });
		await this.saveConfig();
		return { pid: project.pid };
	}

	async remove(pid: string): Promise<void> {
		const project = this.getProject(pid);
		await project.close();
		this.projects.delete(pid);
		this.hub.set(['projects', pid], undefined);
		await this.saveConfig();
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
				if (task.branch.status === 'up' && !task.agentBusy && task.viewers === 0
						&& Date.now() - task.lastActivity > IDLE_SHUTDOWN_MS) {
					task.termNote('workspace idle, shutting down');
					await task.branch.down().catch(e => log(`idle shutdown ${task.key}:`, e));
				}
			}
		}
	}

	async shutdown(): Promise<void> {
		await Promise.all([...this.projects.values()].map(p => p.close()));
	}
}
