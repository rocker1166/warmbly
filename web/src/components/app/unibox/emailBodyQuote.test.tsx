// The quote split is the one bit of real logic in EmailBody: get it wrong and
// either the whole reply disappears behind the toggle, or the toggle never
// appears and the thread stays a wall of repeated text.

import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

import EmailBody from "./EmailBody";

const GMAIL_REPLY =
    `<div dir="ltr">Sounds good, Tuesday works.</div>` +
    `<div class="gmail_quote"><blockquote class="gmail_quote">Original question here</blockquote></div>`;

const PLAIN_REPLY = [
    "Sounds good, Tuesday works.",
    "",
    "On Mon, 15 Sep 2026 at 09:12, Ada <ada@example.com> wrote:",
    "> Can we move the call?",
].join("\n");

function toggle() {
    return screen.queryByRole("button", { name: /quoted text/i });
}

describe("EmailBody quoted history", () => {
    it("offers the toggle for an HTML reply that carries a quote", () => {
        render(<EmailBody html={GMAIL_REPLY} />);
        expect(toggle()).toHaveTextContent("Show quoted text");
    });

    it("flips the toggle label on click", () => {
        render(<EmailBody html={GMAIL_REPLY} />);
        fireEvent.click(toggle()!);
        expect(toggle()).toHaveTextContent("Hide quoted text");
    });

    it("detects a plain-text quote tail and wraps it", () => {
        const { container } = render(<EmailBody plain={PLAIN_REPLY} />);
        expect(toggle()).toBeInTheDocument();
        const srcDoc = container.querySelector("iframe")!.getAttribute("srcdoc")!;
        expect(srcDoc).toContain("Sounds good, Tuesday works.");
        // The tail is inside the wrapper the hide rule targets, the new line is not.
        // lastIndexOf: the attribute also appears in the hide rule up in <head>.
        const wrapped = srcDoc.slice(srcDoc.lastIndexOf("data-warmbly-quote"));
        expect(wrapped).toContain("Can we move the call?");
        expect(wrapped).not.toContain("Sounds good");
    });

    it("leaves a message that is nothing but quote alone", () => {
        render(<EmailBody plain={"> only the quote\n> second line"} />);
        expect(toggle()).not.toBeInTheDocument();
    });

    it("shows no toggle for an ordinary first message", () => {
        render(<EmailBody plain={"Hi Ada,\n\nAre you free Tuesday?"} />);
        expect(toggle()).not.toBeInTheDocument();
    });
});
