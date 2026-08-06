import { describe, expect, it } from "vitest";
import { MAX_KUBERNETES_FILE_BYTES, readKubernetesTextFile } from "./fileUploads";

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
