import A from 'aberdeen';

/**
 * Websocket connection to the TPS server. The server's state tree is mirrored
 * into the reactive $state proxy (via patch messages), which the whole UI
 * renders from. Commands go the other way and resolve with the reply.
 */

export const $state: any = A.proxy({ connected: false, ready: false, projects: {}, hosts: {}, models: [] });

let ws: WebSocket | undefined;
let nextId = 1;
const pending = new Map<number, { resolve: (v: any) => void; reject: (e: Error) => void }>();
const watchCounts = new Map<string, number>();
const chats = new Map<string, any[]>();

/** A task's chat log as a reactive array, filled by the server while watching. */
export function chatLog(pid: string, tid: string): any[] {
	const key = `${pid}/${tid}`;
	let $chat = chats.get(key);
	if (!$chat) chats.set(key, $chat = A.proxy([] as any[]));
	return $chat;
}

function connect(): void {
	ws = new WebSocket(`ws://${location.host}/ws`);
	ws.onopen = () => {
		for (const key of watchCounts.keys()) sendRaw({ watch: key, on: true });
	};
	ws.onmessage = e => {
		const msg = JSON.parse(e.data);
		if (msg.hello) {
			for (const [key, value] of Object.entries(msg.hello)) $state[key] = value;
			$state.connected = true;
		} else if (msg.p) {
			applyPatch(msg.p, msg.del ? undefined : msg.v);
		} else if (msg.re !== undefined) {
			const p = pending.get(msg.re);
			pending.delete(msg.re);
			if (msg.error) p?.reject(new Error(msg.error));
			else p?.resolve(msg.result);
		} else if (msg.c !== undefined) {
			const [pid, tid] = msg.c.split('/');
			const $chat = chatLog(pid, tid);
			if (msg.es) {
				$chat.splice(0, $chat.length, ...msg.es);
			} else if (msg.u) { // update: replace the newest entry with the same id
				const i = $chat.findLastIndex(e => e.id === msg.e.id);
				if (i >= 0) $chat[i] = msg.e;
				else $chat.push(msg.e);
			} else {
				$chat.push(msg.e);
			}
		}
	};
	ws.onclose = () => {
		$state.connected = false;
		for (const p of pending.values()) p.reject(new Error('Connection lost'));
		pending.clear();
		setTimeout(connect, 1000);
	};
	ws.onerror = () => ws?.close();
}
connect();

function sendRaw(msg: object): void {
	if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg));
}

function applyPatch(path: (string | number)[], value: unknown): void {
	let obj = $state;
	for (const key of path.slice(0, -1)) obj = obj[key] ??= {};
	const last = path[path.length - 1];
	if (value === undefined) delete obj[last];
	else obj[last] = value;
}

function whenConnected(timeoutMs = 8000): Promise<void> {
	return new Promise((resolve, reject) => {
		if (ws?.readyState === WebSocket.OPEN) return resolve();
		const start = Date.now();
		const timer = setInterval(() => {
			if (ws?.readyState === WebSocket.OPEN) {
				clearInterval(timer);
				resolve();
			} else if (Date.now() - start > timeoutMs) {
				clearInterval(timer);
				reject(new Error('Not connected to the TPS server'));
			}
		}, 100);
	});
}

export async function send(cmd: string, args?: object): Promise<any> {
	await whenConnected();
	return new Promise((resolve, reject) => {
		const id = nextId++;
		pending.set(id, { resolve, reject });
		ws!.send(JSON.stringify({ id, cmd, args }));
	});
}

/**
 * Watches are counted per key, and the server hears only of the first and
 * the last: a key is a task (`pid/tid`), whose chat then streams and whose
 * workspace, VS Code included, is kept up — or `pid/-`, the project's own
 * checkout in VS Code. They are placed again whenever the connection comes
 * back (see connect).
 */
export function addWatch(key: string): void {
	const count = watchCounts.get(key) ?? 0;
	watchCounts.set(key, count + 1);
	if (!count) sendRaw({ watch: key, on: true });
}

export function dropWatch(key: string): void {
	const left = (watchCounts.get(key) ?? 1) - 1;
	if (left > 0) {
		watchCounts.set(key, left);
	} else {
		watchCounts.delete(key);
		sendRaw({ watch: key, on: false });
	}
}

/** Watch a task for as long as the calling reactive scope lives. */
export function watchTask(pid: string, tid: string): void {
	const key = `${pid}/${tid}`;
	addWatch(key);
	A.clean(() => dropWatch(key));
}
