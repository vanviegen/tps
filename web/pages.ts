import A from 'aberdeen';
import * as S from 'staffa';
import { externalLink, pencil, rotateCw } from 'staffa/icons.js';
import { drawCode } from './code.ts';
import { portUrl } from './util.ts';

/**
 * The pages a task's container serves, as tabs beside VS Code: one for each
 * forwarded port that answers HTTP, loaded the first time its tab is opened.
 * A port that starts answering while the task is on screen is brought to the
 * foreground.
 */

/** The tab each task shows, by `pid/tid`: a port, or '' for VS Code. */
const $shown: Record<string, string> = A.proxy({});

/**
 * Where a port's frame was last pointed, by its URL's origin and `pid/tid`, and
 * how often it was reloaded. The page is of another origin, so where the user
 * has clicked through to since is not ours to know.
 */
const $pages: Record<string, { url: string; loads: number }> = A.proxy({});

function livePorts($t: any): any[] {
	return ($t.ports ?? []).filter((p: any) => p.live);
}

/** The task's tabs while it has pages, for the palette: VS Code's id is ''. */
export function taskTabs($t: any): { id: string; title: string }[] {
	const list = livePorts($t);
	return list.length ? [{ id: '', title: 'VS Code' }, ...list.map(p => ({ id: String(p.port), title: `Port ${p.port}` }))] : [];
}

/** Switch the task (by `pid/tid`) to a tab. */
export function showTab(key: string, id: string): void {
	$shown[key] = id;
}

/**
 * The right column of a task with pages: a tab strip of VS Code (drawn by
 * `code`, given the top its frame starts at) and the pages, or VS Code alone
 * while there are none.
 */
export function drawTaskPages(key: string, $t: any, left: string, code: (top: string) => void): void {
	let was: any[] | undefined;
	A(() => {
		const list = livePorts($t);
		const fresh = was && list.find(p => !was!.some(q => q.port === p.port));
		was = list;
		if (fresh) $shown[key] = String(fresh.port);
		if (!list.length) return code('0px');
		// The frames are fixed (see code.ts): they start right under the strip.
		const $bar = A.proxy({ bottom: 0, height: 0 });
		const top = () => `${$bar.bottom}px`;
		// Up against the top of the window, out of the column's padding, as VS Code is.
		const el = A('div position:relative', 'margin-top: calc(-1 * $3);', () => S.tabs({
			bind: A.ref($shown, key),
			tabs: [
				{ id: '', label: 'VS Code', content: () => A(() => code(top())) },
				...list.map(p => ({
					id: String(p.port),
					label: () => {
						A('text=', `Port ${p.port}`);
						A('span display:inline-flex ml:$1', 'click=', (e: Event) => { e.stopPropagation(); window.open(portUrl(p), '_blank'); }, () => {
							S.addTooltip({ tip: 'Open in a new browser tab' });
							externalLink({ size: '0.9em' });
						});
					},
					content: () => {
						const id = `${portUrl(p)}${key}`;
						$pages[id] ??= { url: portUrl(p), loads: 0 };
						const $page = $pages[id];
						// Over the right end of the tab bar.
						A('div position:absolute top:0 right:0 display:flex align-items:center gap:$1 pr:$2', () => {
							A('height:', `${$bar.height}px`);
							S.iconButton({ icon: rotateCw, tooltip: 'Reload', attrs: '.small fg:$s-muted', click: () => { $page.loads++; } });
							S.iconButton({ icon: pencil, tooltip: 'Go to another URL', attrs: '.small fg:$s-muted', click: async () => {
								const url = await S.prompt('URL', $page.url);
								if (url) $page.url = url;
							}});
						});
						A(() => drawCode(`${key}#${p.port}`, $page.url, `${$page.url}#${$page.loads}`, left, top()));
					},
				})),
			],
		})) as HTMLElement;
		const rect = (el.querySelector('.s-tabbar') as HTMLElement).getBoundingClientRect();
		$bar.bottom = rect.bottom;
		$bar.height = rect.height;
	});
}
