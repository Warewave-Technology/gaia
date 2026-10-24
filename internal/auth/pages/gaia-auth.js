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

// stepUp confirms the session with a passkey, or with a TOTP code for
// accounts that have none.
async function stepUp() {
  let start;
  try {
    start = await post("/api/v1/auth/step-up/begin");
  } catch (e) {
    if (e.status !== 409) throw e;
    const form = document.getElementById("stepup-totp");
    form.hidden = false;
    form.code.focus();
    await new Promise((resolve, reject) => {
      form.onsubmit = async (ev) => {
        ev.preventDefault();
        try {
          await post("/api/v1/auth/step-up/totp", { code: form.code.value });
          form.hidden = true;
          resolve();
        } catch (err) {
          reject(err);
        }
      };
    });
    return;
  }
  const cred = await navigator.credentials.get(requestOptions(start.options));
  await post("/api/v1/auth/step-up/finish", { flow: start.flow, credential: credentialJSON(cred) });
}

function cell(row, text, tag) {
  const td = document.createElement(tag || "td");
  td.textContent = text;
  td.style.cssText = "text-align:left;padding:6px 8px;border-bottom:1px solid #E1E5EB";
  row.appendChild(td);
}

function item(list, text) {
  const li = document.createElement("li");
  li.textContent = text;
  list.appendChild(li);
}

const gateText = {
  validation: "Kontrat planlanabiliyor",
  signature: "Commit'ler kayıtlı anahtarlarla imzalı",
  approval: "Onay",
  ok: "tamam",
  waiting: "bekliyor",
  failed: "engelli",
};

async function approvePage() {
  const me = await signedIn();
  if (!me) return;
  const id = decodeURIComponent(location.pathname.split("/").pop());
  const r = await fetch("/api/v1/changes/" + encodeURIComponent(id), { credentials: "same-origin" });
  const c = await r.json();
  if (!r.ok) return showError(new Error(c.error || r.statusText));
  document.getElementById("change-contract").textContent = c.contract;
  document.getElementById("change-id").textContent = c.id;
  document.getElementById("change-status").textContent = c.status;
  document.getElementById("change-plan").textContent = c.planHash;

  const table = document.getElementById("change-servers");
  const head = document.createElement("tr");
  ["İşlem", "Sunucu", "Site", "Ağ"].forEach((h) => cell(head, h, "th"));
  table.appendChild(head);
  for (const ct of (c.plan && c.plan.contracts) || []) {
    for (const s of ct.servers || []) {
      const row = document.createElement("tr");
      cell(row, s.action);
      cell(row, s.hostname);
      cell(row, s.site);
      cell(row, s.network);
      if (s.action === "delete") row.style.color = "#B3261E";
      table.appendChild(row);
    }
  }
  const commits = document.getElementById("change-commits");
  for (const cm of c.commits || []) {
    item(commits, cm.repo + " " + cm.commit.slice(0, 12) + " · " + (cm.problem || cm.signer));
  }
  const gates = document.getElementById("change-gates");
  let mine = false;
  const order = ["validation", "signature", "approval", "lock"];
  const sorted = (c.gates || []).slice().sort((a, b) => order.indexOf(a.gate) - order.indexOf(b.gate));
  for (const g of sorted) {
    let text = (gateText[g.gate] || g.gate) + ": " + (gateText[g.status] || g.status);
    if (g.gate === "approval") {
      for (const grp of (g.detail && g.detail.groups) || []) {
        text += " · " + grp.name + " (" + grp.why + "): " + (grp.approvedBy ? "onaylayan " + grp.approvedBy : "onaylayabilecekler: " + (grp.candidates || []).join(", "));
        if (!grp.approvedBy && (grp.candidates || []).includes(me.username)) mine = true;
      }
    }
    item(gates, text);
  }
  for (const a of c.approvals || []) {
    item(gates, a.username + " " + (a.decision === "approve" ? "onayladı" : "reddetti") + (a.planHash === c.planHash ? "" : " (eski plan)") + (a.comment ? ": " + a.comment : ""));
  }
  if (!mine || (c.status !== "pending" && c.status !== "approved")) return;
  document.getElementById("decide").hidden = false;

  const decide = (what) => async () => {
    document.getElementById("error").textContent = "";
    const body = { planHash: c.planHash, comment: document.getElementById("comment").value };
    if (what === "reject" && !body.comment.trim()) return showError(new Error("Reddetmek için bir not yazın."));
    const url = "/api/v1/changes/" + encodeURIComponent(c.id) + "/" + what;
    try {
      try {
        await post(url, body);
      } catch (e) {
        if (!e.stepUp) throw e;
        await stepUp();
        await post(url, body);
      }
      document.getElementById("decide").hidden = true;
      const done = document.getElementById("done");
      done.textContent = what === "approve" ? "Onaylandı." : "Reddedildi.";
      done.hidden = false;
    } catch (e) {
      showError(e);
    }
  };
  document.getElementById("approve").addEventListener("click", decide("approve"));
  document.getElementById("reject").addEventListener("click", decide("reject"));
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
  if (document.getElementById("approve-page")) return approvePage();
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
        a.style.cssText = "display:block;text-align:center;padding:12px;border:1px solid #C6CCD5;border-radius:6px;color:#14171C;text-decoration:none;font-weight:600";
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
