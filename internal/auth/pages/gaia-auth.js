// Gaia's sign-in pages: WebAuthn ceremonies against /api/v1/auth.
"use strict";

const b64 = {
  decode(s) {
    s = s.replace(/-/g, "+").replace(/_/g, "/");
    const bin = atob(s + "===".slice((s.length + 3) % 4));
    return Uint8Array.from(bin, (c) => c.charCodeAt(0)).buffer;
  },
  encode(buf) {
    const bin = String.fromCharCode(...new Uint8Array(buf));
    return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  },
};

async function post(path, body) {
  const r = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Gaia-Request": "1" },
    body: JSON.stringify(body || {}),
    credentials: "same-origin",
  });
  const data = r.status === 204 ? {} : await r.json();
  if (!r.ok) {
    const e = new Error(data.error || r.statusText);
    e.status = r.status;
    e.stepUp = data.stepUp === true;
    throw e;
  }
  return data;
}

function creationOptions(o) {
  const pk = o.publicKey;
  if (window.PublicKeyCredential && PublicKeyCredential.parseCreationOptionsFromJSON) {
    return { publicKey: PublicKeyCredential.parseCreationOptionsFromJSON(pk) };
  }
  pk.challenge = b64.decode(pk.challenge);
  pk.user.id = b64.decode(pk.user.id);
  (pk.excludeCredentials || []).forEach((c) => (c.id = b64.decode(c.id)));
  return { publicKey: pk };
}

function requestOptions(o) {
  const pk = o.publicKey;
  if (window.PublicKeyCredential && PublicKeyCredential.parseRequestOptionsFromJSON) {
    return { publicKey: PublicKeyCredential.parseRequestOptionsFromJSON(pk) };
  }
  pk.challenge = b64.decode(pk.challenge);
  (pk.allowCredentials || []).forEach((c) => (c.id = b64.decode(c.id)));
  return { publicKey: pk };
}

function credentialJSON(c) {
  if (typeof c.toJSON === "function") return c.toJSON();
  const r = c.response;
  const out = { id: c.id, rawId: b64.encode(c.rawId), type: c.type, clientExtensionResults: c.getClientExtensionResults(), response: { clientDataJSON: b64.encode(r.clientDataJSON) } };
  if (r.attestationObject) {
    out.response.attestationObject = b64.encode(r.attestationObject);
    if (r.getTransports) out.response.transports = r.getTransports();
  } else {
    out.response.authenticatorData = b64.encode(r.authenticatorData);
    out.response.signature = b64.encode(r.signature);
    if (r.userHandle) out.response.userHandle = b64.encode(r.userHandle);
  }
  return out;
}

// nextPath keeps redirects on this site.
function nextPath(v) {
  if (!v || !v.startsWith("/") || v.startsWith("//") || v.includes("\\")) return "/";
  return v;
}

async function signedIn() {
  const r = await fetch("/api/v1/me", { credentials: "same-origin" });
  if (r.status === 401) {
    location.href = "/login?next=" + encodeURIComponent(location.pathname + location.search);
    return null;
  }
  return r.json();
}

async function cliPage() {
  const me = await signedIn();
  if (!me) return;
  const q = new URLSearchParams(location.search);
  document.getElementById("who").textContent = me.username;
  document.getElementById("target").textContent = q.get("redirect_uri");
  document.getElementById("cli-approve").addEventListener("click", async () => {
    try {
      const out = await post("/api/v1/auth/cli/authorize", {
        redirectUri: q.get("redirect_uri"), state: q.get("state"), codeChallenge: q.get("code_challenge"),
      });
      document.getElementById("done").hidden = false;
      location.href = out.redirect;
    } catch (e) {
      showError(e);
    }
  });
}

async function devicePage() {
  if (!(await signedIn())) return;
  const form = document.getElementById("device-form");
  const q = new URLSearchParams(location.search);
  if (q.get("code")) form.code.value = q.get("code");
  let code = "";
  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    code = form.code.value.trim().toUpperCase();
    try {
      const r = await fetch("/api/v1/auth/device/" + encodeURIComponent(code), { credentials: "same-origin" });
      const info = await r.json();
      if (!r.ok) throw new Error(info.error);
      document.getElementById("device-ip").textContent = info.ip;
      document.getElementById("device-time").textContent = new Date(info.requestedAt).toLocaleString();
      document.getElementById("device-agent").textContent = info.userAgent;
      document.getElementById("device-check").hidden = false;
    } catch (e) {
      showError(e);
    }
  });
  const decide = (what) => async () => {
    try {
      await post("/api/v1/auth/device/" + encodeURIComponent(code) + "/" + what);
      document.getElementById("device-check").hidden = true;
      const done = document.getElementById("done");
      done.textContent = what === "approve" ? "Onaylandı. Terminale dönebilirsiniz." : "Reddedildi.";
      done.hidden = false;
    } catch (e) {
      showError(e);
    }
  };
  document.getElementById("device-approve").addEventListener("click", decide("approve"));
  document.getElementById("device-deny").addEventListener("click", decide("deny"));
}

function showError(e) {
  document.getElementById("error").textContent = e.message || String(e);
}

