import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { claimKeyInCode } from './code.ts';
import { closeBase, sortedProjects } from './projects.ts';
import { addTask } from './task.ts';
import { isFinished, pathTo, phaseOrder, PHASE_LABELS, selection, taskTitle, type Phase } from './util.ts';

/**
 * Go anywhere without the mouse: a key opens a field, you type a few letters of
 * a project or a task, and Enter takes you there. Code's ctrl-shift-p, for the
 * one thing this app navigates between.
 *
 * The combination has to be free twice over. A browser keeps some keystrokes
 * from the page entirely — ctrl-shift-t reopens a closed tab and never arrives
 * — and the palette claims its key inside the workspace iframe as well, so
 * whatever VS Code does with it there is what you give up: ctrl-K and ctrl-;
 * are the prefix of a chord, ctrl-shift-G opens source control, ctrl-[ is the
 * outdent. Ctrl-L does arrive (the address bar lets go of it once the page
 * takes it) and costs Code no more than "select current line". `?` lists it,
 * and every other shortcut in reach.
 */
export function bindPalette(): void {
	S.bindKey('mod+l', 'Go to a project or task', showPalette);
	claimKeyInCode(isPaletteKey, showPalette);
}

/** ⌘ on a Mac, Ctrl elsewhere — the same key Staffa's `mod+` stands for. */
const APPLE = /mac|iphone|ipad|ipod/i.test(navigator.platform || navigator.userAgent);

/**
 * `mod+l` again, spelled out as a keydown test: inside the iframe the keystroke
 * is ours to recognise, as Staffa's own matching never sees it.
 */
function isPaletteKey(e: KeyboardEvent): boolean {
	if (e.altKey || e.shiftKey || (APPLE ? e.ctrlKey : e.metaKey)) return false;
	return (APPLE ? e.metaKey : e.ctrlKey) && e.key.toLowerCase() === 'l';
}

/** One entry: the path it goes to (or the word it acts on), and the text you type against. */
interface Destination { value: string; label: string }

/**
 * The entries that are not a place to go: creating a task in a project, which
 * is CREATE and the project id, and closing its directory, CLOSE and the id.
 * Every other value is a path, and a path begins with a slash, so neither can
 * be taken for one.
 */
const CREATE = 'create:';
const CLOSE = 'close:';

/**
 * Everywhere the palette can take you, in the order the board would show it:
 * projects with something waiting first, and under each its own checkout —
 * the project directory, as the sidebar and the tab title name it — then the
 * task to start, which is the one entry here that makes its destination
 * rather than going to it, and then its tasks. The two that open something
 * rather than going somewhere say so, as "project directory" on its own reads as a
 * place and Create task set the pattern. A finished one — merged, or
 * closed without it — is left out: it is done with, and what the board keeps
 * of it is a record rather than somewhere to go. The label carries the
 * project's name and the task's phase, as matching is on the label alone —
 * and it takes a term at a time, from the start of a word, so "tps hum" finds
 * what waits for you here.
 *
 * The project you are in comes first, its whole block of entries with it: the
 * place you want next is nearly always beside the one you are at, so a couple
 * of letters of a task name lands there without a like-named task elsewhere
 * standing in front of it. Its board heads the list, so ctrl-L Enter is the
 * way out of VS Code, which otherwise owns the keyboard. While its directory
 * is on screen, closing that takes the place of opening it.
 */
function destinations(): Destination[] {
	const { pid: here, tid: open } = selection();
	const blocks: Destination[][] = [];
	for (const [pid, $p] of sortedProjects()) {
		const out: Destination[] = [];
		if (pid === here) blocks.unshift(out);
		else blocks.push(out);
		out.push({ value: pathTo(pid), label: $p.name });
		if (pid === here && open === 'base') out.push({ value: CLOSE + pid, label: `${$p.name} › Close project directory` });
		else out.push({ value: pathTo(pid, 'base'), label: `${$p.name} › Open project directory` });
		out.push({ value: CREATE + pid, label: `${$p.name} › Create task` });
		const tasks = (Object.entries($p.tasks ?? {}) as [string, any][]).filter(([, $t]) => !isFinished($t));
		tasks.sort((a, b) => {
			const [ka, kb] = [phaseOrder(a[1], a[0]), phaseOrder(b[1], b[0])];
			return ka[0] - kb[0] || ka[1] - kb[1];
		});
		for (const [tid, $t] of tasks) {
			const phase = PHASE_LABELS[$t.phase as Phase] ?? $t.phase;
			out.push({ value: pathTo(pid, tid), label: `${$p.name} › ${taskTitle($t)} · ${phase}` });
		}
	}
	return blocks.flat();
}

/**
 * The palette. Its `bind` is a pair of accessors rather than state: what you
 * pick is a destination, not a value the field should hold on to and show back.
 */
function showPalette(): void {
	void S.dialog({
		header: 'Go to', attrs: 'w:32rem',
		content: close => {
			const bind = {
				get value(): string { return ''; },
				set value(value: string) {
					close();
					if (value.startsWith(CREATE)) void addTask(value.slice(CREATE.length));
					else if (value.startsWith(CLOSE)) closeBase(value.slice(CLOSE.length));
					else void route.go(value);
				},
			};
			S.autocomplete({
				placeholder: 'Type a project or task…', allowCustom: false, bind,
				options: destinations,
			});
		},
	});
}
