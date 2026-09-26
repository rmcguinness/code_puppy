// What a conversation shows, built from saved messages and a turn's
// events. Pure functions, so the rules (streamed text shown once, tool
// results matched to their calls) are tested without a browser.
import type { JsonObject } from "@bufbuild/protobuf";
import type { Message } from "./gen/blitz/v1/session_pb";
import type { TurnEvent } from "./gen/blitz/v1/turn_pb";

export type Entry =
  | { kind: "user"; text: string }
  | { kind: "model"; text: string; author: string; open: boolean }
  | { kind: "tool"; id: string; name: string; args?: JsonObject; result?: JsonObject }
  | { kind: "notice"; text: string; tone: "info" | "error" };

/** The entries for a saved session's messages. */
export function fromMessages(messages: Message[]): Entry[] {
  return messages.map((m) =>
    m.role === "user" ? { kind: "user", text: m.text } : { kind: "model", text: m.text, author: "", open: false },
  );
}

/**
 * Adds a turn event to the entries (returning new ones). Streamed text
 * grows the open model entry; the final event that repeats it closes it;
 * final text that wasn't streamed is an entry of its own. Thoughts and
 * streamed copies of tool calls are left out. A tool result fills in the
 * call it answers.
 */
export function applyEvent(entries: Entry[], ev: TurnEvent): Entry[] {
  const k = ev.kind;
  switch (k.case) {
    case "text": {
      const t = k.value;
      if (t.thought) return entries;
      const last = entries[entries.length - 1];
      const open = last?.kind === "model" && last.open ? last : undefined;
      if (t.partial) {
        if (open) return [...entries.slice(0, -1), { ...open, text: open.text + t.text }];
        return [...entries, { kind: "model", text: t.text, author: ev.author, open: true }];
      }
      if (t.repeat) {
        return open ? [...entries.slice(0, -1), { ...open, open: false }] : entries;
      }
      const closed = open ? [...entries.slice(0, -1), { ...open, open: false }] : entries;
      return [...closed, { kind: "model", text: t.text, author: ev.author, open: false }];
    }
    case "toolCall": {
      if (k.value.partial) return entries;
      return [...close(entries), { kind: "tool", id: k.value.id, name: k.value.name, args: k.value.args }];
    }
    case "toolResult": {
      const r = k.value;
      for (let i = entries.length - 1; i >= 0; i--) {
        const e = entries[i];
        if (e.kind === "tool" && !e.result && (e.id === r.id || (!r.id && e.name === r.name))) {
          const next = entries.slice();
          next[i] = { ...e, result: r.result ?? {} };
          return next;
        }
      }
      return [...entries, { kind: "tool", id: r.id, name: r.name, result: r.result ?? {} }];
    }
    case "finished": {
      const err = k.value.error;
      const done = close(entries);
      return err ? [...done, { kind: "notice", text: err.message || err.reason, tone: "error" }] : done;
    }
    default:
      return entries;
  }
}

function close(entries: Entry[]): Entry[] {
  const last = entries[entries.length - 1];
  return last?.kind === "model" && last.open ? [...entries.slice(0, -1), { ...last, open: false }] : entries;
}

/** Whether a tool result reports failure (the tools return an "error" field). */
export function failed(result: JsonObject | undefined): boolean {
  return !!result && typeof result.error === "string" && result.error !== "";
}

/** A one-line summary of a tool call's arguments. */
export function summarizeArgs(args: JsonObject | undefined): string {
  if (!args) return "";
  for (const key of ["path", "command", "url", "query", "question", "pattern"]) {
    const v = args[key];
    if (typeof v === "string" && v) return v.length > 80 ? v.slice(0, 79) + "…" : v;
  }
  return "";
}
