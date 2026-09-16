// Renders a received message body.
//
// Email HTML is other people's markup: full documents with their own <style>
// blocks, table layouts, and font stacks. Dropping that into the dashboard DOM
// would let a newsletter restyle the app, so it renders inside a sandboxed
// iframe instead. The iframe carries no `allow-scripts`, so nothing in the
// message can execute even though the API already sanitizes the HTML on the
// way out; the two together are belt and braces.
//
// Height is measured from the inner document and kept in sync as images load,
// so the message reads as part of the page rather than a scroll box.

import React from "react";
import { MoreHorizontalIcon } from "lucide-react";
import { plainToDisplayHtml } from "@/lib/email/body";

interface EmailBodyProps {
    html?: string | null;
    plain?: string | null;
}

// ── Quoted history ─────────────────────────────────────────────────────────
// Every reply carries the whole conversation inside it, so rendering a thread
// verbatim shows the same text N times and buries the three lines that are
// actually new. Mail clients collapse that; so do we. The markers below are
// what the major clients leave behind, plus our own wrapper for plain text.
const QUOTE_SELECTORS = [
    ".gmail_quote",
    ".gmail_quote_container",
    ".gmail_extra",
    'blockquote[type="cite"]',
    ".yahoo_quoted",
    ".moz-cite-prefix",
    "#divRplyFwdMsg",
    "#appendonsend",
    "[data-warmbly-quote]",
].join(", ");

const HIDE_QUOTE_CSS = `${QUOTE_SELECTORS} { display: none !important; }`;

