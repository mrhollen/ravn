// Markdown rendering with a no-extra-dependencies sanitization pipeline.
//
// Strategy ("escape-then-render"):
//   1. HTML-significant characters in the *source* are escaped first, so raw
//      <script>/<img onerror=…>/etc. from data can never become live markup —
//      marked only ever emits tags that markdown SYNTAX asks for (p, a, code,
//      ul, img from ![…], …).
//   2. The rendered HTML is then post-processed in a detached DOM: script/
//      form-like elements and <img> are dropped entirely (no remote images in
//      the MVP — this also kills data:-URL exfiltration via broken links),
//      anchor hrefs are restricted to http(s)/relative/#/mailto:, inline event
//      handlers and suspicious style values are stripped.
//
// The pipeline can never throw out of normal operation: any parse failure
// degrades to escaped plain text.

import { marked } from "marked";

const FORBIDDEN_TAGS = new Set([
  "SCRIPT",
  "IFRAME",
  "OBJECT",
  "EMBED",
  "FORM",
  "META",
  "BASE",
  "LINK",
  "STYLE",
  "AUDIO",
  "VIDEO",
]);

export function escapeHtml(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

function sanitizeHtml(html: string): string {
  const doc = new DOMParser().parseFromString(html, "text/html");
  for (const el of Array.from(doc.body.querySelectorAll("*"))) {
    const tag = el.tagName.toUpperCase();
    if (FORBIDDEN_TAGS.has(tag) || tag === "IMG") {
      el.remove();
      continue;
    }
    // Strip inline event handlers (defence in depth — the escape step already
    // prevents them from ever existing).
    for (const attr of Array.from(el.attributes)) {
      if (/^on/i.test(attr.name)) el.removeAttribute(attr.name);
    }
    const style = el.getAttribute("style");
    if (style !== null && /expression|javascript\s*:|url\s*\(/i.test(style)) {
      el.removeAttribute("style");
    }
    if (tag === "A") {
      const href = el.getAttribute("href") ?? "";
      if (/^(https?:|mailto:|\/|#)/i.test(href.trim())) {
        el.setAttribute("rel", "noreferrer noopener");
        el.setAttribute("target", "_blank");
      } else {
        // unknown scheme (javascript:, data:, …) — render as plain text link
        el.removeAttribute("href");
      }
    }
  }
  return doc.body.innerHTML;
}

/** Renders markdown source to sanitized HTML. Empty/whitespace → "". */
export function renderMarkdown(src: string | null | undefined): string {
  if (!src || !src.trim()) return "";
  const escaped = escapeHtml(src);
  let html: string;
  try {
    html = marked.parse(escaped, { async: false });
  } catch {
    return `<p>${escaped}</p>`; // never crash — show the escaped text as-is
  }
  if (typeof html !== "string") return `<p>${escaped}</p>`;
  try {
    return sanitizeHtml(html);
  } catch {
    return `<p>${escaped}</p>`;
  }
}