async function login() {
  try {
    const start = await post("/api/v1/auth/passkey/begin");
    const cred = await navigator.credentials.get(requestOptions(start.options));
    await post("/api/v1/auth/passkey/finish", { flow: start.flow, credential: credentialJSON(cred) });
    location.href = nextPath(new URLSearchParams(location.search).get("next"));
  } catch (e) {
    showError(e);
  }
}

async function recovery(ev) {
  ev.preventDefault();
  const f = new FormData(ev.target);
  try {
    await post("/api/v1/auth/recovery", { username: f.get("username"), code: f.get("code") });
    location.href = "/";
  } catch (e) {
    showError(e);
  }
}

async function enroll() {
  const token = location.hash.slice(1);
  try {
    const start = await post("/api/v1/auth/enroll/begin", { token });
    const cred = await navigator.credentials.create(creationOptions(start.options));
    const done = await post("/api/v1/auth/enroll/finish", {
      token, flow: start.flow, name: document.getElementById("name").value, credential: credentialJSON(cred),
    });
    showCodes(done.recoveryCodes);
    document.getElementById("enroll").disabled = true;
  } catch (e) {
    showError(e);
  }
}

async function passwordLogin(ev) {
  ev.preventDefault();
  const f = new FormData(ev.target);
  try {
    await post("/api/v1/auth/password", { username: f.get("username"), password: f.get("password"), code: f.get("code") });
    location.href = nextPath(new URLSearchParams(location.search).get("next"));
  } catch (e) {
    showError(e);
  }
}

function showCodes(codes) {
  history.replaceState(null, "", location.pathname);
  document.getElementById("codelist").textContent = codes.join("\n");
  document.getElementById("codes").hidden = false;
}

async function totpBegin() {
  try {
    const setup = await post("/api/v1/auth/enroll/totp/begin", { token: location.hash.slice(1) });
    document.getElementById("totp-secret").textContent = setup.secret;
    document.getElementById("totp-uri").href = setup.uri;
    document.getElementById("totp-form").hidden = false;
  } catch (e) {
    showError(e);
  }
}

async function totpFinish(ev) {
  ev.preventDefault();
  const f = new FormData(ev.target);
  try {
    const done = await post("/api/v1/auth/enroll/totp/finish", { token: location.hash.slice(1), password: f.get("password"), code: f.get("code") });
    showCodes(done.recoveryCodes);
  } catch (e) {
    showError(e);
  }
}

// step finishes an OIDC sign-in with Gaia's passkey: an existing one, or
// the person's first.
async function step() {
  try {
    const start = await post("/api/v1/auth/oidc/passkey/begin");
    const cred = start.register
      ? await navigator.credentials.create(creationOptions(start.options))
      : await navigator.credentials.get(requestOptions(start.options));
    const done = await post("/api/v1/auth/oidc/passkey/finish", { flow: start.flow, credential: credentialJSON(cred) });
    if (done.recoveryCodes) {
      showCodes(done.recoveryCodes);
      document.getElementById("codes-next").href = nextPath(done.next);
      return;
    }
    location.href = nextPath(done.next);
  } catch (e) {
    showError(e);
  }
}

document.addEventListener("DOMContentLoaded", async () => {
  if (document.getElementById("cli-approve")) return cliPage();
  if (document.getElementById("device-form")) return devicePage();
  const params = new URLSearchParams(location.search);
  try {
    const cfg = await (await fetch("/api/v1/auth/config")).json();
    if (cfg.require === "mfa") {
      for (const id of ["password", "totp"]) {
        const el = document.getElementById(id);
        if (el) el.hidden = false;
      }
    }
    const box = document.getElementById("providers");
    if (box) {
      for (const p of cfg.providers.filter((p) => p.type === "oidc")) {
        const a = document.createElement("a");
        a.href = "/auth/oidc/" + encodeURIComponent(p.name) + "/start?next=" + encodeURIComponent(params.get("next") || "/");
        a.textContent = p.name + " ile giriş";
        a.className = "btn alt";
        box.appendChild(a);
      }
    }
  } catch (e) {
    /* the page still offers passkeys */
  }
  if (params.get("step") === "passkey") {
    document.getElementById("intro").textContent = "Kimliğiniz doğrulandı. Girişi Gaia passkey'inizle tamamlayın.";
    document.getElementById("step").hidden = false;
    document.getElementById("step").addEventListener("click", step);
    document.getElementById("passkey").hidden = true;
    document.getElementById("providers").hidden = true;
  }
  const pw = document.getElementById("password");
  if (pw) pw.addEventListener("submit", passwordLogin);
  const tb = document.getElementById("totp-begin");
  if (tb) tb.addEventListener("click", totpBegin);
  const tf = document.getElementById("totp-form");
  if (tf) tf.addEventListener("submit", totpFinish);
  const p = document.getElementById("passkey");
  if (p) p.addEventListener("click", login);
  const r = document.getElementById("recovery");
  if (r) r.addEventListener("submit", recovery);
  const e = document.getElementById("enroll");
  if (e) e.addEventListener("click", enroll);
});
