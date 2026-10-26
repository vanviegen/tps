import A from 'aberdeen';
import * as S from 'staffa';
import { file as fileGlyph, paperclip, x } from 'staffa/icons.js';
import { cmd } from './util.ts';

/**
 * Attachments of the message being composed: files pasted, dropped or picked
 * into a task's chat input. They are stored with the task the moment they are
 * picked, the message itself being kept with the task unsent (see setDraftL
 * in daemon/task.go), and the message refers to each by the path the agent
 * reads it at — its text is the whole record of them.
 */

/** Where the daemon puts a task's attachments in its container (see daemon/uploads.go). */
const UPLOADS = '/uploads';

/** One attachment's path, which is the reference the message carries. */
function uploadPath(name: string): string {
	return `${UPLOADS}/${name}`;
}

// As much as the daemon takes of one file, and of the files that go over
// together (see uploads.go); beyond either, the file is refused here, where
// there is somewhere to say so — and before a batch too big for the websocket
// to carry breaks the connection instead.
const MAX_BYTES = 8 << 20;
const MAX_TOTAL_BYTES = 10 << 20;

/**
 * Store files with the task right away, and answer with the paths they are
 * read at, for the message to carry.
 */
export async function uploadFiles(pid: string, tid: string, files: File[]): Promise<string[]> {
	const carried = (await Promise.all(fitting(files).map(carry))).filter(f => f !== undefined);
	return (await cmd('attach', { pid, tid, files: carried })) || [];
}

/**
 * The files that fit, with a word about each that does not — one too big, or
 * one that would tip the batch over what goes at once.
 */
function fitting(files: File[]): File[] {
	const fits: File[] = [];
	let taken = 0;
	for (const file of files) {
		if (file.size > MAX_BYTES) {
			S.toast({ title: file.name || 'Attachment', message: `Larger than ${MAX_BYTES >> 20} MB`, type: 'danger' });
		} else if (taken + file.size > MAX_TOTAL_BYTES) {
			S.toast({ title: file.name || 'Attachment', message: `No more than ${MAX_TOTAL_BYTES >> 20} MB can be attached at once`, type: 'danger' });
		} else {
			fits.push(file);
			taken += file.size;
		}
	}
	return fits;
}

/**
 * One file as it travels: its name and its bytes — or nothing at all, with a
 * word about why, when it cannot be read (a folder dropped on the field, say).
 */
async function carry(file: File): Promise<{ name: string; data: string } | undefined> {
	try {
		return { name: fileName(file), data: base64(await file.arrayBuffer()) };
	} catch (e) {
		S.toast({ title: file.name || 'Attachment', message: (e as Error).message, type: 'danger' });
	}
}

/** How a field takes files on, answering with the paths its text should carry. */
type Take = (files: File[]) => Promise<string[]>;

const over = A.insertCss({ '&': 'outline: 2px dashed $s-primary; outline-offset:-2px r:$s-radius' });

/**
 * A file dropped anywhere on `zone`, or pasted while the focus is in it, goes
 * to the field. The path it lands at is put in where the cursor is, that being
 * all the text can say about a file.
 */
export function acceptFiles(zone: HTMLElement, area: () => HTMLTextAreaElement, take: Take): void {
	const mark = (on: boolean) => zone.classList.toggle(over.slice(1), on);
	zone.addEventListener('paste', (e: ClipboardEvent) => {
		const files = [...e.clipboardData?.files ?? []];
		if (!files.length) return; // a plain paste is the browser's to handle
		e.preventDefault();
		void putFiles(area, take, files);
	});
	// Without this the browser leaves the page for the file that was dropped on
	// it; a card dragged past is the board's business, and passes through.
	zone.addEventListener('dragover', (e: DragEvent) => {
		if (!e.dataTransfer?.types.includes('Files')) return;
		e.preventDefault();
		mark(true);
	});
	// Crossing from one element inside the zone to another is not leaving it.
	zone.addEventListener('dragleave', (e: DragEvent) => {
		if (!zone.contains(e.relatedTarget as Node)) mark(false);
	});
	zone.addEventListener('drop', (e: DragEvent) => {
		if (!e.dataTransfer?.types.includes('Files')) return;
		e.preventDefault();
		mark(false);
		void putFiles(area, take, [...e.dataTransfer.files]);
	});
}

/** The paperclip in a field's corner: the way in for a file that is not on the clipboard. */
export function attachButton(area: () => HTMLTextAreaElement, take: Take): void {
	const picker = A('input', 'type=file', 'multiple=true', 'display:none',
		'change=', () => {
			void putFiles(area, take, [...picker.files ?? []]);
			picker.value = ''; // so that picking the same file again is a change again
		}) as HTMLInputElement;
	S.iconButton({ icon: paperclip, ariaLabel: 'Attach files', tooltip: 'Attach files', attrs: 'fg:$s-muted', click: () => picker.click() });
}

async function putFiles(area: () => HTMLTextAreaElement, take: Take, files: File[]): Promise<void> {
	for (const ref of await take(files)) insertRef(area(), ref);
}

