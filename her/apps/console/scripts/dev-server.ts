import { startStandIn } from "./stand-in.ts";

// The built console in front of a real her, as hermeshd would serve it (stand-in.ts): run
// `pnpm build` first, then `pnpm dev` (PORT picks the port, HER_BIN_DIR where her and her-mock
// are). Its her has a private home, deleted when this stops.

const site = await startStandIn({ port: Number(process.env.PORT ?? 5179) });
console.log(`The console: ${site.url}`);
console.log(`her's home:  ${site.home}`);
console.log(`An MCP server to add by address: ${site.mcpUrl}`);
const stop = () => void site.close().then(() => process.exit(0));
process.on("SIGINT", stop);
process.on("SIGTERM", stop);
