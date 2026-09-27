export const previewIDVariable = "{{ instance.id }}";
export const hasPreviewID = (value: string) => /\{\{\s*instance\.id\s*\}\}/.test(value) || value.includes("__PREVIEW_ID__");
