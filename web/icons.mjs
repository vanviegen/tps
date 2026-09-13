/**
 * The app's icon: the robot of robot.ts, written out as the SVG the page and
 * the manifest point at, and rasterized into the PNGs a browser wants for an
 * installed app (`npm run icons`). Nothing in the build depends on it — the
 * files it writes are checked in — so it needs no tools beyond node itself
 * (22.18 or later, which reads the .ts it imports), which is why the
 * rasterizer below is 60 lines of its own.
 *
 * The artwork is the robot in TPS's own colour on nothing, edge to edge: the
 * square around what it draws is the whole canvas, so there is no plate to
 * mask, and the manifest offers the icon as-is rather than as maskable.
 */
import { deflateSync } from 'node:zlib';
import { writeFileSync } from 'node:fs';
import { COLOR, markup, SHAPES, STROKE } from './robot.ts';

// The square around what the robot draws, strokes included, on its own
// canvas: the lamp reaches up to 1 and the keyboard down to 22.5, and the
// same 21.5 across is centred on it.
const SIDE = 21.5, LEFT = 12 - SIDE / 2, TOP = 1;

const svg = [
	`<svg xmlns="http://www.w3.org/2000/svg" viewBox="${LEFT} ${TOP} ${SIDE} ${SIDE}" width="100" height="100" fill="none" stroke="${COLOR}" color="${COLOR}" stroke-width="${STROKE}" stroke-linecap="round" stroke-linejoin="round">`,
	markup(),
	`</svg>`,
].join('\n') + '\n';
writeFileSync(new URL('icon.svg', import.meta.url), svg);

/** Whether the point (x, y), in the robot's own units, is under one of its shapes: a stroke of STROKE, or a dot. */
function inside(s, x, y) {
	const half = STROKE / 2;
	if (s.dot) {
		const [cx, cy, r] = s.dot;
		return (x - cx) ** 2 + (y - cy) ** 2 <= r * r;
	}
	if (s.line) {
		// Within half a stroke of the segment: a capsule, which is a round-capped line.
		const [x1, y1, x2, y2] = s.line, dx = x2 - x1, dy = y2 - y1;
		const t = Math.max(0, Math.min(1, ((x - x1) * dx + (y - y1) * dy) / (dx * dx + dy * dy || 1)));
		return (x - x1 - t * dx) ** 2 + (y - y1 - t * dy) ** 2 <= half * half;
	}
	// Within half a stroke of the outline of a rounded box: its signed distance,
	// which is the distance to the corner circle's centre less its radius
	// outside the box, and the (negative) depth inside it.
	const [rx, ry, w, h, r0] = s.rrect, r = Math.min(r0, w / 2, h / 2);
	const qx = Math.abs(x - rx - w / 2) - (w / 2 - r), qy = Math.abs(y - ry - h / 2) - (h / 2 - r);
	const d = Math.hypot(Math.max(qx, 0), Math.max(qy, 0)) + Math.min(Math.max(qx, qy), 0) - r;
	return Math.abs(d) <= half;
}

const SS = 4; // samples per pixel per axis: the edges are curves, and they show

function render(size) {
	const px = Buffer.alloc(size * size * 4);
	for (let y = 0; y < size; y++) {
		for (let x = 0; x < size; x++) {
			let r = 0, g = 0, b = 0, a = 0;
			for (let sy = 0; sy < SS; sy++) {
				for (let sx = 0; sx < SS; sx++) {
					const ux = LEFT + ((x + (sx + 0.5) / SS) / size) * SIDE, uy = TOP + ((y + (sy + 0.5) / SS) / size) * SIDE;
					if (!SHAPES.some(s => inside(s, ux, uy))) continue;
					r += parseInt(COLOR.slice(1, 3), 16);
					g += parseInt(COLOR.slice(3, 5), 16);
					b += parseInt(COLOR.slice(5, 7), 16);
					a += 255;
				}
			}
			const n = SS * SS, i = (y * size + x) * 4;
			// Premultiplied while averaging, so a half-covered edge keeps its colour.
			px[i] = a ? Math.round(r / (a / 255)) : 0;
			px[i + 1] = a ? Math.round(g / (a / 255)) : 0;
			px[i + 2] = a ? Math.round(b / (a / 255)) : 0;
			px[i + 3] = Math.round(a / n);
		}
	}
	return png(size, px);
}

function png(size, px) {
	const raw = Buffer.alloc((size * 4 + 1) * size);
	for (let y = 0; y < size; y++) {
		raw[y * (size * 4 + 1)] = 0; // filter: none
		px.copy(raw, y * (size * 4 + 1) + 1, y * size * 4, (y + 1) * size * 4);
	}
	const ihdr = Buffer.alloc(13);
	ihdr.writeUInt32BE(size, 0);
	ihdr.writeUInt32BE(size, 4);
	ihdr.set([8, 6, 0, 0, 0], 8); // 8 bits per channel, RGBA, no interlace
	return Buffer.concat([
		Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
		chunk('IHDR', ihdr), chunk('IDAT', deflateSync(raw, { level: 9 })), chunk('IEND', Buffer.alloc(0)),
	]);
}

const CRC = Array.from({ length: 256 }, (_, n) => {
	let c = n;
	for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
	return c >>> 0;
});

function chunk(type, data) {
	const head = Buffer.alloc(4);
	head.writeUInt32BE(data.length, 0);
	const body = Buffer.concat([Buffer.from(type, 'ascii'), data]);
	let crc = 0xffffffff;
	for (const byte of body) crc = CRC[(crc ^ byte) & 0xff] ^ (crc >>> 8);
	const tail = Buffer.alloc(4);
	tail.writeUInt32BE((crc ^ 0xffffffff) >>> 0, 0);
	return Buffer.concat([head, body, tail]);
}

for (const size of [192, 512]) writeFileSync(new URL(`icon-${size}.png`, import.meta.url), render(size));
console.log('web/icon.svg, web/icon-192.png, web/icon-512.png');
