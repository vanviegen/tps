import A from 'aberdeen';
import * as S from 'staffa';
import { externalLink } from 'staffa/icons.js';
import { drawCode } from './code.ts';
import { portUrl } from './util.ts';

/**
 * The pages a task's container serves, as tabs beside VS Code: one for each
 * forwarded port that answers HTTP, loaded the first time its tab is opened,
 * on its front page — or on the page the container asks the user to look at
 * (see daemon/views.go). That request is switched to once its port answers:
 * there and then for one made while the task is on screen, and as it is opened
 * for one made while it was not.
 */

/** The tab each task shows, by `pid/tid`: a port, or '' for VS Code. */
const $shown: Record<string, string> = A.proxy({});
/** The newest view request each task had when last looked at, by `pid/tid`. */
const seenAt: Record<string, number> = {};

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

/** Where this browser reaches a forwarded port's page: the one requested, where it is on that port. */
function pageUrl($t: any, p: any): string {
	return portUrl(p) + ($t.view?.port === p.port ? $t.view.path.slice(1) : '');
}

/**
 * The right column of a task with pages: a tab strip of VS Code (drawn by
 * `code`, given the top its frame starts at) and the pages, or VS Code alone
 * while there are none.
 */
export function drawTaskPages(key: string, $t: any, left: string, code: (top: string) => void): void {
	// A request not switched to yet, until its port answers.
	let pending: number | undefined;
	A(() => {
		const at = $t.view?.at;
		const seen = seenAt[key];
		if (!at) pending = undefined;
		else if (seen === undefined ? A.peek(() => $t.viewNew) : at > seen) pending = at;
		seenAt[key] = Math.max(seen ?? 0, at ?? 0);
		if (pending && livePorts($t).some(p => p.port === $t.view.port)) {
			pending = undefined;
			$shown[key] = String($t.view.port);
		}
	});
	A(() => {
		const list = livePorts($t);
		if (!list.length) return code('0px');
		// The frames are fixed (see code.ts): they start right under the strip.
		const $top = A.proxy({ px: 0 });
		const top = () => `${$top.px}px`;
		// Up against the top of the window, out of the column's padding, as VS Code is.
		const el = A('div', 'margin-top: calc(-1 * $3);', () => S.tabs({
			bind: A.ref($shown, key),
			tabs: [
				{ id: '', label: 'VS Code', content: () => A(() => code(top())) },
				...list.map(p => ({
					id: String(p.port),
					label: () => {
						A('text=', `Port ${p.port}`);
						A('span display:inline-flex ml:$1', 'click=', (e: Event) => { e.stopPropagation(); window.open(pageUrl($t, p), '_blank'); }, () => {
							S.addTooltip({ tip: 'Open in a new browser tab' });
							externalLink({ size: '0.9em' });
						});
					},
					content: () => A(() => {
						const url = pageUrl($t, p);
						drawCode(`${key}#${p.port}`, url, url, left, top());
					}),
				})),
			],
		})) as HTMLElement;
		$top.px = (el.querySelector('.s-tabbar') as HTMLElement).getBoundingClientRect().bottom;
	});
}

/**
 * The page the container asks the user to look at, at the bottom of the chat:
 * a link that switches to its port's tab, or what keeps it from having one.
 */
export function drawViewRequest(key: string, $t: any): void {
	A(() => {
		const v = $t.view;
		if (!v) return;
		const p = ($t.ports ?? []).find((p: any) => p.port === v.port);
		A('div.s-s.primary.tonal p:$2 r:$s-radius-sm overflow-wrap:anywhere', () => {
			A('text=', 'The agent requests you look at ');
			A('a text=', `http://localhost:${v.port}${v.path}`, 'href=', p && pageUrl($t, p), 'click=', (e: MouseEvent) => {
				if (e.ctrlKey || e.metaKey || e.shiftKey) return; // a browser tab of its own
				e.preventDefault();
				if (p?.live) $shown[key] = String(v.port);
			});
			A('text=', !p ? ', which is not available: Containerfile.dev needs an EXPOSE line for that port.'
				: !p.live ? ', which is not available yet: nothing answers HTTP on that port.' : '.');
		});
	});
}
