// The YAML diff of a change: what changed in Git for its contract since
// it was last applied.
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api } from "../../api/client";
import { Icon } from "../../ui/icons";
import { kindOf, objectPath } from "../../ui/kinds";
import { Badge, ErrorNote, Hint, Loading, Mono, shortLabel, type Tone } from "../../ui/parts";

type ObjectDiff = { kind: string; name: string; file: string; status: "added" | "modified" | "removed"; diff: string };
type Diff = { from?: string; to: string; objects: ObjectDiff[] };

const STATUS: Record<ObjectDiff["status"], { tone: Tone; label: string }> = {
  added: { tone: "green", label: "eklendi" },
  modified: { tone: "yellow", label: "değişti" },
  removed: { tone: "red", label: "silindi" },
};

function lineStyle(l: string): React.CSSProperties | undefined {
  if (l.startsWith("@@")) return { color: "var(--blue)" };
  if (l.startsWith("+")) return { background: "var(--tint-green)", color: "var(--green)" };
  if (l.startsWith("-")) return { background: "var(--tint-red)", color: "var(--red)" };
  return undefined;
}

export function YamlDiff({ id, contract }: { id: string; contract: string }) {
  const q = useQuery({ queryKey: ["change", id, "diff"], queryFn: () => api<Diff>(`/changes/${encodeURIComponent(id)}/diff`) });
  if (q.isPending) return <Loading />;
  if (q.error) return <div className="card-body"><ErrorNote error={q.error} /></div>;
  const d = q.data;
  return (
    <div className="card-body" style={{ display: "flex", flexDirection: "column", gap: 16 }}>
      <div className="row muted" style={{ gap: 8 }}>
        <Icon name="commit" size={15} />
        {d.from ? <>uygulanan <Mono>{shortLabel(d.from)}</Mono><Icon name="arrow" size={13} /><Mono>{shortLabel(d.to)}</Mono></> : <>ilk uygulama: <Mono>{shortLabel(d.to)}</Mono></>}
        <Hint width={320}>Kontratın son uygulandığı commit'ten bu değişikliğin commit'ine Git'te değişenler. Kontratın kendisi, referans verdiği bütün katalog nesneleri (Site, Network, Image…) ile Policy ve Platform gösterilir.</Hint>
      </div>
      {d.objects.length === 0 && <div className="empty">Git'te {contract} kontratını etkileyen bir fark yok; değişiklik aynı tanımı yeniden uyguluyor.</div>}
      {d.objects.map((o) => {
        const lines = o.diff.split("\n").filter((l) => !l.startsWith("---") && !l.startsWith("+++"));
        while (lines.length && lines[lines.length - 1] === "") lines.pop();
        const link = objectPath(o.kind, o.name);
        return (
          <section key={o.kind + "/" + o.name} aria-label={`${o.kind}/${o.name}`}>
            <div className="row" style={{ gap: 8, marginBottom: 8 }}>
              <span className="faint"><Icon name={o.kind === "Server" ? "file" : kindOf(o.kind).icon} size={15} /></span>
              <Link to={link}><span className="faint">{o.kind}/</span>{o.name}</Link>
              <Badge tone={STATUS[o.status].tone}>{STATUS[o.status].label}</Badge>
              <Mono><span className="faint">{o.file}</span></Mono>
            </div>
            <pre className="mono" style={{ margin: 0, background: "var(--bg0h)", border: "1px solid var(--bg1)", borderRadius: 6, padding: "10px 0", lineHeight: 1.6, overflowX: "auto", color: "var(--fg2)" }}>
              {lines.map((l, i) => <span key={i} style={{ display: "block", padding: "0 12px", ...lineStyle(l) }}>{l || " "}</span>)}
            </pre>
          </section>
        );
      })}
    </div>
  );
}
