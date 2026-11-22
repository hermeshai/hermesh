import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { LocalService } from "@hermesh/proto/local";
import { ThreadService } from "@hermesh/proto/thread";

// hermeshd's LocalService and v2 ThreadService, through the main process (app://hermesh/api).
const transport = createConnectTransport({ baseUrl: "app://hermesh/api", useBinaryFormat: true });
export const local = createClient(LocalService, transport);
export const threads = createClient(ThreadService, transport);
