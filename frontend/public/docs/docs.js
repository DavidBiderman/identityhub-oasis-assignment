// The documentation page's own logic: sign in, then hand the token to
// swagger-ui.
//
// A file rather than an inline <script> because the Content-Security-Policy
// this application serves is `default-src 'self'` with no script-src override,
// which blocks inline execution. The alternatives were to allow
// 'unsafe-inline' -- which gives away most of what the policy is for -- or to
// maintain a hash that changes with every edit. Serving it from the same origin
// satisfies the policy with nothing given up.

// The same key the application uses, on the same origin. Signing in to
// either unlocks both, and signing out of either ends both -- one origin,
// one session, rather than two things a person has to keep in step.
const KEY = "identityhub.token";

const gate = document.getElementById("gate");
const loaded = document.getElementById("loaded");
const failure = document.getElementById("failure");

function show(error) {
  failure.textContent = error;
  failure.hidden = !error;
}

// Renders swagger-ui with the token already applied.
//
// requestInterceptor covers the fetch of the document itself, which
// happens before any Authorize state exists; preauthorizeApiKey fills in
// the accessToken scheme so "Try it out" works on the first click.
function render(token, claims) {
  document.getElementById("who").textContent =
    claims.email + " — " + (claims.roles || []).join(", ");

  const ui = SwaggerUIBundle({
    url: "/openapi.yaml",
    dom_id: "#swagger",
    presets: [SwaggerUIBundle.presets.apis],
    plugins: [SwaggerUIBundle.plugins.DownloadUrl],
    layout: "BaseLayout",
    deepLinking: true,
    persistAuthorization: true,
    tryItOutEnabled: true,
    defaultModelsExpandDepth: 0,
    docExpansion: "list",
    requestInterceptor: (request) => {
      // Same origin only. This token is ours and belongs nowhere else.
      if (request.url.startsWith("/") || request.url.startsWith(location.origin)) {
        request.headers.Authorization = "Bearer " + token;
      }
      return request;
    },
    onComplete: () => ui.preauthorizeApiKey("accessToken", token),
  });
  window.ui = ui;

  gate.hidden = true;
  loaded.hidden = false;
}

// A token is only useful here if the API still accepts it: it may have
// expired, or been signed out from the application in another tab.
async function claimsFor(token) {
  const response = await fetch("/api/auth/me", {
    headers: { Authorization: "Bearer " + token },
  });
  if (!response.ok) return null;
  return (await response.json()).data;
}

document.getElementById("signin").addEventListener("submit", async (event) => {
  event.preventDefault();
  show("");

  const button = document.getElementById("submit");
  button.disabled = true;
  try {
    const response = await fetch("/api/auth/signin", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        email: document.getElementById("email").value,
        password: document.getElementById("password").value,
      }),
    });
    const payload = await response.json();
    if (!response.ok) {
      // The API answers the same way for an unknown address and a wrong
      // password. Repeating its message keeps them indistinguishable here.
      show(payload.error ? payload.error.message : "Could not sign in.");
      return;
    }

    const token = payload.data.accessToken;
    sessionStorage.setItem(KEY, token);
    render(token, await claimsFor(token));
  } catch (error) {
    show("Could not reach the API. Is the stack running?");
  } finally {
    button.disabled = false;
  }
});

document.getElementById("signout").addEventListener("click", async () => {
  const token = sessionStorage.getItem(KEY);
  if (token) {
    // Tell the server first, so the token stops being accepted rather
    // than merely being forgotten here.
    await fetch("/api/auth/signout", {
      method: "POST",
      headers: { Authorization: "Bearer " + token },
    }).catch(() => undefined);
  }
  sessionStorage.removeItem(KEY);
  location.reload();
});

// Already signed in elsewhere on this origin? Then do not ask again.
(async () => {
  const token = sessionStorage.getItem(KEY);
  const claims = token ? await claimsFor(token) : null;
  if (claims) {
    render(token, claims);
  } else {
    sessionStorage.removeItem(KEY);
    gate.hidden = false;
  }
})();
