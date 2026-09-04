import type { IncomingMessage } from 'node:http';
import type { Duplex } from 'node:stream';
import { WebSocketServer, type WebSocket } from 'ws';
import { log } from './util.ts';

/**
 * Live state hub: keeps the state tree all clients mirror, broadcasts changes
 * as path patches, dispatches client commands, and fans out task chat logs
 * (with recent history) to whoever is watching a task.
 */

type CmdHandler = (args: any) => Promise<unknown> | unknown;

interface Client {
	ws: WebSocket;
	watches: Set<string>;
}

const CHAT_KEEP = 500; // entries held in memory (and replayed to watchers) per task

export class Hub {
	state: any = { projects: {}, models: ['sonnet', 'opus', 'haiku'] };
	cmds: Record<string, CmdHandler> = {};
	/** Called when the number of watchers of a task key (pid/tid) changes. */
	onWatch?: (key: string, count: number) => void;

	private wss = new WebSocketServer({ noServer: true });
	private clients = new Set<Client>();
	private chats = new Map<string, unknown[]>();

	constructor() {
		this.wss.on('connection', ws => this.onConnection(ws));
	}

	handleUpgrade(req: IncomingMessage, socket: Duplex, head: Buffer): void {
		this.wss.handleUpgrade(req, socket, head, ws => this.wss.emit('connection', ws, req));
	}

	/** Update the state tree and broadcast; `undefined` deletes. */
	set(path: (string | number)[], value: unknown): void {
		let obj = this.state;
		for (const key of path.slice(0, -1)) obj = obj[key] ??= {};
		const last = path[path.length - 1];
		if (value === undefined) {
			if (!(last in obj)) return;
			delete obj[last];
			this.broadcast({ p: path, del: true });
		} else {
			if (JSON.stringify(obj[last]) === JSON.stringify(value)) return;
			obj[last] = value;
			this.broadcast({ p: path, v: value });
		}
	}

	get(path: (string | number)[]): any {
		let obj = this.state;
		for (const key of path) {
			if (obj == null) return undefined;
			obj = obj[key];
		}
		return obj;
	}

	/** Append a chat entry for a task and stream it to its watchers. */
	chat(key: string, entry: unknown): void {
		let log = this.chats.get(key);
		if (!log) this.chats.set(key, log = []);
		log.push(entry);
		if (log.length > CHAT_KEEP) log.splice(0, log.length - CHAT_KEEP);
		for (const client of this.clients) {
			if (client.watches.has(key)) this.sendTo(client.ws, { c: key, e: entry });
		}
	}

	/** Re-send a changed entry; it replaces the newest entry with the same id. */
	chatUpdate(key: string, entry: { id?: string }): void {
		const log = this.chats.get(key) ?? [];
		const i = log.findLastIndex(e => (e as any).id === entry.id);
		if (i >= 0) log[i] = entry;
		else return this.chat(key, entry);
		for (const client of this.clients) {
			if (client.watches.has(key)) this.sendTo(client.ws, { c: key, e: entry, u: true });
		}
	}

	/** Replace a task's chat log (initial load from disk, or a discard). */
	setChat(key: string, entries: unknown[]): void {
		this.chats.set(key, entries.slice(-CHAT_KEEP));
		for (const client of this.clients) {
			if (client.watches.has(key)) this.sendTo(client.ws, { c: key, es: this.chats.get(key) });
		}
	}

	watcherCount(key: string): number {
		let n = 0;
		for (const client of this.clients) if (client.watches.has(key)) n++;
		return n;
	}

	private broadcast(msg: unknown): void {
		const json = JSON.stringify(msg);
		for (const client of this.clients) {
			if (client.ws.readyState === client.ws.OPEN) client.ws.send(json);
		}
	}

	private sendTo(ws: WebSocket, msg: unknown): void {
		if (ws.readyState === ws.OPEN) ws.send(JSON.stringify(msg));
	}

	private onConnection(ws: WebSocket): void {
		const client: Client = { ws, watches: new Set() };
		this.clients.add(client);
		this.sendTo(ws, { hello: this.state });
		ws.on('message', async raw => {
			let msg: any;
			try { msg = JSON.parse(raw.toString()); } catch { return; }
			if (msg.cmd !== undefined) {
				try {
					const handler = this.cmds[msg.cmd];
					if (!handler) throw new Error(`Unknown command: ${msg.cmd}`);
					const result = await handler(msg.args ?? {});
					this.sendTo(ws, { re: msg.id, result: result ?? null });
				} catch (e) {
					log(`cmd ${msg.cmd} failed:`, e);
					this.sendTo(ws, { re: msg.id, error: (e as Error).message });
				}
			} else if (typeof msg.watch === 'string') {
				if (msg.on) {
					client.watches.add(msg.watch);
					this.sendTo(ws, { c: msg.watch, es: this.chats.get(msg.watch) ?? [] });
				} else {
					client.watches.delete(msg.watch);
				}
				this.onWatch?.(msg.watch, this.watcherCount(msg.watch));
			}
		});
		ws.on('close', () => {
			this.clients.delete(client);
			for (const key of client.watches) this.onWatch?.(key, this.watcherCount(key));
		});
	}
}
