import { describe, expect, test } from "vitest";
import { appLoginURL, connectURLFrom, parseAppLink, parseConnectAddress, pkceChallenge, projectIdFromFolder } from "./signin";

describe("sign-in", () => {
  test("the challenge is RFC 7636 S256 of the verifier", async () => {
    // RFC 7636 appendix B.
    expect(await pkceChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")).toBe("E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM");
  });

  test("the browser starts on the center's app-login page", () => {
    expect(appLoginURL("https://hermesh.io/", "abc")).toBe("https://hermesh.io/app-login?challenge=abc");
    expect(appLoginURL(" http://127.0.0.1:8080 ", "a-b_c")).toBe("http://127.0.0.1:8080/app-login?challenge=a-b_c");
    expect(() => appLoginURL("hermesh.io", "x")).toThrow("中心地址无效");
    expect(() => appLoginURL("file:///etc", "x")).toThrow("中心地址无效");
  });

  test("links: a link code, or an owner to join; anything else is not ours", () => {
    expect(parseAppLink("hermesh://link?code=ABCD-EFGH")).toEqual({ kind: "link", code: "ABCD-EFGH" });
    expect(parseAppLink("hermesh://join?owner=o_123")).toEqual({ kind: "join", owner: "o_123" });
    expect(parseAppLink("hermesh://link")).toBeNull();
    expect(parseAppLink("hermesh://join?owner=")).toBeNull();
    expect(parseAppLink("hermesh://other?code=x")).toBeNull();
    expect(parseAppLink("https://link?code=x")).toBeNull();
    expect(parseAppLink("not a url")).toBeNull();
  });

  test("discovery: a center without a connect URL is its own", () => {
    expect(connectURLFrom("https://hermesh.io/", 404, "")).toBe("https://hermesh.io");
    expect(connectURLFrom("https://hermesh.io", 200, "{}")).toBe("https://hermesh.io");
    expect(connectURLFrom("https://hermesh.io", 200, '{"connect_url":"https://connect.hermesh.io/"}')).toBe("https://connect.hermesh.io");
    expect(connectURLFrom("https://hermesh.io", 200, '{"connect_url":"https://connect.hermesh.io:8443"}')).toBe("https://connect.hermesh.io:8443");
    expect(connectURLFrom("http://127.0.0.1:9000", 200, '{"connect_url":"http://127.0.0.1:9001"}')).toBe("http://127.0.0.1:9001");
  });

  test("discovery: a connect URL that is not a bare https origin is refused", () => {
    for (const bad of ["http://connect.hermesh.io", "https://connect.hermesh.io/path", "https://connect.hermesh.io?x=1", "https://connect.hermesh.io#x", "ftp://connect.hermesh.io", "connect.hermesh.io"]) {
      expect(() => connectURLFrom("https://hermesh.io", 200, JSON.stringify({ connect_url: bad })), bad).toThrow("中心给出的连接地址无效");
    }
    expect(() => connectURLFrom("https://hermesh.io", 500, "")).toThrow("HTTP 500");
    expect(() => connectURLFrom("https://hermesh.io", 200, "<html>")).toThrow("JSON");
  });

  test("connection addresses", () => {
    const owner = "o_" + "a".repeat(24);
    expect(parseConnectAddress(`relay.example.com/${owner}`)).toEqual({ relay: "https://relay.example.com", owner });
    expect(parseConnectAddress(` http://127.0.0.1:8080/${owner}/ `)).toEqual({ relay: "http://127.0.0.1:8080", owner });
    expect(parseConnectAddress(`example.com/hermesh/${owner}`)).toEqual({ relay: "https://example.com/hermesh", owner });
    expect(parseConnectAddress("relay.example.com")).toBeNull();
    expect(parseConnectAddress("relay.example.com/o_short")).toBeNull();
    expect(parseConnectAddress("")).toBeNull();
  });

  test("project ids from folder names", () => {
    expect(projectIdFromFolder("My App", new Set())).toBe("my-app");
    expect(projectIdFromFolder(".dotfiles", new Set())).toBe("dotfiles");
    expect(projectIdFromFolder("笔记", new Set())).toBe("project");
    expect(projectIdFromFolder("app", new Set(["app", "app-2"]))).toBe("app-3");
  });
});
