import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { useNavigate } from "react-router-dom";
import { onUnauthorized } from "@/lib/api";
import * as auth from "@/lib/auth";

interface AuthContextValue {
  token: string | null;
  user: auth.SessionUser | null;
  sessionExpired: boolean;
  login: (token: string, user: auth.SessionUser) => void;
  logout: () => void;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setToken] = useState<string | null>(() => auth.getToken());
  const [sessionExpired, setSessionExpired] = useState(false);
  const navigate = useNavigate();

  useEffect(() => auth.subscribeToken(setToken), []);

  // Mid-session 401 (stored token rejected/expired): flag the expiry and
  // drop the session. The guard renders the /login?reason=session_expired
  // redirect in the same commit — imperative navigation here would race
  // LoginPage's authed-redirect and clobber the reason.
  useEffect(
    () =>
      onUnauthorized(() => {
        setSessionExpired(true);
        auth.clearSession();
      }),
    [],
  );

  const value = useMemo<AuthContextValue>(
    () => ({
      token,
      user: token ? auth.getUser() : null,
      sessionExpired,
      login: (tok, user) => {
        setSessionExpired(false);
        auth.setSession(tok, user);
      },
      logout: () => {
        auth.clearSession();
        navigate("/login", { replace: true });
      },
    }),
    [token, sessionExpired, navigate],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
