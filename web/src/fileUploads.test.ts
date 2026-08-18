import { describe, expect, it } from "vitest";
import { MAX_HOOK_FILE_BYTES, MAX_KUBERNETES_FILE_BYTES, MAX_SECRET_FILE_BYTES, readHookScriptFile, readKubernetesTextFile, readSecretTextFile } from "./fileUploads";

describe("readKubernetesTextFile", () => {
  it("returns uploaded text unchanged", async () => {
    const content = "apiVersion: v1\nclusters: []\n";
    await expect(readKubernetesTextFile({ size: content.length, text: async () => content })).resolves.toBe(content);
  });

  it("rejects empty files", async () => {
    await expect(readKubernetesTextFile({ size: 2, text: async () => "  " })).rejects.toThrow("selected file is empty");
  });

  it("rejects files over the credential limit before reading them", async () => {
    let read = false;
    await expect(readKubernetesTextFile({
      size: MAX_KUBERNETES_FILE_BYTES + 1,
      text: async () => { read = true; return "content"; },
    })).rejects.toThrow("under 4 MB");
    expect(read).toBe(false);
  });
});

describe("readSecretTextFile", () => {
  it("preserves multiline private keys", async () => {
    const content = "-----BEGIN OPENSSH PRIVATE KEY-----\nprivate\n-----END OPENSSH PRIVATE KEY-----\n";
    await expect(readSecretTextFile({ size: content.length, text: async () => content })).resolves.toBe(content);
  });

  it("rejects oversized and binary files", async () => {
    await expect(readSecretTextFile({ size: MAX_SECRET_FILE_BYTES + 1, text: async () => "secret" })).rejects.toThrow("under 64 KiB");
    await expect(readSecretTextFile({ size: 5, text: async () => "a\0b" })).rejects.toThrow("text-based");
  });
});

describe("readHookScriptFile", () => {
  it("preserves an uploaded Bash script", async () => {
    const content = "#!/usr/bin/env bash\nset -euo pipefail\n";
    await expect(readHookScriptFile({ size: content.length, text: async () => content })).resolves.toBe(content);
  });

  it("rejects oversized, empty, and binary scripts", async () => {
    await expect(readHookScriptFile({ size: MAX_HOOK_FILE_BYTES + 1, text: async () => "echo ignored" })).rejects.toThrow("under 64 KiB");
    await expect(readHookScriptFile({ size: 1, text: async () => " " })).rejects.toThrow("script is empty");
    await expect(readHookScriptFile({ size: 5, text: async () => "a\0b" })).rejects.toThrow("text-based Bash");
  });
});
