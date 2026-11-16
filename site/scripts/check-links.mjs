// Fails the build when a page links to a path or #fragment that the built site does not contain.
import { readFileSync, readdirSync, existsSync } from 'node:fs';
import { join, relative, dirname } from 'node:path';

const dist = new URL('../dist/', import.meta.url).pathname;
const base = '/gitlab-reviewer-cli/';

const pages = readdirSync(dist, { recursive: true }).filter((f) => f.endsWith('.html')).map((f) => join(dist, f));
const ids = new Map(pages.map((p) => [p, new Set([...readFileSync(p, 'utf8').matchAll(/\sid="([^"]+)"/g)].map((m) => decodeURIComponent(m[1])))]));

function resolve(page, href) {
	const url = new URL(href, 'https://site' + base + relative(dist, page));
	if (url.origin !== 'https://site') return null;
	if (!url.pathname.startsWith(base)) return { missing: url.pathname };
	let file = join(dist, decodeURIComponent(url.pathname.slice(base.length)));
	if (url.pathname.endsWith('/')) file = join(file, 'index.html');
	return { file, hash: decodeURIComponent(url.hash.slice(1)) };
}

const broken = [];
for (const page of pages) {
	const html = readFileSync(page, 'utf8');
	for (const [, href] of html.matchAll(/<a\s[^>]*?href="([^"]+)"/g)) {
		if (/^(mailto:|javascript:)/.test(href)) continue;
		const target = resolve(page, href.replace(/&amp;/g, '&'));
		if (!target) continue;
		const where = relative(dist, page);
		if (target.missing || !existsSync(target.file)) {
			broken.push(`${where}: ${href} (no such page)`);
		} else if (target.hash && target.hash !== '_top' && target.file.endsWith('.html') && !ids.get(target.file)?.has(target.hash)) {
			broken.push(`${where}: ${href} (no such anchor)`);
		}
	}
}

if (broken.length) {
	console.error(`Broken internal links:\n  ${[...new Set(broken)].join('\n  ')}`);
	process.exit(1);
}
console.log(`Checked internal links in ${pages.length} pages.`);
