// Per-thread reply drafts, kept in localStorage.
//
// The composer unmounts on anything that leaves the thread (a route change, a
// scope switch, a remount after a background refetch), and React state goes
// with it. What the user typed is mirrored here and read back when a composer
// for the same target opens again. Cleared once the reply is sent.

import type { ReplySeed } from "@/components/app/unibox/ReplyComposer";

const PREFIX = "warmbly-reply-draft:";

/** threadId + the message being replied to + the mode: one draft per composer. */
export function replyDraftKey(threadId: string, messageId: string, mode: string): string {
    return `${PREFIX}${threadId}:${messageId}:${mode}`;
}

function isEmpty(d: ReplySeed): boolean {
    return (
        !d.body.trim() &&
        !d.cc.length &&
        !d.bcc.length
    );
}

export function loadReplyDraft(key: string): ReplySeed | null {
    try {
        const raw = localStorage.getItem(key);
        if (!raw) return null;
        const d = JSON.parse(raw) as Partial<ReplySeed>;
        if (typeof d?.body !== "string") return null;
        return {
            to: Array.isArray(d.to) ? d.to : [],
            cc: Array.isArray(d.cc) ? d.cc : [],
            bcc: Array.isArray(d.bcc) ? d.bcc : [],
            subject: typeof d.subject === "string" ? d.subject : "",
            body: d.body,
        };
    } catch {
        // Private mode, quota, or hand-edited junk: a lost draft is not worth
        // taking the composer down for.
        return null;
    }
}

/** Writes the draft, or removes it once there is nothing worth keeping. */
export function saveReplyDraft(key: string, draft: ReplySeed): void {
    try {
        if (isEmpty(draft)) localStorage.removeItem(key);
        else localStorage.setItem(key, JSON.stringify(draft));
    } catch {
        /* storage unavailable: drafting still works for this session */
    }
}

export function clearReplyDraft(key: string): void {
    try {
        localStorage.removeItem(key);
    } catch {
        /* nothing to do */
    }
}
