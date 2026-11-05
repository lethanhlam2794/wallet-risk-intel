import { Outlet } from "react-router-dom";
import { Button } from "@/components";
import { useAuth } from "@/auth/auth-context";

export function AppShell() {
  const { logout, user } = useAuth();
  return (
    <div className="min-h-svh bg-background text-foreground">
      <header
        data-testid="app-header"
        className="flex items-center justify-between border-b px-4 py-2"
      >
        <span className="font-heading text-base font-medium">
          Check Wallet Admin
        </span>
        <div className="flex items-center gap-3">
          {user ? (
            <span
              data-testid="session-user"
              className="text-sm text-muted-foreground"
            >
              {user.username}
            </span>
          ) : null}
          <Button
            data-testid="logout-button"
            variant="outline"
            size="sm"
            onClick={logout}
          >
            Logout
          </Button>
        </div>
      </header>
      <main className="mx-auto w-full max-w-6xl p-4">
        <Outlet />
      </main>
    </div>
  );
}
