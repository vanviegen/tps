/**
 * The app's icon: one description of the artwork, written out as the SVG the
 * page and the manifest point at, and rasterized into the PNGs a browser
 * wants for an installed app (`npm run icons`). Nothing in the build depends
 * on it — the files it writes are checked in — so it needs no tools beyond
 * node itself, which is why the rasterizer below is 60 lines of its own.
 *
 * The artwork is TPS's robot on the blue of the first project colour, drawn
 * full bleed with rounded corners: an installed icon is masked to whatever
 * shape the system likes, so the background reaches the edge and everything
 * that has to be seen stays well inside the middle (the "safe zone").
 */
import { deflateSync } from 'node:zlib';
import { writeFileSync } from 'node:fs';

const BG = '#5b9cf5', FG = '#ffffff';
// On a 100x100 canvas: the plate, then the robot in white, with the blue
// showing through its eyes and mouth.
const SHAPES = [
	{ rect: [0, 0, 100, 100], r: 22, fill: BG },
	{ rect: [48.5, 16, 3, 10], r: 1.5, fill: FG },   // antenna
	{ circle: [50, 14, 4], fill: FG },
	{ rect: [21, 40, 5, 14], r: 2.5, fill: FG },     // ears
	{ rect: [74, 40, 5, 14], r: 2.5, fill: FG },
	{ rect: [27, 26, 46, 42], r: 12, fill: FG },     // head
	{ circle: [40, 44, 4.5], fill: BG },             // eyes
	{ circle: [60, 44, 4.5], fill: BG },
	{ rect: [39, 55, 22, 5], r: 2.5, fill: BG },     // mouth
	{ rect: [36, 72, 6, 12], r: 3, fill: FG },       // legs
	{ rect: [58, 72, 6, 12], r: 3, fill: FG },
];

const svg = [
	`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100" width="100" height="100">`,
	...SHAPES.map(s => s.circle
		? `<circle cx="${s.circle[0]}" cy="${s.circle[1]}" r="${s.circle[2]}" fill="${s.fill}"/>`
		: `<rect x="${s.rect[0]}" y="${s.rect[1]}" width="${s.rect[2]}" height="${s.rect[3]}" rx="${s.r}" fill="${s.fill}"/>`),
	`</svg>`,
].join('\n') + '\n';
writeFileSync(new URL('icon.svg', import.meta.url), svg);

/** How much of the point (x, y) — in canvas units — a shape covers: 1 inside, 0 outside. */
function inside(s, x, y) {
	if (s.circle) {
		const [cx, cy, r] = s.circle;
		return (x - cx) ** 2 + (y - cy) ** 2 <= r * r;
	}
	const [rx, ry, w, h] = s.rect, r = Math.min(s.r ?? 0, w / 2, h / 2);
	if (x < rx || y < ry || x > rx + w || y > ry + h) return false;
	// Outside a rounded corner is the only way to be inside the box and out of the shape.
	const dx = Math.max(rx + r - x, x - (rx + w - r), 0);
	const dy = Math.max(ry + r - y, y - (ry + h - r), 0);
	return dx * dx + dy * dy <= r * r;
}

const SS = 4; // samples per pixel per axis: the edges are curves, and they show

function render(size) {
	const px = Buffer.alloc(size * size * 4);
	for (let y = 0; y < size; y++) {
		for (let x = 0; x < size; x++) {
			let r = 0, g = 0, b = 0, a = 0;
			for (let sy = 0; sy < SS; sy++) {
				for (let sx = 0; sx < SS; sx++) {
					const cx = ((x + (sx + 0.5) / SS) / size) * 100, cy = ((y + (sy + 0.5) / SS) / size) * 100;
					let hit;
					for (const s of SHAPES) if (inside(s, cx, cy)) hit = s.fill;
					if (!hit) continue;
					r += parseInt(hit.slice(1, 3), 16);
					g += parseInt(hit.slice(3, 5), 16);
					b += parseInt(hit.slice(5, 7), 16);
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
