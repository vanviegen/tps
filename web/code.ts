import A from 'aberdeen';

/**
 * VS Code, running in a container, over the right part of the window.
 *
 * The iframes are fixed-positioned children of the body rather than part of
 * the column they stand in: moving an iframe in the DOM reloads it, and a
 * code-server reload costs seconds. Switching away only hides one, and it is
 * kept around (at most `MAX`, for at most `TTL`), so switching back is
 * instant. Where its left edge is — after the sidebar and the chat column,
 * or after the sidebar alone — is the caller's to say.
 */

A.insertGlobalCss({
	// An iframe is a replaced element: left and right alone leave it at its
	// intrinsic 300x150, so its size is spelled out against the viewport. The
	// easing is what makes it slide over as the columns around it change; a
	// drag of the split (see split.ts) turns it off, so the frame keeps up.
	'.tps-code': 'position:fixed top:0 h:100% border:0; transition: left 0.15s, width 0.15s;',
});

const MAX = 3;
const TTL = 10 * 60_000;

interface View {
	key: string;
	el: HTMLIFrameElement;
	/** Which code-server the frame is talking to; a new one means reload. */
	gen: unknown;
	/** How many scopes are showing it, and when the last of them stopped. */
	uses: number;
	idle: number;
}

const views: View[] = [];

/**
 * Show VS Code at `src`, for as long as the calling reactive scope lives,
 * with its left edge at `left` (a CSS length).
 *
 * `gen` identifies the code-server behind `src`: its port and start time, as
 * a restart changes the latter. VS Code cannot reconnect across one (it puts
 * up a modal asking to reload the window), so a frame left over from an older
 * one is reloaded: while the workspace is still down that lands on the proxy's
 * "not running" page, which retries by itself.
 */
export function drawCode(key: string, src: string, gen: unknown, left: string): void {
	let view = views.find(v => v.key === key);
	if (!view) {
		// Room first: the view unused for the longest goes.
		while (views.length >= MAX) {
			const lru = views.filter(v => !v.uses).sort((a, b) => a.idle - b.idle)[0];
			if (!lru) break;
			drop(lru);
		}
		const el = document.createElement('iframe');
		el.className = 'tps-code';
		el.setAttribute('allow', 'clipboard-read; clipboard-write');
		el.style.display = 'none';
		el.src = src;
		el.addEventListener('load', () => { el.focus(); shareKeys(el); });
		document.body.appendChild(el);
		views.push(view = { key, el, gen, uses: 0, idle: 0 });
	} else if (view.gen !== gen) {
		view.gen = gen;
		view.el.src = src;
	}
	const shown = view;
	shown.el.style.left = left;
	shown.el.style.width = `calc(100% - ${left})`;
	shown.uses++;
	sync();
	setTimeout(() => shown.el.focus(), 100);
	A.clean(() => {
		if (--shown.uses) return;
		shown.idle = Date.now();
		sync();
	});
}

/** A keystroke the app takes from VS Code: how to recognise it, and what it does. */
const claims: { test: (e: KeyboardEvent) => boolean; press: () => void }[] = [];

/**
 * Take one keystroke away from VS Code. Focus lives in the iframe whenever one
 * is up, and a keystroke there never reaches the page around it, so a shortcut
 * that has to work everywhere needs claiming here too — the iframe is
 * same-origin (the dashboard proxies it), which is what makes that possible.
 * Every claim is a key VS Code no longer gets, so they are named one by one and
 * the list stays short: the palette's (see palette.ts) is the only one.
 */
export function claimKeyInCode(test: (e: KeyboardEvent) => boolean, press: () => void): void {
	claims.push({ test, press });
}

/** Offer a frame's keystrokes to the claims, before VS Code sees them. */
function shareKeys(el: HTMLIFrameElement): void {
	try {
		// Capture on the frame's window, the earliest a handler in it can run.
		el.contentWindow?.addEventListener('keydown', (e: KeyboardEvent) => {
			for (const claim of claims) {
				if (!claim.test(e)) continue;
				e.preventDefault();
				e.stopPropagation(); // VS Code's own handlers never see it
				claim.press();
				return;
			}
		}, true);
	} catch { /* another origin after all: its keys are not ours to see */ }
}

function drop(view: View): void {
	view.el.remove();
	views.splice(views.indexOf(view), 1);
}

/** Only the views in use are visible. */
function sync(): void {
	for (const v of views) v.el.style.display = v.uses ? 'block' : 'none';
}

// A cached view doesn't outstay its welcome.
setInterval(() => {
	const now = Date.now();
	for (const v of [...views]) if (!v.uses && now - v.idle > TTL) drop(v);
}, 60_000);
