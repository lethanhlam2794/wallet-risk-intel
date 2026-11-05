// Admin session store. sessionStorage ONLY — the token survives reloads
// but dies with the tab. Never localStorage, cookie, URL, or logs.

export const TOKEN_STORAGE_KEY = "cw.admin.token";
export const USER_STORAGE_KEY = "cw.admin.user";

export interface SessionUser {
  username: string;
  role: string;
}

type TokenListener = (token: string | null) => void;
const listeners = new Set<TokenListener>();

function notify(): void {
  const token = getToken();
  for (const listener of listeners) listener(token);
}

export function getToken(): string | null {
  return sessionStorage.getItem(TOKEN_STORAGE_KEY);
}

export function getUser(): SessionUser | null {
  const raw = sessionStorage.getItem(USER_STORAGE_KEY);
  if (!raw) return null;
  try {
    const v: unknown = JSON.parse(raw);
    if (
      typeof v === "object" &&
      v !== null &&
      typeof (v as SessionUser).username === "string" &&
      typeof (v as SessionUser).role === "string"
    ) {
      return v as SessionUser;
    }
  } catch {
    /* fall through */
  }
  return null;
}

export function setSession(token: string, user: SessionUser): void {
  sessionStorage.setItem(TOKEN_STORAGE_KEY, token);
  sessionStorage.setItem(USER_STORAGE_KEY, JSON.stringify(user));
  notify();
}

export function clearSession(): void {
  sessionStorage.removeItem(TOKEN_STORAGE_KEY);
  sessionStorage.removeItem(USER_STORAGE_KEY);
  notify();
}

export function subscribeToken(listener: TokenListener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
