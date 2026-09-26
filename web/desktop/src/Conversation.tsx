import { useCallback, useEffect, useRef, useState } from "react";
import { ConnectError } from "@connectrpc/connect";
import { sessions } from "./api";
import type { SessionInfo } from "./gen/blitz/v1/session_pb";
import { Decision, type ApprovalRequest, type Question, type Usage } from "./gen/blitz/v1/turn_pb";
import { applyEvent, failed, fromMessages, summarizeArgs, type Entry } from "./turns";

type Pending = { kind: "approval"; req: ApprovalRequest } | { kind: "question"; q: Question };

/** One workspace: its sessions, and the conversation in the active one. */
export function Conversation({ dir }: { dir: string }) {
  const [list, setList] = useState<SessionInfo[]>([]);
  const [session, setSession] = useState<SessionInfo>();
  const [entries, setEntries] = useState<Entry[]>([]);
  const [running, setRunning] = useState(false);
  const [pending, setPending] = useState<Pending | null>(null);
  const [usage, setUsage] = useState("");
  const [error, setError] = useState("");
  const abort = useRef<AbortController | null>(null);
  const bottom = useRef<HTMLDivElement>(null);

  const refreshList = useCallback(async () => {
    setList((await sessions.listSessions({ workspace: dir })).sessions);
  }, [dir]);

  const show = useCallback((s: SessionInfo) => {
    setSession(s);
    setEntries(fromMessages(s.messages));
    setUsage("");
  }, []);

  useEffect(() => {
    (async () => {
      try {
        const active = (await sessions.getActiveSession({ workspace: dir })).session;
        show(active ?? (await sessions.newSession({ workspace: dir })).session!);
        await refreshList();
      } catch (e) {
        setError(message(e));
      }
    })();
  }, [dir, show, refreshList]);

  useEffect(() => bottom.current?.scrollIntoView({ block: "end" }), [entries, pending]);

  const run = useCallback(
    async (text: string, accepted = false) => {
      if (!session) return;
      if (!accepted) setEntries((e) => [...e, { kind: "user", text }]);
      setRunning(true);
      setError("");
      const ctl = new AbortController();
      abort.current = ctl;
      let leftover: string[] = [];
      try {
        const stream = sessions.runTurn({ workspace: dir, sessionId: session.id, turn: { text, accepted } }, { signal: ctl.signal });
        for await (const res of stream) {
          const ev = res.event!;
          if (ev.kind.case === "approvalRequest") setPending({ kind: "approval", req: ev.kind.value });
          else if (ev.kind.case === "question") setPending({ kind: "question", q: ev.kind.value });
          else setEntries((e) => applyEvent(e, ev));
          if (ev.kind.case === "finished") {
            setUsage(usageLine(ev.kind.value.before, ev.kind.value.after));
            leftover = ev.kind.value.leftover;
          }
        }
      } catch (e) {
        if (!ctl.signal.aborted) setError(message(e));
        else setEntries((e) => [...e, { kind: "notice", text: "Interrupted.", tone: "info" }]);
      } finally {
        setRunning(false);
        setPending(null);
        abort.current = null;
        refreshList();
      }
      // Steer messages sent after the agent's last tool call were never
      // read: they are the next turn (unless the turn was stopped).
      if (leftover.length > 0 && !ctl.signal.aborted) run(leftover.join("\n\n"), true);
    },
    [dir, session, refreshList],
  );

  const submit = async (text: string) => {
    if (!running) return run(text);
    // While a turn runs, a message steers it.
    try {
      await sessions.steer({ workspace: dir, sessionId: session!.id, text });
      setEntries((e) => [...e, { kind: "user", text }, { kind: "notice", text: "Queued for the agent.", tone: "info" }]);
    } catch (e) {
      setError(message(e));
    }
  };

  const decide = async (decision: Decision) => {
    if (pending?.kind !== "approval") return;
    const requestId = pending.req.requestId;
    setPending(null);
    await sessions.approve({ workspace: dir, requestId, decision }).catch((e) => setError(message(e)));
  };

  const answer = async (text: string) => {
    if (pending?.kind !== "question") return;
    const requestId = pending.q.requestId;
    setPending(null);
    await sessions.answer({ workspace: dir, requestId, answer: text }).catch((e) => setError(message(e)));
  };

  const newSession = async () => show((await sessions.newSession({ workspace: dir })).session!);
  const load = async (id: string) => show((await sessions.loadSession({ workspace: dir, ref: id })).session!);

  return (
    <div className="conversation">
      <aside className="sessions">
        <button onClick={newSession} disabled={running}>
          New session
        </button>
        {list.map((s) => (
          <button key={s.id} className={s.id === session?.id ? "session active" : "session"} disabled={running} onClick={() => load(s.id)} title={s.id}>
            {s.snapshot ? `snapshot ${s.snapshot}` : s.title || "(untitled)"}
            <small>{s.messageCount} messages</small>
          </button>
        ))}
      </aside>
      <section className="chat">
        <div className="entries">
          {entries.map((e, i) => (
            <EntryView key={i} entry={e} />
          ))}
          {running && !pending && <p className="muted">Working…</p>}
          {pending?.kind === "approval" && <ApprovalView req={pending.req} onDecide={decide} />}
          {pending?.kind === "question" && <QuestionView q={pending.q} onAnswer={answer} />}
          {usage && <p className="muted small">{usage}</p>}
          {error && <p className="error">{error}</p>}
          <div ref={bottom} />
        </div>
        <Composer running={running} onSubmit={submit} onStop={() => abort.current?.abort()} />
      </section>
    </div>
  );
}

