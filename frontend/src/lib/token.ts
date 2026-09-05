/**
 * Where the access token lives in the browser.
 *
 * In a real deployment the identity provider issues this: the application
 * redirects to it, and the token comes back. Here a development endpoint mints
 * one, and the picker chooses which identity to act as.
 *
 * sessionStorage rather than localStorage: the token should not outlive the
 * tab. It is not a cookie because it is not ours to set — a provider issues it,
 * and the application only carries it.
 */
const key = "identityhub.token";

export function readToken(): string | null {
  return sessionStorage.getItem(key);
}

export function writeToken(token: string): void {
  sessionStorage.setItem(key, token);
}

export function clearToken(): void {
  sessionStorage.removeItem(key);
}
