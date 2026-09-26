// Clients for the Blitz service. The desktop app's Go side forwards
// /blitz.v1.* requests from this page to the service's Unix socket, so
// the page talks to its own origin.
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { SessionService } from "./gen/blitz/v1/session_pb";
import { WorkerService } from "./gen/blitz/v1/worker_pb";
import { WorkspaceService } from "./gen/blitz/v1/workspace_pb";

const transport = createConnectTransport({ baseUrl: window.location.origin });

export const sessions = createClient(SessionService, transport);
export const workspaces = createClient(WorkspaceService, transport);
export const workers = createClient(WorkerService, transport);
