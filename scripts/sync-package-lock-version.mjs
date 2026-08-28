import { readFile, writeFile } from "node:fs/promises";

const packagePath = new URL("../package.json", import.meta.url);
const lockPath = new URL("../package-lock.json", import.meta.url);

const packageManifest = JSON.parse(await readFile(packagePath, "utf8"));
const lockManifest = JSON.parse(await readFile(lockPath, "utf8"));
const rootPackage = lockManifest.packages?.[""];

if (typeof packageManifest.version !== "string" || packageManifest.version.length === 0) {
  throw new Error("package.json must contain a non-empty string version");
}
if (lockManifest.name !== packageManifest.name || rootPackage?.name !== packageManifest.name) {
  throw new Error("package-lock.json root package does not match package.json");
}

lockManifest.version = packageManifest.version;
rootPackage.version = packageManifest.version;

await writeFile(lockPath, `${JSON.stringify(lockManifest, null, 2)}\n`);
