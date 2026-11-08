import { createContext, useContext } from "react";

/** The session a conversation's content belongs to: what a row keeps while it is not drawn is kept under it (useRowState). None outside a session. */
const SessionContext = createContext("");
export const ContentScope = SessionContext.Provider;
export const useContentSession = () => useContext(SessionContext);

/** Inside the block a reply is still writing: nothing in it acts yet (links are words, code blocks have no buttons). */
export const DraftContext = createContext(false);
