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
  if (!r.ok) throw new Error(data.error || r.statusText);
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

function showError(e) {
  document.getElementById("error").textContent = e.message || String(e);
}

async function login() {
  try {
    const start = await post("/api/v1/auth/passkey/begin");
    const cred = await navigator.credentials.get(requestOptions(start.options));
    await post("/api/v1/auth/passkey/finish", { flow: start.flow, credential: credentialJSON(cred) });
    location.href = new URLSearchParams(location.search).get("next") || "/";
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
    history.replaceState(null, "", location.pathname);
    document.getElementById("codelist").textContent = done.recoveryCodes.join("\n");
    document.getElementById("codes").hidden = false;
    document.getElementById("enroll").disabled = true;
  } catch (e) {
    showError(e);
  }
}

document.addEventListener("DOMContentLoaded", () => {
  const p = document.getElementById("passkey");
  if (p) p.addEventListener("click", login);
  const r = document.getElementById("recovery");
  if (r) r.addEventListener("submit", recovery);
  const e = document.getElementById("enroll");
  if (e) e.addEventListener("click", enroll);
});
