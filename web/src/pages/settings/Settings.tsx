// Ayarlar: Gaia's own settings, one tab each (design 17.8). Providers,
// notification channels, sources and discovery, and the platform-wide
// objects: sign-in, Platform and Policy.
import type { ReactNode } from "react";
import { useQueries } from "@tanstack/react-query";
import { Link, Navigate, useParams } from "react-router-dom";
import { api } from "../../api/client";
import { Icon, type IconName } from "../../ui/icons";
import { Hint, Loading, Mono } from "../../ui/parts";
import { Page } from "../../ui/shell";
import { TabHead, Tabs } from "../../ui/tabs";
import { kindOf, objectPath } from "../../ui/kinds";
import { ObjectsError } from "../../ui/objects";
import { Providers } from "./Providers";
import { Sources } from "./Sources";
import { Discovery } from "./Discovery";
import { Versions } from "./Versions";
import { Users } from "./Users";
import { fromSettings, useObjects, useSettings } from "../../api/objects";
import { EditButtons } from "../../ui/settings-editor";

// ObjectYAML shows each object of a kind as it is written, for the
// kinds there are one or two of.
function ObjectYAML({ kind, text, hint }: { kind: string; text: string; hint: string }) {
  const list = useObjects([kind]);
  const details = useQueries({
    queries: (list.data?.objects ?? []).map((o) => ({
      queryKey: ["catalog", o.kind, o.name],
      queryFn: () => api<{ yaml: string; source: string }>(`/catalog/${encodeURIComponent(o.kind)}/${encodeURIComponent(o.name)}`),
    })),
  });
  return (
    <>
      <TabHead icon={kindOf(kind).icon} text={text} hint={hint} />
      {list.isPending ? <Loading /> : list.error ? <ObjectsError error={list.error} /> : list.data.objects.length === 0 ? <div className="card empty">Tanımlı bir {kind} yok.</div> : (
        list.data.objects.map((o, i) => (
          <section key={o.name} className="card" aria-label={`${kind} ${o.name}`}>
            <div className="card-head">
              <div className="card-title"><Icon name={kindOf(kind).icon} size={16} /><h2>{o.name}</h2></div>
              <span className="row" style={{ gap: 14 }}>
                <span className="il muted" style={{ fontWeight: 400 }}><Icon name={fromSettings(o.source) ? "history" : "file"} size={14} /><Mono>{o.source.replace(/^settings:/, "")}</Mono></span>
                <Link to={objectPath(kind, o.name)} className="il">Ayrıntı<Icon name="arrow" size={14} /></Link>
                {fromSettings(o.source) && <EditButtons kind={kind} name={o.name} />}
              </span>
            </div>
            {details[i]?.data ? <pre className="mono" style={{ margin: 0, padding: 16, overflowX: "auto", color: "var(--fg2)", lineHeight: 1.6 }}>{details[i].data.yaml}</pre> : <Loading />}
          </section>
        ))
      )}
    </>
  );
}

type Tab = { tab: string; label: string; icon: IconName; body: () => ReactNode };

const TABS: Tab[] = [
  { tab: "providers", label: "Provider'lar", icon: "plug", body: () => <Providers /> },
  { tab: "sources", label: "Kaynaklar", icon: "repeat", body: () => <Sources /> },
  { tab: "discovery", label: "Keşif", icon: "compass", body: () => <Discovery /> },
  { tab: "users", label: "Kullanıcılar", icon: "users", body: () => <Users /> },
  { tab: "auth", label: "Kimlik doğrulama", icon: "key", body: () => <><AuthSummary /><ObjectYAML kind="AuthConfig" text="Giriş ayarlarının kendisi." hint="AuthConfig, kişilerin nasıl girdiğini söyler. Değiştirmek bir ayar değişikliğidir ve platform-admin onayı ister." /></> },
  { tab: "channels", label: "Bildirim kanalları", icon: "bell", body: () => <Providers channels /> },
  {
    tab: "platform", label: "Platform", icon: "grid",
    body: () => <ObjectYAML kind="Platform" text="Platformun genel ayarları: motorlar, izleme, arşiv, depolar." hint="Platform nesnesi Gaia'nın kendisini nasıl çalıştıracağını söyler: Pulumi ve Ansible sürümleri, run kayıtlarının arşivi, izleme, artifact depoları." />,
  },
  {
    tab: "policy", label: "Policy", icon: "lock",
    body: () => <ObjectYAML kind="Policy" text="Kurallar: onaylar, isimlendirme, zorunlu etiketler, ortamlar, roller." hint="Policy, kontratların uyması gereken kuralları ve onay akışını söyler: hangi ortamda kimin onayı gerekir, sunucular nasıl adlandırılır, hangi etiketler zorunludur, ayar değişikliklerini kim onaylar." />,
  },
  { tab: "versions", label: "Sürümler", icon: "history", body: () => <Versions /> },
];

