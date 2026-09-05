import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { Layout } from "@/components/Layout";
import { Spinner } from "@/components/ui/feedback";
import { useAuth } from "@/hooks/useAuth";
import { ApiKeys } from "@/pages/ApiKeys";
import { Audit } from "@/pages/Audit";
import { Connections } from "@/pages/Connections";
import { NewFinding } from "@/pages/NewFinding";
import { RecentTickets } from "@/pages/RecentTickets";
import { SignIn } from "@/pages/SignIn";

/**
 * RequireToken keeps a visitor without a token out of the application shell.
 *
 * A usability guard, not a security one: every endpoint verifies the token
 * itself, so removing this would leak nothing. It exists so that someone
 * arriving without one sees the identity picker rather than a page of failed
 * requests.
 */
function RequireToken({ children }: { children: React.ReactNode }) {
  const { me, loading } = useAuth();
  const location = useLocation();

  if (loading) return <Spinner label="Loading…" />;
  if (!me) return <Navigate to="/signin" replace state={{ from: location.pathname }} />;
  return <>{children}</>;
}

export function App() {
  const { me, loading } = useAuth();

  return (
    <Routes>
      <Route
        path="/signin"
        element={
          loading ? (
            <Spinner label="Loading…" />
          ) : me ? (
            <Navigate to="/findings/new" replace />
          ) : (
            <SignIn />
          )
        }
      />

      <Route
        element={
          <RequireToken>
            <Layout />
          </RequireToken>
        }
      >
        <Route path="/findings/new" element={<NewFinding />} />
        <Route path="/tickets" element={<RecentTickets />} />
        <Route path="/connections" element={<Connections />} />
        <Route path="/api-keys" element={<ApiKeys />} />
        <Route path="/audit" element={<Audit />} />
        <Route path="/" element={<Navigate to="/findings/new" replace />} />
      </Route>

      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}
