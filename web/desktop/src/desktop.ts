// The desktop app's own functions (Go methods bound by Wails), for what the
// service can't do: its own status and installation, and native dialogs.
// Wails injects window.go at run time.

export interface ServiceStatus {
  running: boolean;
  installed: boolean;
  socket: string;
  cli: string; // the blitz binary used to install the service ("" if not found)
}

type Bound = {
  ServiceStatus(): Promise<ServiceStatus>;
  InstallService(): Promise<string>;
  ChooseWorkspace(): Promise<string>;
};

function app(): Bound {
  const go = (window as unknown as { go?: { main?: { App?: Bound } } }).go;
  if (!go?.main?.App) throw new Error("not running inside the Blitz desktop app");
  return go.main.App;
}

export const serviceStatus = () => app().ServiceStatus();
export const installService = () => app().InstallService();
export const chooseWorkspace = () => app().ChooseWorkspace();