type AuthSpec = {
  url?: string; require?: string;
  providers?: { name: string; type: string; purpose?: string; directory?: { provider: string } }[];
  sessions?: { ui?: { ttl?: string; idle?: string }; cli?: { accessTtl?: string; refreshTtl?: string } };
};

// AuthSummary says in a few lines how people sign in.
function AuthSummary() {
  const q = useObjects(["AuthConfig"]);
  const spec = q.data?.objects[0]?.spec as AuthSpec | undefined;
  if (!spec) return null;
  const ldap = spec.providers?.filter((p) => p.type === "ldap") ?? [];
  const local = spec.providers?.find((p) => p.type === "local");
  return (
    <section className="status" aria-label="Giriş">
      <div className="status-cell">
        <div className="status-title"><Icon name="users" size={14} /><span>Giriş</span><Hint>Kişiler dizindeki kullanıcı adı ve parolasıyla girer; Gaia parolayı saklamaz, dizinde doğrular.</Hint></div>
        <div className="status-value" style={{ fontSize: 15 }}>
          {ldap.length ? ldap.map((p) => <span key={p.name} className="il">Dizin · <Link to={objectPath("Provider", p.directory?.provider ?? "")}>{p.directory?.provider}</Link></span>) : "Yerel hesaplar"}
        </div>
        <div className="status-sub">{local ? (ldap.length ? "yerel hesaplar sadece break-glass" : "yerel hesaplar") : "yerel hesap yok"}</div>
      </div>
      <div className="status-cell">
        <div className="status-title"><Icon name="key" size={14} /><span>İkinci adım</span><Hint>Paroladan sonra Gaia'nın kendi ikinci adımı istenir; ilk girişte orada kaydedilir.</Hint></div>
        <div className="status-value" style={{ fontSize: 15 }}>{spec.require === "passkey" ? "Passkey" : "Passkey ya da doğrulama kodu"}</div>
        <div className="status-sub">require: {spec.require}</div>
      </div>
      <div className="status-cell">
        <div className="status-title"><Icon name="clock" size={14} /><span>Oturum</span></div>
        <div className="status-value" style={{ fontSize: 15 }}>{spec.sessions?.ui?.ttl ?? "8h"} · boşta {spec.sessions?.ui?.idle ?? "30m"}</div>
        <div className="status-sub">CLI: {spec.sessions?.cli?.accessTtl ?? "1h"} / {spec.sessions?.cli?.refreshTtl ?? "12h"}</div>
      </div>
      <div className="status-cell">
        <div className="status-title"><Icon name="arrow" size={14} /><span>Adres</span><Hint right>Passkey'ler bu adresin alan adına bağlıdır.</Hint></div>
        <div className="status-value mono" style={{ fontSize: 14 }}>{spec.url}</div>
      </div>
    </section>
  );
}


export function Settings() {
  const { tab = "" } = useParams();
  const settings = useSettings();
  const t = TABS.find((x) => x.tab === tab);
  if (!t) return <Navigate to="/settings/providers" replace />;
  return (
    <Page crumbs={<><Link to="/settings">Ayarlar</Link> / {t.label}</>}>
      <div className="title-row">
        <h1>Ayarlar</h1>
        <Hint>Gaia'nın kendi ayarları: konuştuğu sistemler, kaynaklar, giriş, platform ve kurallar.</Hint>
        {settings.data?.current && (
          <Link to={`/settings/versions/${settings.data.current.version}`} className="il muted" style={{ fontWeight: 400 }} title="Gaia'nın şu an kullandığı ayar sürümü">
            <Icon name="history" size={15} />ayarlar @ v{settings.data.current.version}
          </Link>
        )}
        {settings.data && !settings.data.current && <span className="il muted" style={{ fontWeight: 400 }}><Icon name="commit" size={15} />ayarlar Git'ten okunuyor</span>}
      </div>
      <Tabs label="Ayarlar" items={TABS.map((x) => ({ to: `/settings/${x.tab}`, icon: x.icon, label: x.label }))} />
      {t.body()}
    </Page>
  );
}