function EntryView({ entry }: { entry: Entry }) {
  switch (entry.kind) {
    case "user":
      return <div className="entry user">{entry.text}</div>;
    case "model":
      return <div className="entry model">{entry.text}</div>;
    case "tool":
      return (
        <div className={`entry tool ${failed(entry.result) ? "failed" : ""}`}>
          <code>{entry.name}</code> {summarizeArgs(entry.args)}
          {entry.result === undefined ? " …" : failed(entry.result) ? ` ✗ ${String(entry.result.error)}` : " ✓"}
        </div>
      );
    case "notice":
      return <div className={`entry notice ${entry.tone}`}>{entry.text}</div>;
  }
}

function ApprovalView({ req, onDecide }: { req: ApprovalRequest; onDecide: (d: Decision) => void }) {
  return (
    <div className="prompt">
      <p>
        <strong>{req.tool}</strong> wants to: {req.detail}
      </p>
      {req.diff && <pre className="diff">{req.diff}</pre>}
      <div className="buttons">
        <button onClick={() => onDecide(Decision.ONCE)} autoFocus>
          Allow once
        </button>
        {req.scopeLabel && <button onClick={() => onDecide(Decision.SESSION)}>Allow {req.scopeLabel} this session</button>}
        {req.scopeLabel && <button onClick={() => onDecide(Decision.ALWAYS)}>Always allow {req.scopeLabel}</button>}
        <button onClick={() => onDecide(Decision.DENY)}>Deny</button>
      </div>
    </div>
  );
}

function QuestionView({ q, onAnswer }: { q: Question; onAnswer: (a: string) => void }) {
  const [text, setText] = useState("");
  return (
    <div className="prompt">
      <p>{q.question}</p>
      <div className="buttons">
        {q.options.map((o) => (
          <button key={o} onClick={() => onAnswer(o)}>
            {o}
          </button>
        ))}
      </div>
      <form onSubmit={(e) => (e.preventDefault(), onAnswer(text))}>
        <input value={text} onChange={(e) => setText(e.target.value)} placeholder="Or answer in your own words" autoFocus />
      </form>
    </div>
  );
}

function Composer({ running, onSubmit, onStop }: { running: boolean; onSubmit: (t: string) => void; onStop: () => void }) {
  const [text, setText] = useState("");
  const send = () => {
    const t = text.trim();
    if (!t) return;
    setText("");
    onSubmit(t);
  };
  return (
    <div className="composer">
      <textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.shiftKey) {
            e.preventDefault();
            send();
          }
        }}
        placeholder={running ? "Steer the agent: it reads this with its next tool result" : "Ask Blitz… (Shift+Enter for a new line)"}
        rows={3}
      />
      <div className="buttons">
        <button onClick={send}>{running ? "Steer" : "Send"}</button>
        {running && <button onClick={onStop}>Stop</button>}
      </div>
    </div>
  );
}

function usageLine(before?: Usage, after?: Usage): string {
  if (!after || !before || after.calls === before.calls) return "";
  const k = (n: bigint) => (n >= 1000n ? `${(Number(n) / 1000).toFixed(1)}k` : String(n));
  let s = `↳ ${k(after.input - before.input)} in · ${k(after.output - before.output)} out · context ${k(after.lastPrompt)}`;
  if (after.priced) s += ` · $${(after.costUsd - before.costUsd).toFixed(4)}`;
  return s;
}

function message(e: unknown): string {
  return e instanceof ConnectError ? e.rawMessage : String(e);
}
