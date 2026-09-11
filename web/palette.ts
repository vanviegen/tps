import * as route from 'aberdeen/route';
import * as S from 'staffa';
import { claimKeyInCode } from './code.ts';
import { sortedProjects } from './projects.ts';
import { branchLabel, isFinished, pathTo, phaseOrder, PHASE_LABELS, taskTitle, type Phase } from './util.ts';

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

/** One place to go: the path to it, and the text you type against. */
interface Destination { value: string; label: string }

/**
 * Everywhere the palette can take you, in the order the board would show it:
 * projects with something waiting first, and under each its own checkout —
 * named for its branch, as the sidebar and the tab title name it — and then
 * its tasks, the done ones last. The label carries the project's name and the
 * task's phase, as matching is on the label alone — so "human" finds what
 * waits for you.
 */
function destinations(): Destination[] {
	const out: Destination[] = [];
	for (const [pid, $p] of sortedProjects()) {
		out.push({ value: pathTo(pid), label: $p.name });
		out.push({ value: pathTo(pid, 'base'), label: `${$p.name} › ${branchLabel($p)}` });
		const tasks = Object.entries($p.tasks ?? {}) as [string, any][];
		tasks.sort((a, b) => {
			const [ka, kb] = [phaseOrder(a[1], a[0]), phaseOrder(b[1], b[0])];
			return (isFinished(a[1]) ? 1 : 0) - (isFinished(b[1]) ? 1 : 0) || ka[0] - kb[0] || ka[1] - kb[1];
		});
		for (const [tid, $t] of tasks) {
			const phase = PHASE_LABELS[$t.phase as Phase] ?? $t.phase;
			out.push({ value: pathTo(pid, tid), label: `${$p.name} › ${taskTitle($t)} · ${phase}` });
		}
	}
	return out;
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
				set value(path: string) { void route.go(path); close(); },
			};
			S.autocomplete({ placeholder: 'Type a project or task…', allowCustom: false, options: destinations, bind });
		},
	});
}
