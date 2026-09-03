import { randomBytes } from 'node:crypto';
import { rm } from 'node:fs/promises';
import { join } from 'node:path';
import { run, type RunOpts } from './util.ts';

/** Identity used for every commit TPS creates. */
const GIT_ENV = {
	GIT_AUTHOR_NAME: 'TPS', GIT_AUTHOR_EMAIL: 'tps@localhost',
	GIT_COMMITTER_NAME: 'TPS', GIT_COMMITTER_EMAIL: 'tps@localhost',
};

export const TMP_COMMIT_MARKER = 'tps-tmp: workspace shutdown snapshot (soft-reset on restore)';

export interface TreeEntry { mode: string; sha: string; path: string; }

/**
 * Git access for one repository, including everything needed to read from and
 * commit to branches *without* a worktree (via `git show` and a temporary index).
 */
export class Repo {
	readonly dir: string;
	constructor(dir: string) {
		this.dir = dir;
	}

	async git(args: string[], opts: RunOpts = {}): Promise<string> {
		return Repo.gitIn(this.dir, args, opts);
	}

	/** Run git in an arbitrary directory (e.g. a worktree) with the TPS identity. */
	static async gitIn(dir: string, args: string[], opts: RunOpts = {}): Promise<string> {
		const r = await run(['git', '-C', dir, ...args], { ...opts, env: { ...GIT_ENV, ...opts.env } });
		return r.out.replace(/\n$/, '');
	}

	/** Like git(), but returns undefined instead of throwing on failure. */
	async tryGit(args: string[], opts: RunOpts = {}): Promise<string | undefined> {
		try { return await this.git(args, opts); } catch { return undefined; }
	}

	/** The branch checked out in the repo's own worktree, if any. */
	headBranch(): Promise<string | undefined> {
		return this.tryGit(['symbolic-ref', '--short', 'HEAD']);
	}

	tip(ref: string): Promise<string | undefined> {
		return this.tryGit(['rev-parse', '--verify', '--quiet', ref + '^{commit}']);
	}

	/** Read a file from a ref without needing a worktree. Exact content, no newline stripping. */
	async readFile(ref: string, path: string): Promise<string | undefined> {
		const r = await run(['git', '-C', this.dir, 'show', `${ref}:${path}`], { check: false });
		return r.code === 0 ? r.out : undefined;
	}

	async lsTree(ref: string, prefix?: string): Promise<TreeEntry[]> {
		const out = await this.tryGit(['ls-tree', '-r', '-z', ref, ...(prefix ? ['--', prefix] : [])]);
		if (!out) return [];
		return out.split('\0').filter(Boolean).map(line => {
			const [meta, path] = splitOnce(line, '\t');
			const [mode, , sha] = meta.split(' ');
			return { mode, sha, path };
		});
	}

	/** Top-level file/dir names of a ref (used for toolchain detection). */
	async listRoot(ref: string): Promise<string[]> {
		const out = await this.tryGit(['ls-tree', '--name-only', '-z', ref]);
		return out ? out.split('\0').filter(Boolean) : [];
	}

	mergeBase(a: string, b: string): Promise<string | undefined> {
		return this.tryGit(['merge-base', a, b]);
	}

	async isDirty(): Promise<boolean> {
		return (await this.git(['status', '--porcelain'])) !== '';
	}

	async branches(prefix: string): Promise<string[]> {
		const out = await this.git(['for-each-ref', '--format=%(refname:short)', `refs/heads/${prefix}`]);
		return out ? out.split('\n') : [];
	}

	updateRef(ref: string, sha: string, oldSha?: string): Promise<string> {
		return this.git(['update-ref', `refs/heads/${ref}`, sha, ...(oldSha !== undefined ? [oldSha] : [])]);
	}

	deleteRef(ref: string): Promise<string> {
		return this.git(['update-ref', '-d', `refs/heads/${ref}`]);
	}

	commitMessage(ref: string): Promise<string | undefined> {
		return this.tryGit(['log', '-1', '--format=%B', ref]);
	}

	/** True when `ancestor` is an ancestor of (or equal to) `ref`. */
	async isAncestor(ancestor: string, ref: string): Promise<boolean> {
		return (await run(['git', '-C', this.dir, 'merge-base', '--is-ancestor', ancestor, ref], { check: false })).code === 0;
	}

	/**
	 * Build a commit through a temporary index, so branch trees can be created
	 * and rewritten without any worktree. Returns the new commit sha.
	 */
	async makeCommit(opts: {
		base?: string; // ref/sha whose tree to start from; empty tree when omitted
		overlay?: { ref: string; prefix: string }; // copy these entries from another ref
		set?: Record<string, string>; // path -> file content
		removeMatching?: (path: string) => boolean;
		parents: string[];
		message: string;
	}): Promise<string> {
		const indexFile = join(this.dir, '.git', 'tps-index-' + randomBytes(4).toString('hex'));
		const env = { GIT_INDEX_FILE: indexFile };
		try {
			await this.git(opts.base ? ['read-tree', opts.base] : ['read-tree', '--empty'], { env });
			if (opts.overlay) {
				const entries = await this.lsTree(opts.overlay.ref, opts.overlay.prefix);
				if (entries.length) {
					const input = entries.map(e => `${e.mode} ${e.sha} 0\t${e.path}`).join('\n') + '\n';
					await this.git(['update-index', '--add', '--index-info'], { env, input });
				}
			}
			for (const [path, content] of Object.entries(opts.set || {})) {
				const sha = await this.git(['hash-object', '-w', '--stdin'], { input: content });
				await this.git(['update-index', '--add', '--cacheinfo', `100644,${sha},${path}`], { env });
			}
			if (opts.removeMatching) {
				const files = (await this.git(['ls-files', '-z'], { env })).split('\0').filter(Boolean);
				const doomed = files.filter(opts.removeMatching);
				if (doomed.length) {
					await this.git(['update-index', '--force-remove', '-z', '--stdin'], { env, input: doomed.join('\0') });
				}
			}
			const tree = await this.git(['write-tree'], { env });
			const parentArgs = opts.parents.flatMap(p => ['-p', p]);
			return await this.git(['commit-tree', tree, ...parentArgs, '-m', opts.message]);
		} finally {
			await rm(indexFile, { force: true });
		}
	}

	/** Commit changed/added files onto a ref without a worktree. */
	async writeFilesToRef(ref: string, files: Record<string, string>, message: string): Promise<string> {
		const tip = await this.tip(ref);
		if (!tip) throw new Error(`No such ref: ${ref}`);
		const sha = await this.makeCommit({ base: tip, set: files, parents: [tip], message });
		await this.updateRef(ref, sha, tip);
		return sha;
	}
}

function splitOnce(s: string, sep: string): [string, string] {
	const i = s.indexOf(sep);
	return [s.slice(0, i), s.slice(i + 1)];
}
