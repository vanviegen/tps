import A from 'aberdeen';
import * as S from 'staffa';
import { x } from 'staffa/icons.js';

/**
 * Attachments of the message being composed: images pasted into a task's chat
 * input. Until the message is sent they live here — the composer is redrawn
 * whenever the task changes, and leaving the task and coming back must not
 * throw them away — and the message refers to each by the path the agent will
 * read it at. Sending hands them to the daemon, which writes them there.
 */
export type Attachment = {
	name: string;
	url: string; // object URL, for the thumbnail; released when the attachment goes
	data: string; // base64 of the bytes, which is how it travels
};

/** Where the daemon puts a task's attachments in its container (see daemon/uploads.go). */
const UPLOADS = '/uploads';

/** One attachment's path, which is the reference the message carries. */
export function uploadPath(name: string): string {
	return `${UPLOADS}/${name}`;
}

// As much as the daemon takes of one file, and of a message's files together
// (see uploads.go); beyond either, the paste is refused here, where there is
// somewhere to say so.
const MAX_BYTES = 8 << 20;
const MAX_TOTAL_BYTES = 10 << 20;

const composers = new Map<string, Attachment[]>();

/** The attachments waiting in this task's composer, as a reactive array. */
export function attachments(pid: string, tid: string): Attachment[] {
	const key = `${pid}/${tid}`;
	let $atts = composers.get(key);
	if (!$atts) composers.set(key, $atts = A.proxy([] as Attachment[]));
	return $atts;
}

/**
 * Take on the images of a paste (or a drop), one attachment each, and answer
 * with the references the message should carry — in the order they were given,
 * and skipping whatever could not be taken.
 */
export async function addFiles(pid: string, tid: string, files: File[]): Promise<string[]> {
	const $atts = attachments(pid, tid);
	const refs: string[] = [];
	let total = A.peek(() => $atts.reduce((sum, a) => sum + bytes(a.data), 0));
	for (const file of files) {
		if (file.size > MAX_BYTES) {
			S.toast({ title: file.name || 'Attachment', message: `Larger than ${MAX_BYTES >> 20} MB`, type: 'danger' });
			continue;
		}
		if (total + file.size > MAX_TOTAL_BYTES) {
			S.toast({ title: file.name || 'Attachment', message: `One message carries no more than ${MAX_TOTAL_BYTES >> 20} MB`, type: 'danger' });
			continue;
		}
		let data: string;
		try {
			data = base64(await file.arrayBuffer());
		} catch (e) {
			S.toast({ title: file.name || 'Attachment', message: (e as Error).message, type: 'danger' });
			continue;
		}
		const name = freeName(A.peek(() => $atts.map(a => a.name)), fileName(file));
		$atts.push({ name, url: URL.createObjectURL(file), data });
		refs.push(uploadPath(name));
		total += file.size;
	}
	return refs;
}

/** Drop one attachment again, before the message it was for was sent. */
export function dropAttachment(pid: string, tid: string, name: string): void {
	const $atts = attachments(pid, tid);
	const i = A.peek(() => $atts.findIndex(a => a.name === name));
	if (i < 0) return;
	URL.revokeObjectURL(A.peek(() => $atts[i].url));
	$atts.splice(i, 1);
}

/** The attachments to send with the message, taken out of the composer. */
export function takeAttachments(pid: string, tid: string): { name: string; data: string }[] {
	const $atts = attachments(pid, tid);
	const taken = A.peek(() => $atts.map(a => ({ name: a.name, data: a.data, url: a.url })));
	for (const a of taken) URL.revokeObjectURL(a.url);
	$atts.splice(0, $atts.length);
	return taken.map(({ name, data }) => ({ name, data }));
}

/** The images among what was pasted or dropped; anything else is left alone. */
export function imageFiles(data: DataTransfer | null): File[] {
	return [...(data?.files ?? [])].filter(f => f.type.startsWith('image/'));
}

const thumb = A.insertCss({
	'&': 'position:relative w:5rem h:5rem r:$s-radius-sm overflow:hidden border: 1px solid $s-faint',
	img: 'w:100% h:100% object-fit:cover display:block',
	// The delete button sits on the image, on a scrim so it reads on any of them.
	'.del': 'position:absolute top:0 right:0 background:rgba(0,0,0,0.55) r:0 border-bottom-left-radius:$s-radius-sm',
});

/**
 * The strip of what is attached to the message being composed: a thumbnail
 * each, with the button that takes it back off again.
 */
export function drawAttachments(pid: string, tid: string, onRemove: (name: string) => void): void {
	const $atts = attachments(pid, tid);
	A(() => {
		if (!$atts.length) return;
		A('div display:flex flex-wrap:wrap gap:$2', () => {
			A.onEach($atts, ($a: any) => {
				A('div', thumb, () => {
					S.addTooltip({ tip: uploadPath($a.name) });
					A('img', 'src=', $a.url, 'alt=', $a.name);
					S.iconButton({ icon: x, ariaLabel: `Remove ${$a.name}`, attrs: '.small .neutral .del', click: () => onRemove($a.name) });
				});
			});
		});
	});
}

/**
 * The name to keep the file under: the one it came with, or — a pasted
 * screenshot arrives without much of one — what its type says it is. Folded
 * the way the daemon folds it (see uploadName), so that the path written into
 * the message is the path the file ends up at.
 */
function fileName(file: File): string {
	if (!file.name) return `image.${(file.type.split('/')[1] || 'bin').split('+')[0]}`;
	const name = file.name.trim().split(/[\\/]/).pop()!.replace(/[^A-Za-z0-9._-]+/g, '-').replace(/^[-.]+|[-.]+$/g, '');
	return name.slice(0, 80) || 'file';
}

/**
 * A name no other attachment of this message has: image.png becomes image-2.png,
 * and image-2.png image-3.png — the daemon settles it the same way against the
 * files the task already has (see freeUploadName), and corrects the message
 * where its answer differs.
 */
function freeName(taken: string[], name: string): string {
	if (!taken.includes(name)) return name;
	const dot = name.lastIndexOf('.');
	let stem = dot > 0 ? name.slice(0, dot) : name;
	const ext = dot > 0 ? name.slice(dot) : '';
	let n = 2;
	const m = /-(\d+)$/.exec(stem);
	if (m) {
		stem = stem.slice(0, -m[0].length);
		n = Number(m[1]) + 1;
	}
	while (taken.includes(`${stem}-${n}${ext}`)) n++;
	return `${stem}-${n}${ext}`;
}

/** What base64 of this length weighs in bytes. */
function bytes(data: string): number {
	return Math.floor(data.length * 3 / 4);
}

/** Bytes as base64, in chunks small enough to pass as arguments. */
function base64(buffer: ArrayBuffer): string {
	const view = new Uint8Array(buffer);
	let binary = '';
	for (let i = 0; i < view.length; i += 0x8000) {
		binary += String.fromCharCode(...view.subarray(i, i + 0x8000));
	}
	return btoa(binary);
}
