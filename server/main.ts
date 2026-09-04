#!/usr/bin/env node
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { readFile } from 'node:fs/promises';
import { extname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';
import { TOOL_OPTIONS } from './containerfile.ts';
import { Hub } from './hub.ts';
import { ProjectManager } from './project.ts';
import { proxyRequest, proxyUpgrade } from './proxy.ts';
import { log } from './util.ts';

const { values: args } = parseArgs({
	options: {
		port: { type: 'string', short: 'p', default: '4820' },
		host: { type: 'string', default: '127.0.0.1' },
		help: { type: 'boolean', short: 'h' },
	},
});
if (args.help) {
	console.log('Usage: tps [--port 4820] [--host 127.0.0.1]');
	process.exit(0);
}

const webDir = fileURLToPath(new URL('../web', import.meta.url));
const hub = new Hub();
hub.state.tools = TOOL_OPTIONS.map(({ id, label }) => ({ id, label }));
const manager = new ProjectManager(hub);

hub.cmds = {
	addProject: ({ dir, name }) => manager.add(dir, name),
	removeProject: ({ pid }) => manager.remove(pid),
	setProject: ({ pid, ...config }) => manager.getProject(pid).setConfig(config),
	createContainerfile: ({ pid, tools }) => manager.getProject(pid).createContainerfile(tools ?? []),
	createTask: async ({ pid }) => ({ tid: await manager.getProject(pid).createTask() }),
	updateTask: ({ pid, tid, ...partial }) => manager.getTask(pid, tid).update(partial),
	openTask: ({ pid, tid }) => manager.getTask(pid, tid).open(),
	assignTask: ({ pid, tid, to }) => manager.getTask(pid, tid).assign(to === 'human' ? 'human' : 'agent'),
	chat: ({ pid, tid, text }) => manager.getTask(pid, tid).sendChat(String(text ?? '')),
	stopAgent: ({ pid, tid }) => manager.getTask(pid, tid).stopAgent(),
	mergeTask: ({ pid, tid, message }) => manager.getTask(pid, tid).merge(String(message ?? '')),
	moveTask: ({ pid, tid, phase }) => manager.getTask(pid, tid).moveTo(phase),
	deleteTask: ({ pid, tid }) => manager.getTask(pid, tid).delete(),
};

const MIME: Record<string, string> = {
	'.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.map': 'application/json',
	'.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2', '.ico': 'image/x-icon',
};

async function serveStatic(res: ServerResponse, pathname: string): Promise<void> {
	if (pathname.includes('..')) { res.writeHead(400); res.end(); return; }
	if (pathname === '/' || !extname(pathname)) pathname = '/index.html'; // SPA fallback
	try {
		const content = await readFile(join(webDir, pathname));
		res.writeHead(200, { 'content-type': MIME[extname(pathname)] ?? 'application/octet-stream' });
		res.end(content);
	} catch {
		res.writeHead(404);
		res.end('Not found');
	}
}

/** Match /code/<pid>/<tid>/... and return the task's upstream port + path. */
function codeRoute(url: URL): { port?: number; path: string } | undefined {
	const m = url.pathname.match(/^\/code\/([^/]+)\/([^/]+)(\/.*)?$/);
	if (!m) return undefined;
	try {
		const task = manager.getTask(m[1], m[2]);
		return { port: task.container?.codePort, path: (m[3] || '/') + url.search };
	} catch {
		return { path: '/' };
	}
}

const server = createServer((req: IncomingMessage, res: ServerResponse) => {
	const url = new URL(req.url || '/', 'http://localhost');
	const route = codeRoute(url);
	if (route) {
		if (route.port) proxyRequest(req, res, route.port, route.path);
		else {
			res.writeHead(503, { 'content-type': 'text/html' });
			res.end('<meta http-equiv="refresh" content="2"><body style="font-family:sans-serif;color:#888">Workspace is not running&hellip;</body>');
		}
		return;
	}
	void serveStatic(res, url.pathname);
});

server.on('upgrade', (req, socket, head) => {
	const url = new URL(req.url || '/', 'http://localhost');
	if (url.pathname === '/ws') return hub.handleUpgrade(req, socket, head);
	const route = codeRoute(url);
	if (route?.port) proxyUpgrade(req, socket, head, route.port, route.path);
	else socket.destroy();
});

let shuttingDown = false;
async function shutdown(): Promise<void> {
	if (shuttingDown) process.exit(1);
	shuttingDown = true;
	log('Shutting down: stopping active workspaces…');
	setTimeout(() => { log('Shutdown timed out'); process.exit(1); }, 60_000).unref();
	await manager.shutdown().catch(e => log('Shutdown error:', e));
	process.exit(0);
}
process.on('SIGINT', () => void shutdown());
process.on('SIGTERM', () => void shutdown());
process.on('unhandledRejection', e => log('Unhandled rejection:', e));

await manager.start();
const port = parseInt(args.port!);
server.listen(port, args.host, () => log(`TPS listening on http://${args.host}:${port}/`));
