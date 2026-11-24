import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { LocalService } from "@hermesh/proto/local";
import { SessionService } from "@hermesh/proto/session";

// hermeshd's LocalService and unified SessionService, through the main process (app://hermesh/api).
const transport = createConnectTransport({ baseUrl: "app://hermesh/api", useBinaryFormat: true });
export const local = createClient(LocalService, transport);
export const sessions = createClient(SessionService, transport);
