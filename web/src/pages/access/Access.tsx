// Erişim: who may log in to which servers (design 14.3): the access
// groups, the teams they are made of, and each contract's access entries.
import { useQuery } from "@tanstack/react-query";
import { Link, Navigate, useParams } from "react-router-dom";
import { api } from "../../api/client";
import { field, str, strs, useObjects, type CatalogObj } from "../../api/objects";
import { Icon } from "../../ui/icons";
import { Badge, Card, ErrorNote, Hint, Leg, Loading, Mono } from "../../ui/parts";
import { Page } from "../../ui/shell";
import { Tabs } from "../../ui/tabs";
import { dash, ObjectTable, ObjectsError, Ref, type Col } from "../../ui/objects";
import { AddButton } from "../../ui/settings-editor";

type Entry = { contract: string; group?: string; team?: string; sudo?: boolean; commands?: string[] };

function Refs({ kind, names }: { kind: string; names: string[] }) {
  if (names.length === 0) return dash;
  return <span className="row" style={{ gap: 10 }}>{names.map((n) => <Ref key={n} kind={kind} name={n} />)}</span>;
}

function dn(o: CatalogObj, path: string) {
  const v = str(field(o.spec, path));
  return v ? <Mono>{v}</Mono> : dash;
}

const GROUP_COLS: Col[] = [
  { title: "Açıklama", cell: (o) => str(field(o.spec, "description")) || dash },
  { title: "Takımlar", cell: (o) => <Refs kind="Team" names={strs(field(o.spec, "teams"))} /> },
  { title: "Gruplar", hint: "İçine aldığı başka erişim grupları.", cell: (o) => <Refs kind="Group" names={strs(field(o.spec, "groups"))} /> },
  { title: "Sahip", hint: "Grubun üyeliğini değiştirmeyi onaylayan takım.", cell: (o) => <Ref kind="Team" name={str(field(o.spec, "owner"))} /> },
  { title: "Ayrıcalıklı", hint: "Ayrıcalıklı bir gruba eklenen sudo, değişikliği platform-admin onayına götürür.", cell: (o) => (field(o.spec, "privileged") === true ? <Badge tone="orange" icon="shield">ayrıcalıklı</Badge> : dash) },
];

const TEAM_COLS: Col[] = [
  { title: "Görünen ad", cell: (o) => str(field(o.spec, "displayName")) || dash },
  { title: "Dizin grubu", hint: "Üyelerin geldiği dizin grubu.", cell: (o) => dn(o, "adGroup.dn") },
  { title: "Onaycılar", hint: "Takımın değişikliklerini onaylayanların dizin grubu.", cell: (o) => dn(o, "approversGroup.dn") },
  { title: "Sır öneki", hint: "Takımın kontratlarının başvurabileceği OpenBao yolu.", cell: (o) => dn(o, "secrets.prefix") },
];

function SudoCell({ e }: { e: Entry }) {
  if (e.sudo) return <Badge tone="orange" icon="key">tam sudo</Badge>;
  if (e.commands?.length) {
    return (
      <span style={{ display: "flex", flexDirection: "column", gap: 4 }}>
        {e.commands.map((c) => <span key={c} className="il" style={{ fontWeight: 400 }}><span className="faint"><Icon name="terminal" size={13} /></span><Mono>{c}</Mono></span>)}
      </span>
    );
  }
  return <span className="faint">sudo yok</span>;
}

function ContractAccess() {
  const q = useQuery({ queryKey: ["access"], queryFn: () => api<Entry[]>("/access") });
  return (
    <Card title="Kontrat erişimi" icon="file" hint={<>Her kontratın sunucularına kimin girebildiği ve root olarak ne çalıştırabildiği. Kontratın <b className="term">access</b> alanından okunur.
      <Leg icon="key" color="var(--orange)" name="tam sudo">her komutu root olarak çalıştırabilir</Leg>
      <Leg icon="terminal" color="var(--fg4)" name="komut">sadece listelenen komutları root olarak çalıştırabilir</Leg>
      <Leg icon="minus" color="var(--fg4)" name="sudo yok">girebilir, root olamaz</Leg></>}
      right={<span className="faint">{q.data?.length ?? 0} kayıt</span>}>
      {q.isPending ? <Loading /> : q.error ? <ObjectsError error={q.error} /> : q.data.length === 0 ? <div className="empty">Erişim tanımlayan kontrat yok.</div> : (
        <table className="grid">
          <thead><tr><th>Kontrat</th><th>Kim</th><th>Sudo</th></tr></thead>
          <tbody>
            {q.data.map((e, i) => {
              const first = i === 0 || q.data[i - 1]!.contract !== e.contract;
              return (
                <tr key={i} style={{ verticalAlign: "top" }}>
                  <td style={first ? undefined : { borderTopColor: "transparent" }}>{first && <Link to={`/contracts/${encodeURIComponent(e.contract)}`} className="il"><Icon name="file" size={14} />{e.contract}</Link>}</td>
                  <td>{e.group ? <Ref kind="Group" name={e.group} /> : <Ref kind="Team" name={e.team ?? ""} />}</td>
                  <td><SudoCell e={e} /></td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </Card>
  );
}

const TABS = [
  { tab: "contracts", label: "Kontrat erişimi", icon: "file" as const },
  { tab: "Group", label: "Group'lar", icon: "shield" as const },
  { tab: "Team", label: "Takımlar", icon: "users" as const },
];

export function Access() {
  const { tab = "" } = useParams();
  const q = useObjects(["Group", "Team"]);
  const t = TABS.find((x) => x.tab === tab);
  if (!t) return <Navigate to="/access/contracts" replace />;
  const objs = (k: string) => (q.data?.objects ?? []).filter((o) => o.kind === k);
  return (
    <Page crumbs={<><Link to="/access">Erişim</Link> / {t.label}</>}>
      <div className="title-row">
        <h1>Erişim</h1>
        <Hint width={340}>Sunuculara kimin girebildiği. Kontrat erişimi bir Group'a ya da takıma verir; Group takımların birleşimidir; takımların üyeleri dizinden gelir.</Hint>
      </div>
      <Tabs label="Erişim" items={TABS.map((x) => ({ to: `/access/${x.tab}`, icon: x.icon, label: x.label, count: x.tab === "contracts" ? undefined : q.data ? objs(x.tab).length : undefined }))} />
      {tab === "contracts" ? <ContractAccess />
        : q.isPending ? <Loading />
        : q.error ? <ErrorNote error={q.error} />
        : tab === "Group"
          ? <ObjectTable kind="Group" title="Group'lar" hint="Erişim grupları: takımların (ve başka grupların) birleşimi. Kontratlar erişimi bunlara verir." cols={GROUP_COLS} objs={objs("Group")} right={<AddButton kind="Group" label="Group ekle" />} />
          : <ObjectTable kind="Team" title="Takımlar" hint="Takımlar ve onaycıları. Bir TeamSource varsa dizindeki gruplardan üretilir." cols={TEAM_COLS} objs={objs("Team")} right={<AddButton kind="Team" label="Takım ekle" />} />}
    </Page>
  );
}
