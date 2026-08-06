export const MAX_KUBERNETES_FILE_BYTES = 4 * 1024 * 1024;

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
