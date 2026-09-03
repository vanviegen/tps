import type { IncomingMessage, ServerResponse } from 'node:http';
import { request } from 'node:http';
import { connect } from 'node:net';
import type { Duplex } from 'node:stream';

/**
 * Minimal reverse proxy used to serve each task's code-server (in its
 * container, published on an ephemeral localhost port) under
 * /code/<pid>/<tid>/ of the TPS server, iframe-embeddable. Handles regular
 * requests and websocket upgrades; the prefix is stripped, which is the setup
 * code-server supports.
 */

export function proxyRequest(req: IncomingMessage, res: ServerResponse, port: number, path: string): void {
	// The Host header is forwarded unchanged: code-server requires it to match
	// the browser's Origin, or it rejects websocket upgrades.
	const upstream = request({
		host: '127.0.0.1', port, path, method: req.method,
		headers: req.headers,
	}, upstreamRes => {
		res.writeHead(upstreamRes.statusCode ?? 502, upstreamRes.headers);
		upstreamRes.pipe(res);
	});
	upstream.on('error', () => {
		if (!res.headersSent) res.writeHead(502, { 'content-type': 'text/plain' });
		res.end('TPS: workspace upstream unavailable');
	});
	req.pipe(upstream);
}

export function proxyUpgrade(req: IncomingMessage, socket: Duplex, head: Buffer, port: number, path: string): void {
	const upstream = connect(port, '127.0.0.1', () => {
		let raw = `${req.method} ${path} HTTP/1.1\r\n`;
		for (let i = 0; i < req.rawHeaders.length; i += 2) {
			raw += `${req.rawHeaders[i]}: ${req.rawHeaders[i + 1]}\r\n`;
		}
		upstream.write(raw + '\r\n');
		if (head.length) upstream.write(head);
		socket.pipe(upstream);
		upstream.pipe(socket);
	});
	const abort = () => { socket.destroy(); upstream.destroy(); };
	upstream.on('error', abort);
	socket.on('error', abort);
}
