export function hasOAuthHandoff(params: URLSearchParams) {
  return params.has("auth_code") || params.has("auth_error");
}
