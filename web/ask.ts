import A from 'aberdeen';
import * as S from 'staffa';
import { $state } from './conn.ts';
import { cmd } from './util.ts';

/**
 * Questions from the server: an ssh password, an unknown host key. Each one
 * belongs to the host it is about, and waits in that host's box until it is
 * clicked — a login prompt is not worth interrupting for, and answering it is
 * a decision, not an alarm. Only a question about a host that is not on the
 * board has nowhere to wait, and opens by itself (see main.ts).
 */

/** The question waiting for this host, as `[id, ask]`, if there is one. */
export function hostAsk(hid: string): [string, any] | undefined {
	for (const [id, $a] of Object.entries($state.ask ?? {}) as [string, any][]) {
		if ($a.host === hid) return [id, $a];
	}
}

/** What answering the question amounts to: what the way in to it is labelled. */
export function askLabel($a: any): string {
	return $a.kind === 'password' ? 'Log in' : $a.kind === 'confirm' ? 'Check key' : 'Answer';
}

/**
 * The question in a line, for the strip that waits with it. An unknown host
 * key comes as a paragraph with a fingerprint in it: that belongs in the
 * dialog, where it can be read, not in the box.
 */
export function askSummary($a: any): string {
	if ($a.kind === 'confirm') return 'This host is not known yet: its key wants checking.';
	return ($a.text || $a.title || '').replace(/\s+/g, ' ').trim();
}

// The questions that already have a dialog on screen: clicking a strip twice
// must not stack two of them.
const open = new Set<string>();

/** Put one question to the user, as a dialog. */
export function showAsk(id: string): void {
	if (open.has(id)) return;
	const ask = A.peek(() => ({ ...$state.ask?.[id] }));
	if (!ask.title) return;
	open.add(id);
	const $form = A.proxy({ value: '' });
	let answered = false;
	const answer = (args: object) => {
		if (answered) return;
		answered = true;
		void cmd('answer', { id: Number(id), ...args });
	};
	void S.dialog({ header: ask.title, content: close => {
		// Whatever asked may give up while the dialog is open (ssh timed out,
		// the host was removed): the question goes, and the dialog with it.
		A(() => { if (!$state.ask?.[id]) close(); });
		S.form({
			submit: () => { answer({ value: $form.value }); close(); },
			content: () => {
				A('p white-space:pre-wrap text=', ask.text);
				if (ask.kind !== 'confirm') S.textline({ type: ask.kind === 'password' ? 'password' : 'text', bind: A.ref($form, 'value') });
			},
			actions: () => S.button({ content: ask.kind === 'confirm' ? 'Connect' : 'OK', type: 'submit' }),
		});
	}}).then(() => {
		open.delete(id);
		// Closed without answering: say so, so ssh stops waiting for it. A
		// question that is already gone has nobody left to tell.
		if (A.peek(() => $state.ask?.[id])) answer({ cancel: true });
	});
}
