import { Navigate, Outlet } from "react-router-dom";
import { useAuth } from "./auth-context";

export function AuthGuard() {
  const { token, sessionExpired } = useAuth();
  if (!token) {
    return (
      <Navigate
        to={sessionExpired ? "/login?reason=session_expired" : "/login"}
        replace
      />
    );
  }
  return <Outlet />;
}
