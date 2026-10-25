// Settings: Gaia's own settings, one tab each (design 17.8): providers,
// sources and discovery, people and sign-in, notification channels, the
// platform-wide objects, their versions and the audit log.
import type { ReactNode } from "react";
import { useQueries } from "@tanstack/react-query";
import { Link, Navigate, useParams } from "react-router-dom";
import { api } from "../../api/client";
import { fromSettings, useObjects, useSettings } from "../../api/objects";
import { useSchema } from "../../api/schema";
import { Icon, type IconName } from "../../ui/icons";
import { Hint, Loading } from "../../ui/parts";
import { Page } from "../../ui/shell";
import { TabHead, Tabs } from "../../ui/tabs";
import { kindOf, objectPath } from "../../ui/kinds";
import { ObjectsError } from "../../ui/objects";
import { EditButtons } from "../../ui/settings-editor";
import { SpecView } from "../../ui/spec";
import { YamlButton } from "../../ui/yaml";
import { Providers } from "./Providers";
import { Sources } from "./Sources";
import { Discovery } from "./Discovery";
import { Versions } from "./Versions";
import { Users } from "./Users";
import { Audit } from "./Audit";

// ObjectSettings shows each object of a kind field by field, for the
// kinds there is one of.
function ObjectSettings({ kind, text, hint }: { kind: string; text: string; hint: string }) {
  const list = useObjects([kind]);
  const schema = useSchema(kind);
  const details = useQueries({
    queries: (list.data?.objects ?? []).map((o) => ({
      queryKey: ["catalog", o.kind, o.name],
      queryFn: () => api<{ yaml: string; source: string; spec?: Record<string, unknown> }>(`/catalog/${encodeURIComponent(o.kind)}/${encodeURIComponent(o.name)}`),
    })),
  });
  return (
    <>
      <TabHead icon={kindOf(kind).icon} text={text} hint={hint} />
      {list.isPending ? <Loading /> : list.error ? <ObjectsError error={list.error} /> : list.data.objects.length === 0 ? <div className="card empty">There is no {kind}.</div> : (
        list.data.objects.map((o, i) => (
          <section key={o.name} className="card" aria-label={`${kind} ${o.name}`}>
            <div className="card-head">
              <div className="card-title"><Icon name={kindOf(kind).icon} size={16} /><h2><Link to={objectPath(kind, o.name)}>{o.name}</Link></h2>
                {!fromSettings(o.source) && <span className="badge gray" title={o.source}>in Git</span>}</div>
              <span className="row">
                {details[i]?.data && <YamlButton yaml={details[i].data.yaml} title={`${kind} ${o.name}`} />}
                {fromSettings(o.source) && <EditButtons kind={kind} name={o.name} />}
              </span>
            </div>
            {details[i]?.data ? <SpecView kind={kind} schema={schema.data} spec={details[i].data.spec ?? o.spec} /> : <Loading />}
          </section>
        ))
      )}
    </>
  );
}

type Tab = { tab: string; label: string; icon: IconName; body: () => ReactNode };

const TABS: Tab[] = [
  { tab: "providers", label: "Providers", icon: "plug", body: () => <Providers /> },
  { tab: "sources", label: "Sources", icon: "repeat", body: () => <Sources /> },
  { tab: "discovery", label: "Discovery", icon: "compass", body: () => <Discovery /> },
  { tab: "users", label: "Users", icon: "users", body: () => <Users /> },
  {
    tab: "auth", label: "Sign-in", icon: "key",
    body: () => <><AuthSummary /><ObjectSettings kind="AuthConfig" text="How people sign in, in full." hint="AuthConfig says how people sign in. Changing it is a settings change that a platform admin approves." /></>,
  },
  { tab: "channels", label: "Notifications", icon: "bell", body: () => <Providers channels /> },
  {
    tab: "platform", label: "Platform", icon: "grid",
    body: () => <ObjectSettings kind="Platform" text="How Gaia runs itself: engines, monitoring, archives, repositories." hint="The Platform object says how Gaia runs: the Pulumi and Ansible versions, where run records are archived, monitoring, artifact repositories." />,
  },
  {
    tab: "policy", label: "Policy", icon: "lock",
    body: () => <ObjectSettings kind="Policy" text="The rules: approvals, naming, required labels, environments, roles." hint="The Policy holds the rules contracts must follow and who approves what: per environment, server naming, required labels, Gaia's roles and settings approvers." />,
  },
  { tab: "versions", label: "Versions", icon: "history", body: () => <Versions /> },
  { tab: "audit", label: "Audit log", icon: "eye", body: () => <Audit /> },
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
    <section className="status" aria-label="Sign-in">
      <div className="status-cell">
        <div className="status-title"><Icon name="users" size={14} /><span>Sign-in</span><Hint>People sign in with their directory username and password. Gaia checks the password against the directory and never keeps it.</Hint></div>
        <div className="status-value" style={{ fontSize: 15 }}>
          {ldap.length ? ldap.map((p) => <span key={p.name} className="il">Directory · <Link to={objectPath("Provider", p.directory?.provider ?? "")}>{p.directory?.provider}</Link></span>) : "Local accounts"}
        </div>
        <div className="status-sub">{local ? (ldap.length ? "local accounts are for break-glass only" : "local accounts") : "no local accounts"}</div>
      </div>
      <div className="status-cell">
        <div className="status-title"><Icon name="key" size={14} /><span>Second factor</span><Hint>After the password Gaia asks for its own second factor; a first sign-in registers it.</Hint></div>
        <div className="status-value" style={{ fontSize: 15 }}>{spec.require === "passkey" ? "Passkey" : "Passkey or authenticator code"}</div>
        <div className="status-sub">require: {spec.require}</div>
      </div>
      <div className="status-cell">
        <div className="status-title"><Icon name="clock" size={14} /><span>Sessions</span></div>
        <div className="status-value" style={{ fontSize: 15 }}>{spec.sessions?.ui?.ttl ?? "8h"} · idle {spec.sessions?.ui?.idle ?? "30m"}</div>
        <div className="status-sub">CLI: {spec.sessions?.cli?.accessTtl ?? "1h"} / {spec.sessions?.cli?.refreshTtl ?? "12h"}</div>
      </div>
      <div className="status-cell">
        <div className="status-title"><Icon name="arrow" size={14} /><span>Address</span><Hint right>Passkeys are bound to this address's host.</Hint></div>
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
    <Page crumbs={<><Link to="/settings">Settings</Link> / {t.label}</>}>
      <div className="title-row">
        <h1>Settings</h1>
        <Hint>Gaia's own settings: the systems it talks to, its sources, sign-in, the platform and the rules.</Hint>
        {settings.data?.current && (
          <Link to={`/settings/versions/${settings.data.current.version}`} className="il muted" style={{ fontWeight: 400 }} title="The settings version Gaia uses now">
            <Icon name="history" size={15} />settings v{settings.data.current.version}
          </Link>
        )}
        {settings.data && !settings.data.current && <span className="il muted" style={{ fontWeight: 400 }}><Icon name="commit" size={15} />settings are read from Git</span>}
      </div>
      <Tabs label="Settings" items={TABS.map((x) => ({ to: `/settings/${x.tab}`, icon: x.icon, label: x.label }))} />
      {t.body()}
    </Page>
  );
}
