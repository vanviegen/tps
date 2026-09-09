import A from 'aberdeen';

/**
 * VS Code, running in a container, filling the right column.
 *
 * The iframes live in a fixed overlay of their own rather than in the column:
 * moving an iframe in the DOM reloads it, and a code-server reload costs
 * seconds. Switching away only hides one, and it is kept around (at most
 * `MAX`, for at most `TTL`), so switching back is instant.
 */

const MAX = 3;
const TTL = 10 * 60_000;

interface View {
	key: string;
	el: HTMLIFrameElement;
	/** How many scopes are showing it, and when the last of them stopped. */
	uses: number;
	idle: number;
}

const views: View[] = [];

const overlay = document.createElement('div');
overlay.style.cssText = 'position:fixed;top:0;right:0;bottom:0;left:calc(var(--leftw) + 1px);display:none';
document.body.appendChild(overlay);

/** Show VS Code at `src`, for as long as the calling reactive scope lives. */
export function drawCode(key: string, src: string): void {
	let view = views.find(v => v.key === key);
	if (!view) {
		// Room first: the view unused for the longest goes.
		while (views.length >= MAX) {
			const lru = views.filter(v => !v.uses).sort((a, b) => a.idle - b.idle)[0];
			if (!lru) break;
			drop(lru);
		}
		const el = document.createElement('iframe');
		el.setAttribute('allow', 'clipboard-read; clipboard-write');
		el.style.cssText = 'position:absolute;inset:0;width:100%;height:100%;border:0';
		el.src = src;
		el.addEventListener('load', () => { el.focus(); shareKeys(el); });
		overlay.appendChild(el);
		views.push(view = { key, el, uses: 0, idle: 0 });
	}
	const shown = view;
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

/** Only the views in use are visible, and the overlay is out of the way when none is. */
function sync(): void {
	let any = false;
	for (const v of views) {
		v.el.style.display = v.uses ? 'block' : 'none';
		any ||= !!v.uses;
	}
	overlay.style.display = any ? 'block' : 'none';
}

// A cached view doesn't outstay its welcome.
setInterval(() => {
	const now = Date.now();
	for (const v of [...views]) if (!v.uses && now - v.idle > TTL) drop(v);
}, 60_000);
