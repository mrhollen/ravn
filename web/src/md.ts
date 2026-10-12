// Markdown rendering with a detached DOM sanitization pipeline.
//
// Strategy ("parse-then-sanitize"):
//   1. Raw markdown source is parsed directly with marked.parse so that markdown
//      syntax (blockquotes '>', code blocks with '<' and '&', emphasis, lists)
//      emits proper semantic HTML without entity mangling or double escaping.
//   2. The rendered HTML is post-processed in a detached DOM (DOMParser):
//      forbidden elements (script, noscript, iframe, form, style, svg, math,
//      template, input, button, select, textarea, dialog, etc.) and images are
//      stripped entirely, URL-typed attributes (href/src/data/…) that fail the
//      href allow-list below are neutralized, and inline event handlers and
//      unsafe style values are removed.
//
// The pipeline can never throw out of normal operation: any parse or DOM
// failure degrades safely to escaped plain text.

import { marked } from "marked";

// URL-typed attributes whose values must pass the href allow-list below.
const URL_ATTRS = new Set(["HREF", "SRC", "DATA", "POSTER", "ACTION", "FORMACTION", "XLINK:HREF"]);

// The only schemes an outbound link may carry. Everything else (javascript:,
// data:, vbscript:, ftp:, file:, …) fails closed and the attribute is removed.
const ALLOWED_SCHEMES = new Set(["http", "https", "mailto"]);

/**
 * Allow-list check for URL-typed attribute values (anchor/area href, src,
 * poster, …). Exactly three shapes are permitted:
 *   1. fragment-only references:     #section
 *   2. root-relative paths:          /tasks/123   (a single leading slash)
 *   3. absolute URLs with an explicit scheme from ALLOWED_SCHEMES (case-insensitive)
 *
 * Everything else fails closed so the attribute is stripped and any link text
 * renders as plain text. In particular:
 *   - protocol-relative URLs (//evil.com, ///x) are rejected UP FRONT — before
 *     any URL resolution could rewrite them to https://evil.com.
 *   - root-relative paths containing a backslash are rejected: the WHATWG URL
 *     parser normalizes "\" to "/" in special-scheme contexts, so "/\evil.com"
 *     would navigate cross-origin to https://evil.com.
 *   - "schemes" whose token carries whitespace or control characters (e.g.
 *     "java\tscript:") are not clean RFC 3986 scheme tokens; browsers strip
 *     such C0/control chars before navigation, so we fail closed instead of
 *     resolving them.
 */
export function isAllowedHref(raw: string): boolean {
  const v = raw.trim();
  if (v === "") return false; // empty href was never allowed — keep stripping it
  if (v.startsWith("//")) return false; // protocol-relative — reject BEFORE resolution
  if (v.startsWith("#")) return true; // fragment-only: cannot leave the document
  const colon = v.indexOf(":");
  if (colon !== -1 && /^[A-Za-z][A-Za-z0-9+.-]*$/.test(v.slice(0, colon))) {
    // Clean scheme token directly before the ':' → absolute URL: allow-list only.
    return ALLOWED_SCHEMES.has(v.slice(0, colon).toLowerCase());
  }
  // Otherwise a relative reference (or opaque string): only a root-relative path
  // with a single leading slash is allowed, and it must contain no backslash.
  return v.startsWith("/") && !v.includes("\\");
}

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
    // Neutralize URL-typed attributes that fail the href allow-list — including
    // javascript:/data: values AND protocol-relative URLs on surviving elements
    // (e.g. <area>). <img>/<iframe>/<object>/<embed> are already gone above.
    for (const attr of Array.from(el.attributes)) {
      if (URL_ATTRS.has(attr.name.toUpperCase()) && !isAllowedHref(attr.value)) {
        el.removeAttribute(attr.name);
      }
    }
    const style = el.getAttribute("style");
    if (style !== null && /expression|javascript\s*:|url\s*\(/i.test(style)) {
      el.removeAttribute("style");
    }
    if (tag === "A") {
      const href = el.getAttribute("href") ?? "";
      if (isAllowedHref(href)) {
        el.setAttribute("rel", "noreferrer noopener");
        el.setAttribute("target", "_blank");
      } else {
        // disallowed scheme or protocol-relative URL (javascript:, data:,
        // //evil.com, …) — render as plain text link
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

