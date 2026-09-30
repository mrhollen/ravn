// Markdown rendering with a detached DOM sanitization pipeline.
//
// Strategy ("parse-then-sanitize"):
//   1. Raw markdown source is parsed directly with marked.parse so that markdown
//      syntax (blockquotes '>', code blocks with '<' and '&', emphasis, lists)
//      emits proper semantic HTML without entity mangling or double escaping.
//   2. The rendered HTML is post-processed in a detached DOM (DOMParser):
//      forbidden elements (script, noscript, iframe, form, style, svg, math,
//      template, input, button, select, textarea, dialog, etc.) and images are
//      stripped entirely, URL-typed attributes (href/src/data/…) carrying
//      javascript:/data: values are neutralized, anchor hrefs are restricted to
//      safe schemes (http(s), mailto, relative, #), and inline event handlers
//      and unsafe style values are removed.
//
// The pipeline can never throw out of normal operation: any parse or DOM
// failure degrades safely to escaped plain text.

import { marked } from "marked";

// URL-typed attributes whose values must never carry an active scheme.
const URL_ATTRS = new Set(["HREF", "SRC", "DATA", "POSTER", "ACTION", "FORMACTION", "XLINK:HREF"]);
const UNSAFE_URL_SCHEME = /^\s*(javascript|vbscript|data)\s*:/i;

const FORBIDDEN_TAGS = new Set([
  "SCRIPT",
  "NOSCRIPT",
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
  "SVG",
  "MATH",
  "TEMPLATE",
  "INPUT",
  "BUTTON",
  "SELECT",
  "TEXTAREA",
  "DIALOG",
]);

export function escapeHtml(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

function sanitizeHtml(html: string): string {
  const doc = new DOMParser().parseFromString(html, "text/html");
  for (const el of Array.from(doc.body.querySelectorAll("*"))) {
    if (!el.isConnected) continue;
    const tag = el.tagName.toUpperCase();
    if (FORBIDDEN_TAGS.has(tag) || tag === "IMG") {
      el.remove();
      continue;
    }
    // Strip inline event handlers
    for (const attr of Array.from(el.attributes)) {
      if (/^on/i.test(attr.name)) el.removeAttribute(attr.name);
    }
    // Neutralize javascript:/data:/vbscript: URLs in URL-typed attributes.
    // <img>/<iframe>/<object>/<embed> are already gone above, but raw source
    // markup can still smuggle these onto surviving elements (e.g. <area>).
    for (const attr of Array.from(el.attributes)) {
      if (URL_ATTRS.has(attr.name.toUpperCase()) && UNSAFE_URL_SCHEME.test(attr.value)) {
        el.removeAttribute(attr.name);
      }
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
  let html: string;
  try {
    html = marked.parse(src, { async: false }) as string;
  } catch {
    return `<p>${escapeHtml(src)}</p>`; // never crash — show the escaped text as-is
  }
  if (typeof html !== "string") return `<p>${escapeHtml(src)}</p>`;
  try {
    return sanitizeHtml(html);
  } catch {
    return `<p>${escapeHtml(src)}</p>`;
  }
}

