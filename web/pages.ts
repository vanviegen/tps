import A from 'aberdeen';
import * as S from 'staffa';
import { externalLink } from 'staffa/icons.js';
import { drawCode } from './code.ts';

/**
 * The pages a task's container puts in front of the user, as tabs beside VS
 * Code: one for each forwarded port that answers HTTP (port_<port>, on its own
 * front page), and one for each URL the container set (see daemon/urls.go),
 * which takes the place of a port's of the same id. A page loads the first
 * time its tab is opened. A URL set while the task is on screen is switched to
 * there and then; one set while it was not, once it is opened.
 */

interface Page { id: string; url: string; at?: number }

/** The tab each task shows, by `pid/tid`: a page's id, or '' for VS Code. */
const $shown: Record<string, string> = A.proxy({});
/** The newest URL each task had set when last looked at, by `pid/tid`. */
const seenAt: Record<string, number> = {};

function pages($t: any): Page[] {
	const byId = new Map<string, Page>();
	for (const p of $t.ports ?? []) if (p.live) byId.set(`port_${p.port}`, { id: `port_${p.port}`, url: `http://localhost:${p.port}/` });
	for (const u of ($t.urls ?? []) as Page[]) byId.set(u.id, u);
	return [...byId.values()];
}

/** "port_8080" is "Port 8080". */
function title(id: string): string {
	return id.charAt(0).toUpperCase() + id.slice(1).replace(/_/g, ' ');
}

/** The task's tabs while it has pages, for the palette: VS Code's id is ''. */
export function taskTabs($t: any): { id: string; title: string }[] {
	const list = pages($t);
	return list.length ? [{ id: '', title: 'VS Code' }, ...list.map(p => ({ id: p.id, title: title(p.id) }))] : [];
}

/** Switch the task (by `pid/tid`) to a tab. */
export function showTab(key: string, id: string): void {
	$shown[key] = id;
}

/**
 * Where this browser reaches a URL: one on localhost is the container's, and
 * goes through the port forwarded for it — or nowhere, where none is.
 */
function reach($t: any, url: string): string | undefined {
	let u: URL;
	try { u = new URL(url); } catch { return; }
	if (u.hostname !== 'localhost' && u.hostname !== '127.0.0.1') return url;
	const port = Number(u.port) || (u.protocol === 'https:' ? 443 : 80);
	const p = ($t.ports ?? []).find((p: any) => p.port === port);
	if (!p) return;
	u.hostname = location.hostname;
	u.port = String(p.host);
	return u.href;
}

/**
 * The right column of a task with pages: a tab strip of VS Code (drawn by
 * `code`, given the top its frame starts at) and the pages, or VS Code alone
 * while there are none.
 */
export function drawTaskPages(key: string, $t: any, left: string, code: (top: string) => void): void {
	A(() => {
		let newest: Page | undefined;
		for (const u of ($t.urls ?? []) as Page[]) if (!newest || u.at! > newest.at!) newest = u;
		const seen = seenAt[key];
		seenAt[key] = Math.max(seen ?? 0, newest?.at ?? 0);
		if (newest && (seen === undefined ? A.peek(() => $t.urlsNew) : newest.at! > seen)) $shown[key] = newest.id;
	});
	A(() => {
		const list = pages($t);
		if (!list.length) return code('0px');
		// The frames are fixed (see code.ts): they start right under the strip.
		const $top = A.proxy({ px: 0 });
		const top = () => `${$top.px}px`;
		// Up against the top of the window, out of the column's padding, as VS Code is.
		const el = A('div', 'margin-top: calc(-1 * $3);', () => S.tabs({
			bind: A.ref($shown, key),
			tabs: [
				{ id: '', label: 'VS Code', content: () => A(() => code(top())) },
				...list.map(page => ({
					id: page.id,
					label: () => {
						A('text=', title(page.id));
						const url = reach($t, page.url);
						if (url) A('span display:inline-flex ml:$1', 'click=', (e: Event) => { e.stopPropagation(); window.open(url, '_blank'); }, () => {
							S.addTooltip({ tip: 'Open in a new browser tab' });
							externalLink({ size: '0.9em' });
						});
					},
					content: () => A(() => {
						const url = reach($t, page.url);
						if (url) drawCode(`${key}#${page.id}`, url, url, left, top());
						else A('p text=', `${page.url} is this task's container, on a port that is not forwarded: Containerfile.dev needs an EXPOSE line for it.`);
					}),
				})),
			],
		})) as HTMLElement;
		$top.px = (el.querySelector('.s-tabbar') as HTMLElement).getBoundingClientRect().bottom;
	});
}
