import A from 'aberdeen';
import * as S from 'staffa';
import { globe } from 'staffa/icons.js';
import { send } from './conn.ts';

export const PHASES = ['plan', 'implement', 'review', 'done'] as const;
export type Phase = typeof PHASES[number];

export const PHASE_LABELS: Record<Phase, string> = {
	plan: 'Plan', implement: 'Implement', review: 'Review', done: 'Done',
};

/** What the task's terminal/agent is up to right now, as text plus a color role. */
export function taskActivity($t: any): { text: string; color: string } {
	switch ($t.agent) {
		case 'planner': return { text: 'claude: refining the plan', color: 'accent' };
		case 'implementer': return { text: 'claude: implementing', color: 'accent' };
		case 'reviewer': return { text: 'claude: reviewing', color: 'accent' };
		case 'merge': return { text: 'merging into the target branch', color: 'accent' };
	}
	switch ($t.status) {
		case 'building': return { text: 'podman: building the image', color: 'warning' };
		case 'starting': return { text: $t.statusDetail || 'starting', color: 'warning' };
		case 'stopping': return { text: 'shutting down', color: 'warning' };
		case 'up': return { text: 'idle', color: 'success' };
		case 'error': return { text: 'error', color: 'danger' };
		default: return { text: 'workspace down', color: 'muted' };
	}
}

/** A globe link, shown while something inside the container answers on its $PORT. */
export function drawLiveLink($t: any): void {
	A(() => {
		if (!$t.live || !$t.appPort) return;
		A('a.tps-live title=Open the page served by this task',
			'href=', `http://${location.hostname}:${$t.appPort}/`, 'target=_blank',
			'click=', (e: Event) => e.stopPropagation(),
			() => globe({ size: '1.1em' }));
	});
}

/** Run a server command; on failure show a toast and return undefined. */
export async function cmd(name: string, args?: object): Promise<any> {
	try {
		return await send(name, args) ?? true;
	} catch (e) {
		S.toast({ title: name, message: (e as Error).message, type: 'danger', duration: 10_000 });
		return undefined;
	}
}
