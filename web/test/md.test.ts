import test from "node:test";
import assert from "node:assert/strict";

import { escapeHtml, isAllowedHref, renderMarkdown } from "../src/md.ts";

test("R9: escapeHtml escapes HTML special characters", () => {
  assert.equal(escapeHtml("<script>alert(1)</script>"), "&lt;script&gt;alert(1)&lt;/script&gt;");
  assert.equal(escapeHtml("a && b > c < d"), "a &amp;&amp; b &gt; c &lt; d");
});

test("R9: renderMarkdown handles empty and fallback without crashing", () => {
  assert.equal(renderMarkdown(""), "");
  assert.equal(renderMarkdown(null), "");
  assert.equal(renderMarkdown(undefined), "");
  assert.equal(renderMarkdown("   "), "");

  // When DOMParser is absent, renderMarkdown gracefully degrades to escaped paragraph
  const res = renderMarkdown("Hello *world*");
  assert.ok(res.length > 0);
  assert.ok(res.includes("Hello"));
});

test("MdHref: protocol-relative URLs are rejected (the core fix)", () => {
  assert.equal(isAllowedHref("//evil.com"), false, "//evil.com must be stripped");
  assert.equal(isAllowedHref("//evil.com/phish?x=1"), false);
  assert.equal(isAllowedHref("///x"), false, "triple-slash resolves to host x — strip");
  assert.equal(isAllowedHref("  //evil.com  "), false, "whitespace-padded protocol-relative");
  assert.equal(isAllowedHref("//\t/evil.com"), false, "tab between the slashes still starts with //");
});

test("MdHref: backslash smuggling into a relative path is rejected", () => {
  // Browsers normalize "\" to "/" in special-scheme contexts, so "/\evil.com"
  // would navigate cross-origin to https://evil.com.
  assert.equal(isAllowedHref("/\\evil.com"), false);
  assert.equal(isAllowedHref("\\\\evil.com"), false);
});

test("MdHref: root-relative and fragment links keep working", () => {
  assert.equal(isAllowedHref("/tasks/123"), true, "root-relative path is kept");
  assert.equal(isAllowedHref("/"), true, "site root is kept");
  assert.equal(isAllowedHref("/a/b?c=d#e"), true, "path with query+hash is kept");
  assert.equal(isAllowedHref("#section"), true, "fragment-only is kept");
});

test("MdHref: http(s), mailto and fragments are allowed; schemes are case-insensitive", () => {
  assert.equal(isAllowedHref("https://example.com"), true);
  assert.equal(isAllowedHref("http://example.com/a?b=c#d"), true);
  assert.equal(isAllowedHref("HTTPS://EXAMPLE.COM"), true, "uppercase scheme + host is allowed");
  assert.equal(isAllowedHref("mailto:x@y"), true);
  assert.equal(isAllowedHref("MAILTO:X@Y"), true, "uppercase mailto is allowed");
});

test("MdHref: disallowed schemes are rejected (no regression)", () => {
  assert.equal(isAllowedHref("javascript:alert(1)"), false);
  assert.equal(isAllowedHref("java&#x09;script:alert(1)".replace("&#x09;", "\t")), false, "tab (C0) inside the scheme token");
  assert.equal(isAllowedHref("data:text/html,<script>alert(1)</script>"), false);
  assert.equal(isAllowedHref("vbscript:MsgBox(1)"), false);
  assert.equal(isAllowedHref("ftp://files.example.com/x"), false);
  assert.equal(isAllowedHref("file:///etc/passwd"), false);
});

test("MdHref: empty and bare-relative strings stay rejected (legacy behavior)", () => {
  assert.equal(isAllowedHref(""), false, "empty was stripped before too");
  assert.equal(isAllowedHref("   "), false, "whitespace-only");
  assert.equal(isAllowedHref("tasks/123"), false, "bare relative without leading / was stripped before");
});
