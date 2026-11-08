// THIRD_PARTY_NOTICES.md for a release (standalone-cli-design §13.11): every Rust package the public
// build links, with its licence texts as the package ships them, and what comes from elsewhere.
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import fs from "node:fs";
import path from "node:path";

const OTHERS = `## Not Rust packages

- PDFium (the \`pdf\` component), BSD-3-Clause, https://pdfium.googlesource.com/pdfium/
- agent-desktop (the \`computer\` component), https://github.com/vercel-labs/agent-desktop
- Chrome for Testing is downloaded by \`her install browser\` from Google, under Google's terms.
- The public model catalog snapshot comes from models.dev.
`;

/** The packages the public build links, from `cargo metadata`, resolved without dev dependencies. */
export function linkedPackages(root) {
  const metadata = JSON.parse(execFileSync("cargo", ["metadata", "--format-version", "1", "--locked"], { cwd: root, encoding: "utf8", maxBuffer: 1 << 28 }));
  const tree = execFileSync("cargo", ["tree", "-p", "her-cli", "--no-default-features", "-e", "normal", "--prefix", "none", "--format", "{p}", "--target", "all"], { cwd: root, encoding: "utf8" });
  const wanted = new Set(tree.split("\n").map((line) => line.split(" ").slice(0, 2).join(" ").replace(" v", "@")));
  return metadata.packages
    .filter((pkg) => wanted.has(`${pkg.name}@${pkg.version}`) && pkg.source)
    .sort((a, b) => a.name.localeCompare(b.name) || a.version.localeCompare(b.version));
}

export function notices(root) {
  const lines = ["# Third-party notices", "", "her is built from these packages. Each licence text appears once, with the packages that use it.", ""];
  const packages = linkedPackages(root);
  lines.push("| Package | Version | Licence |", "|---|---|---|");
  for (const pkg of packages) lines.push(`| ${pkg.name} | ${pkg.version} | ${pkg.license ?? pkg.license_file ?? "see below"} |`);
  lines.push("", OTHERS, "## Licence texts", "");
  const texts = new Map();
  for (const pkg of packages) {
    const dir = path.dirname(pkg.manifest_path);
    const files = fs.readdirSync(dir).filter((name) => /^(licen[cs]e|copying|notice)/i.test(name) && fs.statSync(path.join(dir, name)).isFile());
    for (const name of files) {
      const text = fs.readFileSync(path.join(dir, name), "utf8").trim();
      const key = createHash("sha256").update(text).digest("hex");
      if (!texts.has(key)) texts.set(key, { text, users: [] });
      const users = texts.get(key).users;
      if (!users.includes(pkg.name)) users.push(pkg.name);
    }
  }
  for (const { text, users } of texts.values()) {
    lines.push(`### ${users.join(", ")}`, "", "```text", text, "```", "");
  }
  return lines.join("\n");
}
