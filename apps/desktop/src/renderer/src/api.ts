import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { LocalService } from "@hermesh/proto/local";

// hermeshd's LocalService, through the main process (app://hermesh/api).
const transport = createConnectTransport({ baseUrl: "app://hermesh/api", useBinaryFormat: true });
export const local = createClient(LocalService, transport);
