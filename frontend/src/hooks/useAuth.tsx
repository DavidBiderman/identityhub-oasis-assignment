import * as React from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/lib/api";
import { clearToken, readToken, writeToken } from "@/lib/token";
import type { Me } from "@/lib/types";

interface AuthValue {
  /** What the current token asserts, or null when there is none. */
  me: Me | null;
  /** True until the first check of the token has resolved. */
  loading: boolean;
  /** True when the caller's token carries the admin role. */
  isAdmin: boolean;
  /** Accept a token issued by the identity provider. */
  useToken: (token: string) => void;
  /** Discard the token. The provider's session, if any, is unaffected. */
  discardToken: () => void;
}

const AuthContext = React.createContext<AuthValue | null>(null);

/**
 * AuthProvider holds the access token and what it asserts.
 *
 * This application authenticates nobody. It carries a token issued elsewhere
 * and asks the API what that token says — which is why there is no password
 * here, no session, and no sign-in request.
 */
export function AuthProvider({ children }: { children: React.ReactNode }) {
  const queryClient = useQueryClient();
  const [token, setToken] = React.useState<string | null>(() => readToken());

  const me = useQuery({
    queryKey: ["me", token],
    queryFn: async () => {
      if (!token) return null;
      try {
        return await api.get<Me>("/api/auth/me");
      } catch (error) {
        // An expired or rejected token is an ordinary state: drop it and show
        // the picker again rather than retrying something that cannot recover.
        if (error instanceof ApiError && error.isUnauthenticated) {
          clearToken();
          setToken(null);
          return null;
        }
        throw error;
      }
    },
    retry: false,
    staleTime: 60_000,
  });

  const value = React.useMemo<AuthValue>(
    () => ({
      me: me.data ?? null,
      loading: token !== null && me.isLoading,
      isAdmin: (me.data?.roles ?? []).includes("admin"),
      useToken: (next) => {
        writeToken(next);
        setToken(next);
        // Everything cached belongs to the previous identity.
        queryClient.clear();
      },
      discardToken: () => {
        // Tell the server first, so the token stops being accepted rather than
        // merely being forgotten here. A copy taken out of the browser before
        // this point would otherwise keep working until it expired.
        //
        // Not awaited, and a failure is not surfaced: the local half must
        // happen either way, and leaving somebody signed in because a request
        // failed would be the worse outcome. The token expires on its own.
        void api.post("/api/auth/signout").catch(() => undefined);

        clearToken();
        setToken(null);
        queryClient.clear();
      },
    }),
    [me.data, me.isLoading, token, queryClient],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthValue {
  const value = React.useContext(AuthContext);
  if (!value) throw new Error("useAuth must be used inside an AuthProvider");
  return value;
}
