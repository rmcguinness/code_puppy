import { useCallback, useEffect, useState } from "react";
import { workspaces } from "./api";
import { Conversation } from "./Conversation";
import { Workers } from "./Workers";
import { chooseWorkspace, installService, serviceStatus, type ServiceStatus } from "./desktop";

// One tab per workspace directory; the service holds each workspace.
export function App() {
  const [status, setStatus] = useState<ServiceStatus | null>(null);
  const [error, setError] = useState("");
  const [tabs, setTabs] = useState<string[]>([]);
  const [active, setActive] = useState("");

  const refresh = useCallback(async () => {
    try {
      setStatus(await serviceStatus());
    } catch (e) {
      setError(String(e));
    }
  }, []);
  useEffect(() => {
    refresh();
  }, [refresh]);

  const install = async () => {
    setError("");
    try {
      await installService();
      await refresh();
    } catch (e) {
      setError(String(e));
    }
  };

  const open = async () => {
    const dir = await chooseWorkspace();
    if (!dir) return;
    setTabs((t) => (t.includes(dir) ? t : [...t, dir]));
    setActive(dir);
  };

  if (!status) return <main className="center">{error || "Starting…"}</main>;
  if (!status.running) {
    return (
      <main className="center">
        <h1>Blitz</h1>
        <p>The Blitz service isn't running. It holds your workspaces and runs scheduled workers, and it keeps running after this window closes.</p>
        {status.cli ? (
          <button onClick={install}>{status.installed ? "Start the service" : "Install and start the service"}</button>
        ) : (
          <p className="error">The blitz command wasn't found: install Blitz's CLI, then run “blitz service install”.</p>
        )}
        {error && <p className="error">{error}</p>}
      </main>
    );
  }

  return (
    <div className="app">
      <nav className="tabs">
        {tabs.map((dir) => (
          <button key={dir} className={dir === active ? "tab active" : "tab"} onClick={() => setActive(dir)} title={dir}>
            {dir.split("/").pop() || dir}
          </button>
        ))}
        <button className="tab new" onClick={open} title="Open a workspace">
          +
        </button>
      </nav>
      {active ? <Workspace dir={active} key={active} /> : <p className="center">Open a workspace to start.</p>}
    </div>
  );
}

function Workspace({ dir }: { dir: string }) {
  const [info, setInfo] = useState("");
  const [view, setView] = useState<"conversation" | "workers">("conversation");
  useEffect(() => {
    (async () => {
      try {
        const [model, agents] = await Promise.all([
          workspaces.getModel({ workspace: dir }),
          workspaces.listAgents({ workspace: dir }),
        ]);
        const agent = agents.agents.find((a) => a.active);
        setInfo(`${agent?.displayName ?? "?"} on ${model.name}` + (model.unavailable ? ` (model unavailable: ${model.unavailable})` : ""));
      } catch (e) {
        setInfo(String(e));
      }
    })();
  }, [dir]);
  return (
    <section className="workspace">
      <header>
        <strong>{dir}</strong>
        <span>{info}</span>
        <span className="buttons">
          <button className={view === "conversation" ? "tab active" : "tab"} onClick={() => setView("conversation")}>
            Conversation
          </button>
          <button className={view === "workers" ? "tab active" : "tab"} onClick={() => setView("workers")}>
            Workers
          </button>
        </span>
      </header>
      {view === "conversation" ? <Conversation dir={dir} /> : <Workers dir={dir} />}
    </section>
  );
}
