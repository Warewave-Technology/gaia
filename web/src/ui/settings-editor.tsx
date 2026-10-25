// Editing Gaia's settings (design 17.3): a modal with a form for the
// common kinds and the YAML for every kind. Saving proposes a settings
// change; Gaia checks it first and shows what is wrong and which
// contracts' plans it changes.
import { useState, type FormEvent, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { isMap, isSeq, parseDocument, type Document } from "yaml";
import { api, ApiError } from "../api/client";
import { hasRole, useSession } from "../api/session";
import { useSettings } from "../api/objects";
import { checkSettings, propose, type Check, type Edit } from "../api/proposals";
import { Icon } from "./icons";
import { Badge, Button, Hint } from "./parts";
import { Modal } from "./modal";
import { kindOf, objectPath } from "./kinds";

type Field = {
  path: string[];
  label: string;
  type?: "text" | "number" | "list" | "bool" | "select";
  options?: string[];
  hint?: string;
  placeholder?: string;
  mono?: boolean;
};

const s = (...p: string[]) => ["spec", ...p];

// FORMS are the fields a form shows; the rest of an object stays as its
// YAML says.
const FORMS: Record<string, { fields: Field[]; note?: string }> = {
  Size: { fields: [
    { path: s("cpu"), label: "vCPU", type: "number" },
    { path: s("memoryGiB"), label: "Bellek (GiB)", type: "number" },
    { path: s("diskGiB"), label: "Disk (GiB)", type: "number" },
  ] },
  Network: { fields: [
    { path: s("site"), label: "Site" },
    { path: s("ipam", "provider"), label: "IPAM provider'ı" },
    { path: s("ipam", "pool"), label: "IPAM havuzu", mono: true, placeholder: "10.50.0.0/24" },
    { path: s("gateway"), label: "Ağ geçidi", mono: true },
    { path: s("attachment", "portgroup"), label: "vSphere portgroup", hint: "Bağlantı: portgroup, bridge (VLAN ile) ya da vnet'ten biri." },
    { path: s("attachment", "bridge"), label: "Proxmox bridge" },
    { path: s("attachment", "vlan"), label: "VLAN", type: "number" },
    { path: s("attachment", "vnet"), label: "Proxmox SDN VNet" },
    { path: s("dns", "servers"), label: "DNS sunucuları", type: "list", mono: true },
    { path: s("allowedEnvironments"), label: "Ortamlar", type: "list", hint: "Boşsa her ortam kullanabilir." },
    { path: s("description"), label: "Açıklama" },
  ] },
  Site: { fields: [
    { path: s("compute", "provider"), label: "Compute provider'ı" },
    { path: s("compute", "datacenter"), label: "Datacenter (vSphere)" },
    { path: s("compute", "cluster"), label: "Küme" },
    { path: s("compute", "folder"), label: "Klasör (vSphere)" },
    { path: s("compute", "nodes"), label: "Node'lar (Proxmox)", type: "list" },
    { path: s("compute", "pool"), label: "Havuz (Proxmox)" },
    { path: s("ntp"), label: "NTP sunucuları", type: "list", mono: true },
    { path: s("zabbixProxy"), label: "Zabbix proxy" },
  ], note: "Depolama ve VM ID aralığı YAML görünümünde." },
  Image: { fields: [
    { path: s("os"), label: "İşletim sistemi", type: "select", options: ["linux", "windows"] },
    { path: s("connection"), label: "Bağlantı", type: "select", options: ["ssh", "winrm"] },
  ], note: "Şablonlar YAML görünümünde." },
  Group: { fields: [
    { path: s("description"), label: "Açıklama" },
    { path: s("teams"), label: "Takımlar", type: "list" },
    { path: s("groups"), label: "Gruplar", type: "list", hint: "İçine aldığı başka erişim grupları." },
    { path: s("owner"), label: "Sahip takım" },
    { path: s("privileged"), label: "Ayrıcalıklı", type: "bool", hint: "Ayrıcalıklı gruba verilen sudo platform-admin onayı ister." },
  ] },
  Team: { fields: [
    { path: s("displayName"), label: "Görünen ad" },
    { path: s("adGroup", "dn"), label: "Dizin grubu (DN)", mono: true },
    { path: s("approversGroup", "dn"), label: "Onaycıların dizin grubu (DN)", mono: true },
    { path: s("secrets", "prefix"), label: "Sır öneki", mono: true, placeholder: "kv/teams/web/" },
  ], note: "Bildirim abonelikleri YAML görünümünde." },
  Provider: { fields: [
    { path: s("type"), label: "Tür", type: "select", options: ["activedirectory", "git", "ipam", "msteams", "nexus", "openbao", "postgres", "proxmox", "s3", "slack", "smtp", "vsphere", "warpgate", "webhook", "zabbix"] },
    { path: s("endpoint"), label: "Adres", mono: true },
    { path: s("url"), label: "URL (git)", mono: true },
    { path: s("credentials"), label: "Kimlik bilgisi", mono: true, placeholder: "bao://kv/platform/…", hint: "Sadece OpenBao referansı; sırrın kendisi buraya yazılmaz." },
  ] },
};

const NEW_SPEC: Record<string, string> = {
  Size: "spec:\n  cpu: 2\n  memoryGiB: 4\n  diskGiB: 40\n",
  Network: "spec:\n  site:\n  attachment: {}\n  ipam:\n    provider:\n    pool:\n",
  Site: "spec:\n  compute:\n    provider:\n",
  Image: "spec:\n  os: linux\n  connection: ssh\n  templates: []\n",
  Group: "spec:\n  teams: []\n  owner:\n",
  Team: "spec:\n  displayName:\n",
  Provider: "spec:\n  type:\n",
};

function template(kind: string): string {
  return `apiVersion: gaia/v1alpha1\nkind: ${kind}\nmetadata:\n  name:\n${NEW_SPEC[kind] ?? "spec: {}\n"}`;
}

// useCanPropose says whether the person may propose settings changes:
// operators and platform admins, once the settings store is in use.
export function useCanPropose(): boolean {
  const session = useSession();
  const settings = useSettings();
  return !!settings.data?.current && (hasRole(session, "operator") || hasRole(session, "platform-admin"));
}

function read(doc: Document, path: string[]): unknown {
  const v = doc.getIn(path, false) as unknown;
  if (isSeq(v)) return v.toJSON();
  if (isMap(v)) return v.toJSON();
  return v;
}

function FieldInput({ f, doc, set }: { f: Field; doc: Document; set: (v: unknown) => void }) {
  const v = read(doc, f.path);
  const label = <span>{f.label}{f.hint && <Hint width={260}>{f.hint}</Hint>}</span>;
  if (f.type === "bool") {
    return (
      <label className="field" style={{ flexDirection: "row", alignItems: "center", gap: 10 }}>
        <input type="checkbox" checked={v === true} onChange={(e) => set(e.target.checked ? true : undefined)} />
        {label}
      </label>
    );
  }
  if (f.type === "select") {
    return (
      <label className="field">{label}
        <select className="input" value={typeof v === "string" ? v : ""} onChange={(e) => set(e.target.value || undefined)}>
          <option value="">–</option>
          {f.options?.map((o) => <option key={o} value={o}>{o}</option>)}
        </select>
      </label>
    );
  }
  if (f.type === "list") {
    const text = Array.isArray(v) ? v.join(", ") : "";
    return (
      <label className="field">{label}
        <input className={f.mono ? "input mono" : "input"} defaultValue={text} placeholder="virgülle ayırın"
          onBlur={(e) => { const xs = e.target.value.split(",").map((x) => x.trim()).filter(Boolean); set(xs.length ? xs : undefined); }} />
      </label>
    );
  }
  return (
    <label className="field">{label}
      <input className={f.mono ? "input mono" : "input"} type={f.type === "number" ? "number" : "text"} value={v === undefined || v === null ? "" : String(v)} placeholder={f.placeholder}
        onChange={(e) => { const t = e.target.value; set(t === "" ? undefined : f.type === "number" ? Number(t) : t); }} />
    </label>
  );
}

function CheckResult({ c }: { c: Check }) {
  return (
    <div className="check" aria-live="polite">
      {c.errors.length > 0 ? (
        <div className="notice err" role="alert" style={{ margin: 0 }}>
          <b>Bu haliyle önerilemez.</b>
          <ul style={{ margin: "6px 0 0", paddingLeft: 18 }}>{c.errors.map((e) => <li key={e}>{e}</li>)}</ul>
        </div>
      ) : (
        <div className="notice" style={{ margin: 0 }}>
          <span className="il"><Icon name="check-circle" size={15} color="var(--green)" />Ayarlar bu değişiklikle yükleniyor.</span>
          <div style={{ marginTop: 6 }}>
            {c.impact.length === 0 ? <span className="muted">Hiçbir kontratın planı değişmiyor.</span> : (
              <>
                <span className="muted">Planı değişecek kontratlar ({c.impact.length}); uygulanınca her biri için olağan bir Change açılır:</span>
                <span className="row" style={{ gap: 6, marginTop: 6 }}>{c.impact.map((i) => <Link key={i.contract} to={objectPath("Server", i.contract)} className="badge gray"><Icon name="file" size={12} />{i.contract}</Link>)}</span>
              </>
            )}
          </div>
          {c.critical && <div style={{ marginTop: 8 }}><Badge tone="orange" icon="shield">platform-admin onayı</Badge> <span className="muted">{c.whyCritical?.join("; ")}</span></div>}
        </div>
      )}
    </div>
  );
}

// SettingsEditor adds (no name) or changes an object of a settings kind.
export function SettingsEditor({ kind, name, onClose }: { kind: string; name?: string; onClose: () => void }) {
  const current = useQuery({
    queryKey: ["catalog", kind, name],
    queryFn: () => api<{ yaml: string }>(`/catalog/${encodeURIComponent(kind)}/${encodeURIComponent(name ?? "")}`),
    enabled: !!name,
  });
  if (name && current.isPending) return null;
  return <Editor kind={kind} name={name} initial={name ? current.data?.yaml ?? "" : template(kind)} onClose={onClose} />;
}

function Editor({ kind, name, initial, onClose }: { kind: string; name?: string; initial: string; onClose: () => void }) {
  const form = FORMS[kind];
  const [view, setView] = useState<"form" | "yaml">(form ? "form" : "yaml");
  const [text, setText] = useState(initial);
  const [title, setTitle] = useState("");
  const [reason, setReason] = useState("");
  const [check, setCheck] = useState<Check | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const qc = useQueryClient();
  const nav = useNavigate();
  const doc = parseDocument(text);
  const parseError = doc.errors[0]?.message;
  const objName = String(read(doc, ["metadata", "name"]) ?? "");
  const set = (path: string[], v: unknown) => {
    const d = parseDocument(text);
    if (v === undefined) d.deleteIn(path);
    else d.setIn(path, v);
    setText(d.toString());
    setCheck(null);
  };
  const edits = (): Edit[] => [{ yaml: text }];
  const runCheck = async () => {
    setBusy(true);
    setError(null);
    try {
      setCheck(await checkSettings(edits()));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const p = await propose(title || `${kind}/${objName}: ${name ? "güncelle" : "ekle"}`, reason, edits());
      void qc.invalidateQueries({ queryKey: ["proposals"] });
      onClose();
      nav(`/changes/settings/${encodeURIComponent(p.id)}`);
    } catch (e) {
      if (e instanceof ApiError && e.status === 422) setCheck((e.data as { check: Check }).check);
      else setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const k = kindOf(kind);
  return (
    <Modal title={name ? `${kind}/${name} düzenle` : `Yeni ${kind}`} icon={k.icon} onClose={onClose} onSubmit={submit} width={760}
      footer={<>
        <Button onClick={onClose} disabled={busy}>Vazgeç</Button>
        <Button icon="check-circle" onClick={runCheck} disabled={busy || !!parseError}>Kontrol et</Button>
        <Button type="submit" kind="primary" icon="pr" disabled={busy || !!parseError || !objName}>{busy ? "Bekleniyor…" : "Öner"}</Button>
      </>}>
      <p className="muted" style={{ margin: 0 }}>Kaydetmek bir <b className="term">ayar değişikliği</b> önerir: Gaia önce kontrol eder, sonra bir onaycı (siz değil) onaylayınca yeni ayar sürümü olur.</p>
      <div className="spread">
        <span className="label-caps">Nesne</span>
        {form && (
          <div className="chips" role="tablist" aria-label="Görünüm">
            <button type="button" role="tab" className="chip" aria-pressed={view === "form"} aria-selected={view === "form"} disabled={!!parseError} onClick={() => setView("form")}><Icon name="plan" size={14} />Form</button>
            <button type="button" role="tab" className="chip" aria-pressed={view === "yaml"} aria-selected={view === "yaml"} onClick={() => setView("yaml")}><Icon name="code" size={14} />YAML</button>
          </div>
        )}
      </div>
      {view === "form" && form && !parseError ? (
        <>
          <div className="form-grid">
            <label className="field">
              <span>Ad</span>
              <input className="input mono" value={objName} readOnly={!!name} onChange={(e) => set(["metadata", "name"], e.target.value)} required />
            </label>
            {form.fields.map((f) => <FieldInput key={f.path.join(".")} f={f} doc={doc} set={(v) => set(f.path, v)} />)}
          </div>
          {form.note && <span className="faint" style={{ fontSize: 13 }}>{form.note}</span>}
        </>
      ) : (
        <label className="field">
          <span>YAML</span>
          <textarea className="input mono" rows={16} spellCheck={false} value={text} onChange={(e) => { setText(e.target.value); setCheck(null); }} />
          {parseError && <span className="error">{parseError}</span>}
        </label>
      )}
      <div className="modal-section">
        <span className="label-caps">Öneri</span>
        <div className="form-grid two">
          <label className="field"><span>Başlık</span><input className="input" value={title} onChange={(e) => setTitle(e.target.value)} placeholder={`${kind}/${objName || "…"}: ${name ? "güncelle" : "ekle"}`} /></label>
          <label className="field"><span>Neden</span><input className="input" value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Onaycının bilmesi gereken" /></label>
        </div>
      </div>
      {check && <CheckResult c={check} />}
      {error && <div className="error" role="alert">{error}</div>}
    </Modal>
  );
}

// RemoveSetting proposes removing an object.
export function RemoveSetting({ kind, name, onClose }: { kind: string; name: string; onClose: () => void }) {
  const [reason, setReason] = useState("");
  const [check, setCheck] = useState<Check | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const nav = useNavigate();
  const qc = useQueryClient();
  const edits: Edit[] = [{ kind, name, delete: true }];
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const p = await propose(`${kind}/${name}: kaldır`, reason, edits);
      void qc.invalidateQueries({ queryKey: ["proposals"] });
      onClose();
      nav(`/changes/settings/${encodeURIComponent(p.id)}`);
    } catch (e) {
      if (e instanceof ApiError && e.status === 422) setCheck((e.data as { check: Check }).check);
      else setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal title={`${kind}/${name} kaldır`} icon="x" tone="red" onClose={onClose} onSubmit={submit} width={560}
      footer={<>
        <Button onClick={onClose} disabled={busy}>Vazgeç</Button>
        <Button icon="check-circle" disabled={busy} onClick={async () => setCheck(await checkSettings(edits).catch(() => null))}>Kontrol et</Button>
        <Button type="submit" kind="solid-danger" icon="pr" disabled={busy}>{busy ? "Bekleniyor…" : "Kaldırmayı öner"}</Button>
      </>}>
      <p className="muted" style={{ margin: 0 }}>Kaldırmak da bir ayar değişikliğidir: onaylanınca yeni sürümde bu nesne olmaz. Kullanan bir şey varsa Gaia önermeye izin vermez.</p>
      <label className="field"><span>Neden</span><input className="input" value={reason} onChange={(e) => setReason(e.target.value)} /></label>
      {check && <CheckResult c={check} />}
      {error && <div className="error" role="alert">{error}</div>}
    </Modal>
  );
}

// AddButton opens the editor for a new object of a kind, on a card's top
// right; it is not there for who may not propose.
export function AddButton({ kind, label }: { kind: string; label?: string }) {
  const can = useCanPropose();
  const [open, setOpen] = useState(false);
  if (!can) return null;
  return (
    <>
      <Button icon="plus" onClick={() => setOpen(true)}>{label ?? "Ekle"}</Button>
      {open && <SettingsEditor kind={kind} onClose={() => setOpen(false)} />}
    </>
  );
}

// EditButtons are an object page's edit and remove, for settings kinds.
export function EditButtons({ kind, name, generatedBy }: { kind: string; name: string; generatedBy?: string }): ReactNode {
  const can = useCanPropose();
  const [open, setOpen] = useState<"edit" | "remove" | null>(null);
  if (!can) return null;
  const locked = generatedBy ? `${generatedBy} üretiyor; kaynağında değiştirin` : undefined;
  return (
    <span className="row">
      <Button icon="pencil" onClick={() => setOpen("edit")} disabled={!!locked} title={locked}>Düzenle</Button>
      <Button kind="danger" icon="x" onClick={() => setOpen("remove")} disabled={!!locked} title={locked}>Kaldır</Button>
      {open === "edit" && <SettingsEditor kind={kind} name={name} onClose={() => setOpen(null)} />}
      {open === "remove" && <RemoveSetting kind={kind} name={name} onClose={() => setOpen(null)} />}
    </span>
  );
}
