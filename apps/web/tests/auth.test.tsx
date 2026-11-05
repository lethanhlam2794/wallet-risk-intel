import { useEffect } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  MemoryRouter,
  Navigate,
  Route,
  Routes,
  useLocation,
} from "react-router-dom";
import { apiFetch, onUnauthorized } from "@/lib/api";
import { AuthProvider, useAuth } from "@/auth/auth-context";
import { AuthGuard } from "@/auth/auth-guard";
import { AppShell } from "@/app-shell";
import { LoginPage } from "@/features/login/login-page";

const fetchMock = vi.fn<typeof fetch>();

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function LocationDisplay() {
  const loc = useLocation();
  return <div data-testid="location">{`${loc.pathname}${loc.search}`}</div>;
}

function ProtectedStub() {
  return <div>protected-stub</div>;
}

// Fires an authenticated call on mount — used to exercise the mid-session
// 401 path (stored token rejected by the server).
function ProbeStub() {
  const { token } = useAuth();
  useEffect(() => {
    void apiFetch("/api/v1/admin/provider-configs", { token }).catch(
      () => {},
    );
  }, [token]);
  return <div>protected-stub</div>;
}

function renderApp(
  initialEntries: string[] = ["/login"],
  protectedElement: React.ReactNode = <ProtectedStub />,
) {
  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <AuthProvider>
        <Routes>
          <Route
            path="/login"
            element={
              <>
                <LoginPage />
                <LocationDisplay />
              </>
            }
          />
          <Route element={<AuthGuard />}>
            <Route element={<AppShell />}>
              <Route
                path="/providers"
                element={
                  <>
                    {protectedElement}
                    <LocationDisplay />
                  </>
                }
              />
            </Route>
          </Route>
          <Route path="*" element={<Navigate to="/providers" replace />} />
        </Routes>
      </AuthProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockReset();
  sessionStorage.clear();
  localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const LOGIN_OK = {
  data: {
    token: "jwt.token.here",
    token_type: "Bearer",
    expires_at: "2026-11-05T20:00:00Z",
    user: { username: "superadmin", role: "superadmin" },
  },
};

describe("login flow", () => {
  it("200 stores the session in sessionStorage only and navigates to /providers", async () => {
    fetchMock.mockResolvedValue(json(LOGIN_OK));
    renderApp();
    await userEvent.type(
      screen.getByTestId("login-username-input"),
      "superadmin",
    );
    await userEvent.type(
      screen.getByTestId("login-password-input"),
      "s3cret-pw",
    );
    await userEvent.click(screen.getByTestId("login-submit"));
    await waitFor(() =>
      expect(sessionStorage.getItem("cw.admin.token")).toBe(
        "jwt.token.here",
      ),
    );
    await screen.findByText("protected-stub");
    expect(screen.getByTestId("location")).toHaveTextContent("/providers");
    expect(localStorage.getItem("cw.admin.token")).toBeNull();
    // The login request hits /auth/login with credentials in the body —
    // never an Authorization header.
    const call = fetchMock.mock.calls[0]!;
    expect(String(call[0])).toBe("/api/v1/auth/login");
    const init = call[1]!;
    const headers = new Headers(init.headers);
    expect(headers.get("Authorization")).toBeNull();
    expect(headers.get("X-API-Key")).toBeNull();
    const body = JSON.parse(String(init.body)) as Record<string, string>;
    expect(body).toEqual({ username: "superadmin", password: "s3cret-pw" });
    expect(screen.getByTestId("session-user")).toHaveTextContent(
      "superadmin",
    );
  });

  it("401 shows inline error, stays on /login, never fires the unauthorized handler (A1)", async () => {
    fetchMock.mockResolvedValue(
      json(
        {
          error: {
            code: "INVALID_CREDENTIALS",
            message: "invalid username or password",
          },
        },
        401,
      ),
    );
    renderApp();
    // Registered after AuthProvider mounts, so it is the live single-slot
    // handler — proving the login call skips the 401 side effect entirely.
    const unauthorizedSpy = vi.fn();
    const off = onUnauthorized(unauthorizedSpy);
    await userEvent.type(
      screen.getByTestId("login-username-input"),
      "superadmin",
    );
    await userEvent.type(
      screen.getByTestId("login-password-input"),
      "wrong",
    );
    await userEvent.click(screen.getByTestId("login-submit"));
    expect(await screen.findByTestId("login-error")).toHaveTextContent(
      /invalid username or password/i,
    );
    expect(unauthorizedSpy).not.toHaveBeenCalled();
    expect(sessionStorage.getItem("cw.admin.token")).toBeNull();
    expect(screen.getByTestId("location")).toHaveTextContent("/login");
    expect(screen.getByTestId("location")).not.toHaveTextContent(
      "session_expired",
    );
    off();
  });

  it("400 shows the server error message", async () => {
    fetchMock.mockResolvedValue(
      json({ error: { code: "BAD_REQUEST", message: "username required" } }, 400),
    );
    renderApp();
    await userEvent.type(
      screen.getByTestId("login-username-input"),
      "superadmin",
    );
    await userEvent.type(
      screen.getByTestId("login-password-input"),
      "x",
    );
    await userEvent.click(screen.getByTestId("login-submit"));
    expect(await screen.findByTestId("login-error")).toHaveTextContent(
      /username required/i,
    );
    expect(sessionStorage.getItem("cw.admin.token")).toBeNull();
  });

  it("5xx shows a retryable service-unreachable error", async () => {
    fetchMock.mockResolvedValue(
      json({ error: { code: "INTERNAL", message: "boom" } }, 500),
    );
    renderApp();
    await userEvent.type(
      screen.getByTestId("login-username-input"),
      "u",
    );
    await userEvent.type(screen.getByTestId("login-password-input"), "p");
    await userEvent.click(screen.getByTestId("login-submit"));
    expect(await screen.findByTestId("login-error")).toHaveTextContent(
      /unreachable|retry/i,
    );
  });

  it("network failure shows the retryable error", async () => {
    fetchMock.mockRejectedValue(new TypeError("fetch failed"));
    renderApp();
    await userEvent.type(
      screen.getByTestId("login-username-input"),
      "u",
    );
    await userEvent.type(screen.getByTestId("login-password-input"), "p");
    await userEvent.click(screen.getByTestId("login-submit"));
    expect(await screen.findByTestId("login-error")).toHaveTextContent(
      /unreachable|retry/i,
    );
  });
});

describe("guard, logout, and session expiry", () => {
  it("redirects unauthenticated users to /login", async () => {
    renderApp(["/providers"]);
    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent("/login"),
    );
    expect(await screen.findByTestId("login-form")).toBeInTheDocument();
  });

  it("logout clears the key and returns to /login", async () => {
    sessionStorage.setItem("cw.admin.token", "k");
    renderApp(["/providers"]);
    await screen.findByText("protected-stub");
    await userEvent.click(screen.getByTestId("logout-button"));
    expect(sessionStorage.getItem("cw.admin.token")).toBeNull();
    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent("/login"),
    );
  });

  it("mid-session 401 drops the key and redirects with reason=session_expired", async () => {
    sessionStorage.setItem("cw.admin.token", "stale-key");
    fetchMock.mockResolvedValue(
      json({ error: { code: "UNAUTHORIZED", message: "expired" } }, 401),
    );
    renderApp(["/providers"], <ProbeStub />);
    await waitFor(() =>
      expect(sessionStorage.getItem("cw.admin.token")).toBeNull(),
    );
    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent(
        "/login?reason=session_expired",
      ),
    );
    expect(
      await screen.findByText(/session expired/i),
    ).toBeInTheDocument();
  });
});
