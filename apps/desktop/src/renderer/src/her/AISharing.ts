import { useTranslation } from "react-i18next";
import type { Status } from "@hermesh/her-protocol";
import { useStore } from "../store";
import { useHer } from "./context";
import { modelTitle } from "./format";

export function aiSharingAllowed(status: Status) {
  return !!status.recipient && status.allowed?.provider === status.recipient.provider &&
    status.allowed.endpoint === status.recipient.endpoint;
}

export function useAISharingConfirmation() {
  const { t } = useTranslation("her");
  const her = useHer();
  const st = useStore();
  return (model: string, status: Status, after: () => void | Promise<void>) => {
    const { recipient, revision } = status;
    if (!recipient) {
      st.show(t("aiSharing.unverified"));
      return;
    }
    const provider = her.env.models.providers.find((p) => p.id === recipient.provider)?.name ?? recipient.provider;
    const modelLabel = modelTitle(model, her.env.models);
    st.set({
      sheet: {
        kind: "confirm",
        title: t("aiSharing.title"),
        message: t("aiSharing.disclosure", { provider, endpoint: recipient.endpoint, model: modelLabel }),
        button: t("aiSharing.allow"),
        safe: true,
        run: async () => {
          await her.call("ai_consent/allow", { model, recipient, revision });
          await after();
        },
      },
    });
  };
}
