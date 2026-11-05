import { Navigate, Route, Routes } from "react-router-dom";
import { AppShell } from "@/app-shell";
import { AuthGuard } from "@/auth/auth-guard";
import { LoginPage } from "@/features/login/login-page";
import { ProviderDetailPage } from "@/features/providers/provider-detail-page";
import { ProvidersPage } from "@/features/providers/providers-page";

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route element={<AuthGuard />}>
        <Route element={<AppShell />}>
          <Route path="/providers" element={<ProvidersPage />} />
          <Route path="/providers/:code" element={<ProviderDetailPage />} />
        </Route>
      </Route>
      <Route path="*" element={<Navigate to="/providers" replace />} />
    </Routes>
  );
}
