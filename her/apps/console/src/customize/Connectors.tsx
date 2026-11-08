import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { McpServerView } from "@hermesh/her-protocol";
import { PageTop } from "../components";
import { Icon } from "../icons";
import { AddConnector } from "./AddConnector";
import { ServerPage } from "./Details";
import { EditConnector } from "./EditConnector";
import { MyConnectors, type ServerActions } from "./Lists";
import { Loading, useEscBack } from "./parts";
import { useCustomize } from "./useCustomize";

// Connectors: the MCP servers of this environment, each opening a page of its own; added by
// address, signed in to through the owner's device, or started as a command once the owner's
// Mac signs it.

type Sheet = { kind: "add" } | { kind: "edit"; server: McpServerView };

export function ConnectorsPage({ item, go }: { item: string | null; go: (item?: string) => void }) {
  const { t } = useTranslation("customize");
  const { t: tc } = useTranslation();
  const c = useCustomize();
  const [sheet, setSheet] = useState<Sheet | null>(null);
  const server = item ? (c.servers.find((s) => s.name === item) ?? null) : null;
  // Gone (removed elsewhere, or a name that is not there): back to the list.
  useEffect(() => {
    if (item && c.loaded && !server) go();
  }, [item, c.loaded, server, go]);
  useEscBack(!!server, () => go());

  const a: ServerActions = {
    open: (s) => go(s.name),
    add: () => setSheet({ kind: "add" }),
    edit: (s) => setSheet({ kind: "edit", server: s }),
    toggle: (s, on) => void c.switchServer(s, on, t(on ? "toast.serverOn" : "toast.serverOff", { name: s.name })),
  };

  return (
    <>
      <PageTop title={tc("nav.connectors")} back={server ? { label: tc("nav.connectors"), go: () => go() } : undefined}>
        {/* With none yet, the empty list offers it. */}
        {!server && c.servers.length > 0 && (
          <button className="btn" onClick={a.add}>
            <Icon name="plus" />
            {t("add.connector")}
          </button>
        )}
      </PageTop>
      <div className="set-body cz">
        {!c.loaded ? <Loading c={c} /> : server ? <ServerPage key={server.name} c={c} a={a} s={server} /> : <MyConnectors c={c} a={a} />}
      </div>
      {sheet?.kind === "add" && <AddConnector c={c} onClose={() => setSheet(null)} onAdded={() => go()} onOpen={(name) => (setSheet(null), go(name))} />}
      {sheet?.kind === "edit" && <EditConnector c={c} server={sheet.server} onClose={() => setSheet(null)} onSaved={(name) => go(name)} />}
    </>
  );
}