// Decides whether the toggle is worth showing. Kept in step with
// QUOTE_SELECTORS by hand; a DOM parse here would only be thrown away.
const HAS_QUOTE =
    /\b(?:gmail_quote|gmail_extra|yahoo_quoted|moz-cite-prefix)\b|id=["'](?:divRplyFwdMsg|appendonsend)["']|<blockquote[^>]+type=["']cite["']|data-warmbly-quote/i;

// The line that starts the quoted tail of a plain-text reply: a `>` quote, an
// attribution line, or one of the separators Outlook writes.
const PLAIN_QUOTE_LINE =
    /^(?:>.*|On\b[\s\S]{0,200}?\bwrote:|-{2,}\s*Original Message\s*-{2,}|-{2,}\s*Forwarded message\s*-{2,}|_{10,}|From:\s.+)$/;

/** Splits plain text into what the sender wrote and the history below it. */
function splitPlainQuote(text: string): { head: string; quote: string } {
    const lines = text.replace(/\r\n/g, "\n").split("\n");
    const at = lines.findIndex((l) => PLAIN_QUOTE_LINE.test(l.trim()));
    // No marker, or the message is nothing but quote: leave it whole.
    if (at <= 0) return { head: text, quote: "" };
    return {
        head: lines.slice(0, at).join("\n").trimEnd(),
        quote: lines.slice(at).join("\n"),
    };
}

function plainToBody(text: string): string {
    const { head, quote } = splitPlainQuote(text);
    if (!quote) return plainToDisplayHtml(text);
    return `${plainToDisplayHtml(head)}<div data-warmbly-quote>${plainToDisplayHtml(quote)}</div>`;
}

// Typography for the message document. Deliberately minimal: the message
// brings its own styling, and this only sets what it does not. It is injected
// at the END of the document's head so the message's own rules win.
const DOCUMENT_CSS = `
  html, body { margin: 0; padding: 0; }
  body {
    font-family: Inter, ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif;
    font-size: 13px;
    line-height: 1.6;
    color: #1e293b;
    background: transparent;
    word-break: break-word;
    overflow-wrap: anywhere;
  }
  /* Containment, not styling: a 600px design must not scroll the drawer
     sideways, so these hold even against the message's own stylesheet. */
  img { max-width: 100% !important; height: auto; border: 0; }
  table { max-width: 100% !important; }
  a { color: #0284c7; }
  blockquote {
    margin: 0.5em 0;
    padding-left: 0.75em;
    border-left: 2px solid #e2e8f0;
    color: #475569;
  }
  pre { white-space: pre-wrap; }
`;

// A body that is already a whole document (a campaign written in HTML mode, a
// designed newsletter) must not be nested inside another one: the doctype and
// the <head> would land in the body, and the frame would preview something the
// recipient will never see. Its own <head> gets our shell instead.
const DOCUMENT_ROOT = /^\s*(?:<!--[\s\S]*?-->\s*)*(?:<!doctype\s+html|<html[\s>])/i;
const HEAD_OPEN = /<head\b[^>]*>/i;
const HTML_OPEN = /<html\b[^>]*>/i;

function shell(showQuoted: boolean): string {
    return (
        `<meta charset="utf-8"><meta name="referrer" content="no-referrer">` +
        // Every link in the message leaves the dashboard in a new tab.
        `<base target="_blank"><style>${DOCUMENT_CSS}</style>` +
        // The quote rule is !important and injected last so it beats the
        // message's own stylesheet, which is what re-shows it.
        (showQuoted ? "" : `<style>${HIDE_QUOTE_CSS}</style>`)
    );
}

function buildDocument(body: string, showQuoted: boolean): string {
    const SHELL = shell(showQuoted);
    if (DOCUMENT_ROOT.test(body)) {
        // Our shell goes FIRST in the head, so the message's own stylesheet
        // comes after it and wins on everything but the containment rules,
        // which are marked !important above.
        if (HEAD_OPEN.test(body)) return body.replace(HEAD_OPEN, `$&${SHELL}`);
        if (HTML_OPEN.test(body)) return body.replace(HTML_OPEN, `$&<head>${SHELL}</head>`);
        return `<!doctype html><html><head>${SHELL}</head>${body}</html>`;
    }
    return `<!doctype html><html><head>${SHELL}</head><body>${body}</body></html>`;
}

export default function EmailBody({ html, plain }: EmailBodyProps) {
    const frameRef = React.useRef<HTMLIFrameElement>(null);
    const [height, setHeight] = React.useState(0);
    const [showQuoted, setShowQuoted] = React.useState(false);

    // The message body, before the shell. Split from srcDoc so toggling the
    // quote does not re-run the plain-text conversion.
    const body = React.useMemo(() => {
        const trimmedHtml = (html ?? "").trim();
        if (trimmedHtml) return trimmedHtml;
        const trimmedPlain = (plain ?? "").trim();
        if (trimmedPlain) return plainToBody(trimmedPlain);
        return "";
    }, [html, plain]);

    const hasQuote = React.useMemo(() => HAS_QUOTE.test(body), [body]);
    const srcDoc = React.useMemo(
        () => (body ? buildDocument(body, showQuoted) : ""),
        [body, showQuoted],
    );

    // Late-loading remote images change the document height after onLoad, so
    // measurement repeats until the size settles rather than running once.
    const measure = React.useCallback(() => {
        const doc = frameRef.current?.contentDocument;
        if (!doc?.body) return;
        const next = Math.ceil(
            Math.max(doc.body.scrollHeight, doc.documentElement?.scrollHeight ?? 0),
        );
        setHeight((prev) => (Math.abs(prev - next) > 1 ? next : prev));
    }, []);

    const observerRef = React.useRef<ResizeObserver | null>(null);
    React.useEffect(() => () => observerRef.current?.disconnect(), []);

    const onLoad = React.useCallback(() => {
        measure();
        const doc = frameRef.current?.contentDocument;
        if (!doc?.body) return;
        // A reload replaces the document the previous observer watched.
        observerRef.current?.disconnect();
        const observer = new ResizeObserver(measure);
        observer.observe(doc.body);
        observerRef.current = observer;
        doc.querySelectorAll("img").forEach((img) => {
            img.addEventListener("load", measure);
            img.addEventListener("error", measure);
        });
    }, [measure]);

    if (!srcDoc) {
        return (
            <p className="text-[13px] text-slate-400 italic">This message has no content.</p>
        );
    }

    return (
        <>
            <iframe
                ref={frameRef}
                title="Message body"
                srcDoc={srcDoc}
                onLoad={onLoad}
                // No allow-scripts: message markup can never run code. allow-popups
                // (plus escape-to-normal-context) is what lets a link actually open.
                sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
                referrerPolicy="no-referrer"
                className="w-full border-0 block"
                style={{ height: height ? `${height}px` : "80px" }}
            />
            {hasQuote && (
                <button
                    type="button"
                    onClick={() => setShowQuoted((v) => !v)}
                    aria-expanded={showQuoted}
                    title={showQuoted ? "Hide the quoted conversation" : "Show the quoted conversation"}
                    className="mt-1 h-5 px-1.5 rounded bg-slate-100 hover:bg-slate-200 text-slate-500 hover:text-slate-700 inline-flex items-center gap-1 text-[10.5px] transition-colors"
                >
                    <MoreHorizontalIcon className="w-3 h-3" />
                    {showQuoted ? "Hide quoted text" : "Show quoted text"}
                </button>
            )}
        </>
    );
}
