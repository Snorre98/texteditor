// TUI entry point — contract-first: discovers the engine's base URL
// (ADR-0021 §1 fixed mode), rewrites the generated client to it, and renders
// the Solid/OpenTUI app. The TUI is dumb (ADR-0013 §3): every effect the app
// can cause goes through the store, which goes through the generated client.
import { render } from "@opentui/solid";
import { api, setEndpoint } from "./api/client";
import { discover } from "./api/discovery";
import { createAppStore } from "./state/store";
import { App, ConnectionErrorScreen, setConnectError } from "./ui/app";

// A document path can be given as the first CLI arg or TEXTEDITOR_DOC; the
// engine opens it by path (POST /documents).
const docPath = Bun.argv[2] ?? process.env.TEXTEDITOR_DOC ?? null;

// Discovery failure is a full screen, never a half-working store (clients
// discover rather than assume — ADR-0021 §1).
let baseUrl: string;
try {
  baseUrl = await discover();
} catch (err) {
  setConnectError(err instanceof Error ? err.message : String(err));
  await render(() => <ConnectionErrorScreen />);
  process.exit(1);
}

setEndpoint(baseUrl);
const store = createAppStore({ api, baseUrl });
await render(() => <App store={store} docPath={docPath} />);
