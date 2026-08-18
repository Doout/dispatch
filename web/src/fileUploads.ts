export const MAX_KUBERNETES_FILE_BYTES = 4 * 1024 * 1024;
export const MAX_SECRET_FILE_BYTES = 64 * 1024;
export const MAX_HOOK_FILE_BYTES = 64 * 1024;

type ReadableFile = Pick<File, "size" | "text">;

export async function readKubernetesTextFile(file: ReadableFile): Promise<string> {
  if (file.size > MAX_KUBERNETES_FILE_BYTES) {
    throw new Error("Keep credential files under 4 MB.");
  }

  const content = await file.text();
  if (!content.trim()) {
    throw new Error("The selected file is empty.");
  }

  return content;
}

export async function readSecretTextFile(file: ReadableFile): Promise<string> {
  if (file.size > MAX_SECRET_FILE_BYTES) {
    throw new Error("Keep secret files under 64 KiB.");
  }

  const content = await file.text();
  if (!content.trim()) {
    throw new Error("The selected file is empty.");
  }
  if (content.includes("\0")) {
    throw new Error("Choose a text-based credential file.");
  }
  return content;
}

export async function readHookScriptFile(file: ReadableFile): Promise<string> {
  if (file.size > MAX_HOOK_FILE_BYTES) {
    throw new Error("Keep build scripts under 64 KiB.");
  }
  const content = await file.text();
  if (!content.trim()) {
    throw new Error("The selected script is empty.");
  }
  if (content.includes("\0")) {
    throw new Error("Choose a text-based Bash script.");
  }
  return content;
}
