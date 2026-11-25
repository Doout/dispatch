// Older controllers included the conversion URL in the redirect's error text.
// Replace it before the portal starts fetching data or rendering that message.
export function sanitizeLegacyGitHubManifestRedirect() {
  const url = new URL(window.location.href);
  const query = url.searchParams;
  if (query.get("githubAppStatus") !== "error" && query.get("authProviderStatus") !== "error") return;
  const detail = query.get("detail") ?? "";
  if (!/app-manifests(?:\/|%2f)/i.test(detail)) return;

  let message = "The GitHub App registration request failed.";
  if (/certificate signed by unknown authority/i.test(detail)) {
    message = "GitHub App registration failed because the TLS certificate was signed by an unknown authority.";
  } else if (/tls|x509/i.test(detail)) {
    message = "GitHub App registration failed during the TLS handshake.";
  } else if (/timeout|timed out|deadline exceeded/i.test(detail)) {
    message = "The GitHub App registration request timed out.";
  }
  query.set("detail", message);
  window.history.replaceState(window.history.state, "", url);
}
