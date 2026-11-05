import { useState, type FormEvent } from "react";
import {
  Navigate,
  useNavigate,
  useSearchParams,
} from "react-router-dom";
import {
  Alert,
  AlertDescription,
  Button,
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
  Input,
  Label,
} from "@/components";
import { ApiError, apiFetch } from "@/lib/api";
import { useAuth } from "@/auth/auth-context";
import type { LoginResponse } from "@/lib/types";

const RETRYABLE_MESSAGE = "Service unreachable — retry.";

export function LoginPage() {
  const { token, login } = useAuth();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  if (token) return <Navigate to="/providers" replace />;

  const sessionExpired = params.get("reason") === "session_expired";

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (submitting) return;
    setError(null);
    setSubmitting(true);
    try {
      // skipUnauthorizedHandler keeps bad credentials from triggering the
      // session-expired redirect — this 401 means "login failed" (A1).
      const env = await apiFetch<LoginResponse>("/api/v1/auth/login", {
        method: "POST",
        body: { username, password },
        skipUnauthorizedHandler: true,
      });
      login(env.data.token, env.data.user);
      navigate("/providers", { replace: true });
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 401) setError("Invalid username or password.");
        else if (err.status >= 500) setError(RETRYABLE_MESSAGE);
        else setError(err.message);
      } else {
        setError(RETRYABLE_MESSAGE);
      }
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="flex min-h-svh items-center justify-center bg-background p-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>Check Wallet Admin</CardTitle>
          <CardDescription>
            Sign in with an admin account.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {sessionExpired ? (
            <Alert variant="warning" className="mb-3">
              <AlertDescription>
                Session expired — sign in again.
              </AlertDescription>
            </Alert>
          ) : null}
          <form
            data-testid="login-form"
            onSubmit={onSubmit}
            className="space-y-3"
          >
            <div className="space-y-1">
              <Label htmlFor="admin-username">Username</Label>
              <Input
                id="admin-username"
                data-testid="login-username-input"
                autoComplete="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                required
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="admin-password">Password</Label>
              <Input
                id="admin-password"
                data-testid="login-password-input"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </div>
            {error ? (
              <Alert variant="destructive" data-testid="login-error">
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            ) : null}
            <Button
              data-testid="login-submit"
              type="submit"
              loading={submitting}
              className="w-full"
            >
              Sign in
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
