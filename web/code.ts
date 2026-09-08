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
		el.addEventListener('load', () => el.focus());
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
