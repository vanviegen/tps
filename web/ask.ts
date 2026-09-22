import A from 'aberdeen';
import * as S from 'staffa';
import { $state } from './conn.ts';
import { cmd } from './util.ts';

/**
 * The questions ssh raises on the way to a host: a password, a key
 * passphrase, an unknown host key. Each waits on the host it is about, which
 * sits still saying what it needs rather than retrying and re-prompting
 * behind your back. Answering is what makes the next attempt, and the answer
 * is remembered for the ones after it.
 */

// The hosts that already have a dialog on screen: clicking a strip twice must
// not stack two of them.
const open = new Set<string>();

/** Put the question a host waits on to the user, as a dialog. */
export function showAsk(hid: string): void {
	if (open.has(hid)) return;
	const ask = A.peek(() => ({ ...$state.hosts?.[hid]?.ask }));
	if (!ask.title) return;
	open.add(hid);
	const $form = A.proxy({ value: '' });
	void S.dialog({ header: ask.title, content: close => {
		// The question goes when the host is removed or answered elsewhere;
		// the dialog goes with it. Closing it without answering leaves the
		// question standing: nothing waits on it, so there is nobody to tell.
		A(() => { if (!$state.hosts?.[hid]?.ask) close(); });
		S.form({
			submit: () => { void cmd('answer', { hid, value: $form.value }); close(); },
			content: () => {
				A('p white-space:pre-wrap text=', ask.text);
				if (ask.kind !== 'confirm') S.textline({ type: 'password', bind: A.ref($form, 'value') });
			},
			actions: () => S.button({ content: ask.kind === 'confirm' ? 'Connect' : 'Log in', type: 'submit' }),
		});
	}}).then(() => open.delete(hid));
}
