import { useCallback, useEffect, useState } from "react";
import { ConnectError } from "@connectrpc/connect";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { workers } from "./api";
import { RunStatus, WorkerState, type Worker, type WorkerRun } from "./gen/blitz/v1/worker_pb";
import { applyEvent, failed, summarizeArgs, type Entry } from "./turns";

const stateLabel: Record<WorkerState, string> = {
  [WorkerState.UNSPECIFIED]: "?",
  [WorkerState.NEW]: "new",
  [WorkerState.ENABLED]: "enabled",
  [WorkerState.DISABLED]: "disabled",
  [WorkerState.CHANGED]: "changed since enabled",
  [WorkerState.INVALID]: "invalid",
};

const runLabel: Record<RunStatus, string> = {
  [RunStatus.UNSPECIFIED]: "?",
  [RunStatus.RUNNING]: "running",
  [RunStatus.SUCCEEDED]: "succeeded",
  [RunStatus.FAILED]: "failed",
  [RunStatus.LIMITED]: "stopped at a limit",
  [RunStatus.SKIPPED]: "skipped",
};

/** A workspace's workers: review and enable them, run them, see their runs. */
export function Workers({ dir }: { dir: string }) {
  const [list, setList] = useState<Worker[]>([]);
  const [selected, setSelected] = useState("");
  const [error, setError] = useState("");

  const refresh = useCallback(async () => {
    try {
      setList((await workers.listWorkers({ workspace: dir })).workers);
    } catch (e) {
      setError(message(e));
    }
  }, [dir]);
  useEffect(() => {
    refresh();
  }, [refresh]);

  const worker = list.find((w) => w.name === selected);
  return (
    <div className="conversation">
      <aside className="sessions">
        {list.length === 0 && <p className="muted small">No workers: add workers/&lt;name&gt;/WORKER.md to this workspace.</p>}
        {list.map((w) => (
          <button key={w.name} className={w.name === selected ? "session active" : "session"} onClick={() => setSelected(w.name)}>
            {w.name}
            <small>
              {stateLabel[w.state]} · {w.schedule}
            </small>
          </button>
        ))}
      </aside>
      <section className="chat">
        {error && <p className="error">{error}</p>}
        {worker ? <WorkerView dir={dir} worker={worker} onChange={refresh} key={worker.name + worker.hash} /> : <p className="muted">Choose a worker.</p>}
      </section>
    </div>
  );
}

function WorkerView({ dir, worker, onChange }: { dir: string; worker: Worker; onChange: () => void }) {
  const [runs, setRuns] = useState<WorkerRun[]>([]);
  const [live, setLive] = useState<Entry[] | null>(null);
  const [error, setError] = useState("");

  const refreshRuns = useCallback(async () => {
    setRuns((await workers.listWorkerRuns({ workspace: dir, name: worker.name, limit: 20 })).runs);
  }, [dir, worker.name]);
  useEffect(() => {
    refreshRuns().catch((e) => setError(message(e)));
  }, [refreshRuns]);

  const act = async (f: () => Promise<unknown>) => {
    setError("");
    try {
      await f();
      onChange();
    } catch (e) {
      setError(message(e));
    }
  };
  // The hash shown is the one enabled: an edit since then fails.
  const enable = () => act(() => workers.enableWorker({ workspace: dir, name: worker.name, hash: worker.hash }));
  const disable = () => act(() => workers.disableWorker({ workspace: dir, name: worker.name }));
  const runNow = () =>
    act(async () => {
      const started = (await workers.runWorker({ workspace: dir, name: worker.name })).run!;
      setLive([]);
      try {
        for await (const res of workers.watchWorkerRun({ runId: started.id })) {
          setLive((e) => applyEvent(e ?? [], res.event!));
        }
      } catch {
        // A run too quick to watch is simply recorded.
      }
      await refreshRuns();
    });

  const limits = worker.limits;
  return (
    <div className="entries">
      <h3>{worker.name}</h3>
      {worker.description && <p>{worker.description}</p>}
      <dl className="facts">
        <dt>State</dt>
        <dd>{stateLabel[worker.state]}</dd>
        <dt>Schedule</dt>
        <dd>
          {worker.schedule} = <code>{worker.cron}</code> ({worker.timezone})
          {worker.nextRun && `, next ${timestampDate(worker.nextRun).toLocaleString()}`}
        </dd>
        <dt>May</dt>
        <dd>{worker.permissions.length ? worker.permissions.map((p) => <code key={p}>{p} </code>) : "read only"}</dd>
        <dt>Limits</dt>
        <dd>
          {limits?.maxTurns} model calls, ${limits?.maxCostUsd.toFixed(2)}, {limits?.timeout ? `${Number(limits.timeout.seconds) / 60} min` : "?"}
        </dd>
        <dt>Content</dt>
        <dd>
          <code className="small">{worker.hash}</code>
        </dd>
      </dl>
      {worker.problems.map((p) => (
        <p key={p} className="error small">
          ! {p}
        </p>
      ))}
      <div className="buttons">
        {worker.state !== WorkerState.ENABLED && worker.state !== WorkerState.INVALID && (
          <button onClick={enable} title={`Read ${worker.path} first: it runs unattended with these permissions`}>
            Enable as shown
          </button>
        )}
        {worker.state === WorkerState.ENABLED && <button onClick={disable}>Disable</button>}
        {worker.state === WorkerState.ENABLED && <button onClick={runNow}>Run now</button>}
      </div>
      {error && <p className="error">{error}</p>}
      {live && (
        <div className="prompt">
          {live.length === 0 && <p className="muted">Running…</p>}
          {live.map((e, i) => (
            <div key={i} className={`entry ${e.kind}`}>
              {e.kind === "tool" ? `${e.name} ${summarizeArgs(e.args)}${e.result === undefined ? " …" : failed(e.result) ? " ✗" : " ✓"}` : "text" in e ? e.text : ""}
            </div>
          ))}
        </div>
      )}
      <h4>Runs</h4>
      {runs.length === 0 && <p className="muted small">It hasn't run yet.</p>}
      {runs.map((r) => (
        <div key={r.id} className="entry small">
          {r.started && timestampDate(r.started).toLocaleString()} · {runLabel[r.status]} · {r.manual ? "manual" : "scheduled"} · ${r.usage?.costUsd.toFixed(4)} · session{" "}
          <code>{r.sessionId}</code>
          {r.refusals.map((f, i) => (
            <div key={i} className="muted">
              refused: {f.detail}
            </div>
          ))}
          {r.error && <div className="error">{r.error.message}</div>}
        </div>
      ))}
    </div>
  );
}

function message(e: unknown): string {
  return e instanceof ConnectError ? e.rawMessage : String(e);
}