/** Put an attachment's path where the cursor is, kept apart from the words around it. */
function insertRef(el: HTMLTextAreaElement, ref: string): void {
	const at = el.selectionStart ?? el.value.length;
	const before = el.value.slice(0, at);
	const after = el.value.slice(el.selectionEnd ?? at);
	const lead = before && !/\s$/.test(before) ? ' ' : '';
	const trail = /^\s/.test(after) ? '' : ' '; // also at the very end: what is typed next is a new word
	el.value = before + lead + ref + trail + after;
	const pos = before.length + lead.length + ref.length + trail.length;
	el.setSelectionRange(pos, pos);
	el.focus();
	el.dispatchEvent(new Event('input')); // grow with it, and remember the draft
}

/** Take the path of a removed attachment back out, with the space it came with. */
export function removeRef(el: HTMLTextAreaElement, ref: string): void {
	const at = el.value.indexOf(ref);
	if (at < 0) return;
	let end = at + ref.length;
	let start = at;
	if (el.value[end] === ' ') end++;
	else if (start > 0 && el.value[start - 1] === ' ') start--;
	el.value = el.value.slice(0, start) + el.value.slice(end);
	el.setSelectionRange(start, start);
	el.dispatchEvent(new Event('input'));
}

const thumb = A.insertCss({
	'&': 'position:relative w:5rem h:5rem r:$s-radius-sm overflow:hidden border: 1px solid $s-faint',
	img: 'w:100% h:100% object-fit:cover display:block',
	// What has no thumbnail wears a glyph and as much of its name as fits.
	'.doc': 'w:100% h:100% display:flex flex-direction:column align-items:center justify-content:center gap:$1 p:$1 fg:$s-muted',
	'.doc span': 'max-width:100% overflow:hidden text-overflow:ellipsis white-space:nowrap font-size:0.7em',
	// The delete button sits on the image, on a scrim so it reads on any of them.
	'.del': 'position:absolute top:0 right:0 background:rgba(0,0,0,0.55) r:0 border-bottom-left-radius:$s-radius-sm',
});

/**
 * The strip under the composer: the attachments its text points at, a
 * thumbnail each — or, for what is not an image, its name — with the button
 * that takes it back off again, which takes its path back out of the text.
 */
export function drawRefs(pid: string, tid: string, text: () => string, onRemove: (ref: string) => void): void {
	// The paths are derived into a line of their own — none of them holds a
	// space — so that the strip is redrawn when what the text points at changes
	// rather than on every keystroke.
	const $refs = A.proxy({ value: '' });
	A(() => { $refs.value = (text().match(refPattern) ?? []).join(' '); });
	A(() => {
		if (!$refs.value) return;
		A('div display:flex flex-wrap:wrap gap:$2', () => {
			for (const ref of $refs.value.split(' ')) {
				const name = ref.slice(UPLOADS.length + 1);
				drawTile(name, () => preview(pid, tid, name), () => onRemove(ref));
			}
		});
	});
}

const previews = new Map<string, { value: string }>();

/**
 * The thumbnail of an attachment stored with the task, which this browser has
 * no bytes of: asked for the first time it is shown, and kept for the page's
 * life — the strip is drawn again whenever what the text points at changes,
 * and an attachment never changes under its name. Empty until it arrives, and
 * for what is no image.
 */
function preview(pid: string, tid: string, name: string): string {
	const key = `${pid}/${tid}/${name}`;
	if (!previews.has(key)) {
		const $url = A.proxy({ value: '' });
		previews.set(key, $url);
		void cmd('preview', { pid, tid, name }).then(url => {
			if (typeof url === 'string') $url.value = url;
		});
	}
	return previews.get(key)!.value;
}

// An attachment path where a word begins, which is how one is written into the
// text (see insertRef): a path that merely ends in one, `web/uploads/index.ts`,
// is about the project rather than about an attachment.
const refPattern = new RegExp(`(?<=^|\\s)${UPLOADS}/[A-Za-z0-9._-]+`, 'g');

/** One attachment in a strip: its thumbnail, or its name while there is none to show. */
function drawTile(name: string, url: () => string, onRemove: () => void): void {
	A('div', thumb, () => {
		S.addTooltip({ tip: uploadPath(name) });
		A(() => {
			const src = url();
			if (src) A('img', 'src=', src, 'alt=', name);
			else A('div.doc', () => {
				fileGlyph({ size: '1.6rem' });
				A('span text=', name);
			});
		});
		S.iconButton({ icon: x, ariaLabel: `Remove ${name}`, attrs: '.small .neutral .del', click: onRemove });
	});
}

/**
 * The name to keep the file under: the one it came with, or — a pasted
 * screenshot arrives without much of one — what its type says it is. The
 * daemon makes it safe and free (see uploadName), and answers with the path it
 * ends up at.
 */
function fileName(file: File): string {
	if (file.name) return file.name;
	const ext = (file.type.split('/')[1] || 'bin').split('+')[0];
	return `${file.type.startsWith('image/') ? 'image' : 'file'}.${ext}`;
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
