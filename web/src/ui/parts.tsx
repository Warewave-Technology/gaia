// The building blocks every screen uses: icon labels, badges, buttons,
// the "?" hint, cards and Argo CD's status panel.
import { useId, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Icon, type IconName } from "./icons";

export type Tone = "green" | "yellow" | "red" | "blue" | "orange" | "gray";

export const toneColor: Record<Tone, string> = {
  green: "var(--green)",
  yellow: "var(--yellow)",
  red: "var(--red)",
  blue: "var(--blue)",
  orange: "var(--orange)",
  gray: "var(--fg4)",
};

// An icon and its words in one colour.
export function IL({ icon, children, color, size = 16, fill, weight }: {
  icon: IconName;
  children?: ReactNode;
  color?: string;
  size?: number;
  fill?: string;
  weight?: number;
}) {
  return (
    <span className="il" style={{ color, fontWeight: weight }}>
      <Icon name={icon} size={size} fill={fill} />
      {children}
    </span>
  );
}

export function Badge({ tone, icon, children }: { tone: Tone; icon?: IconName; children: ReactNode }) {
  return (
    <span className={`badge ${tone}`}>
      {icon && <Icon name={icon} size={13} />}
      {children}
    </span>
  );
}

type BtnKind = "plain" | "primary" | "danger" | "solid-danger";

export function Button({ kind = "plain", icon, children, onClick, disabled, title, type = "button" }: {
  kind?: BtnKind;
  icon?: IconName;
  children: ReactNode;
  onClick?: () => void;
  disabled?: boolean;
  title?: string;
  type?: "button" | "submit";
}) {
  return (
    <button type={type} className={kind === "plain" ? "btn" : `btn ${kind}`} onClick={onClick} disabled={disabled} title={title}>
      {icon && <Icon name={icon} size={15} />}
      {children}
    </button>
  );
}

export function LinkButton({ to, icon, children, kind = "plain", title }: {
  to: string;
  icon?: IconName;
  children: ReactNode;
  kind?: BtnKind;
  title?: string;
}) {
  return (
    <Link to={to} className={kind === "plain" ? "btn" : `btn ${kind}`} title={title}>
      {icon && <Icon name={icon} size={15} />}
      {children}
    </Link>
  );
}

// A tooltip opened by hovering or focusing its trigger.
export function Tip({ trigger, label, children, right, width }: {
  trigger: ReactNode;
  label: string;
  children: ReactNode;
  right?: boolean;
  width?: number;
}) {
  const id = useId();
  return (
    <span className={right ? "tt r" : "tt"}>
      <span className="tt-ic" tabIndex={0} role="img" aria-label={label} aria-describedby={id}>{trigger}</span>
      <span className="tip" role="tooltip" id={id}>
        <span className="tipbox" style={width ? { width } : undefined}>{children}</span>
      </span>
    </span>
  );
}

// The "?" that says what the thing next to it is.
export function Hint({ children, right, width }: { children: ReactNode; right?: boolean; width?: number }) {
  const id = useId();
  return (
    <span className={right ? "tt r" : "tt"}>
      <span className="tt-ic hq" tabIndex={0} role="img" aria-label="Bu nedir?" aria-describedby={id}><Icon name="help" size={15} /></span>
      <span className="tip" role="tooltip" id={id}>
        <span className="tipbox" style={width ? { width } : undefined}>{children}</span>
      </span>
    </span>
  );
}

// One line of an icon legend inside a hint.
export function Leg({ icon, color, name, children, fill }: {
  icon: IconName;
  color: string;
  name: ReactNode;
  children?: ReactNode;
  fill?: string;
}) {
  return (
    <span className="leg">
      <span style={{ color }}><Icon name={icon} size={15} fill={fill} /></span>
      <span><b style={{ color, fontWeight: 600 }}>{name}</b> {children}</span>
    </span>
  );
}

export function Term({ children }: { children: ReactNode }) {
  return <b className="term">{children}</b>;
}

export function Card({ title, icon, hint, right, children, id }: {
  title: ReactNode;
  icon?: IconName;
  hint?: ReactNode;
  right?: ReactNode;
  children: ReactNode;
  id?: string;
}) {
  const auto = useId();
  const hid = id ?? auto;
  return (
    <section className="card" aria-labelledby={hid}>
      <div className="card-head">
        <div className="card-title">
          {icon && <Icon name={icon} size={16} />}
          <h2 id={hid}>{title}</h2>
          {hint && <Hint>{hint}</Hint>}
        </div>
        {right}
      </div>
      {children}
    </section>
  );
}

export type StatusItem = {
  icon: IconName;
  title: string;
  hint?: ReactNode;
  value: ReactNode;
  sub?: ReactNode;
};

// Argo CD's status panel: side by side, each with its own "?".
export function StatusBar({ items }: { items: StatusItem[] }) {
  return (
    <section className="status" aria-label="Durum">
      {items.map((it, i) => (
        <div className="status-cell" key={it.title}>
          <div className="status-title">
            <Icon name={it.icon} size={14} />
            <span>{it.title}</span>
            {it.hint && <Hint right={i === items.length - 1}>{it.hint}</Hint>}
          </div>
          <div className="status-value">{it.value}</div>
          {it.sub && <div className="status-sub">{it.sub}</div>}
        </div>
      ))}
    </section>
  );
}

export function Mono({ children }: { children: ReactNode }) {
  return <span className="mono">{children}</span>;
}

// A sha shortened the way the screens show it.
export function short(sha: string | null | undefined, n = 7): string {
  return sha ? sha.slice(0, n) : "";
}

// shortLabel shortens each commit of a snapshot label ("platform contracts").
export function shortLabel(label: string | null | undefined): string {
  return (label ?? "").split(/\s+/).filter(Boolean).map((x) => (/^[0-9a-f]{12,}$/.test(x) ? x.slice(0, 7) : x)).join(" ");
}

export function Loading() {
  return <div className="empty" role="status">Yükleniyor…</div>;
}

export function ErrorNote({ error }: { error: unknown }) {
  const msg = error instanceof Error ? error.message : String(error);
  return <div className="notice err" role="alert">{msg}</div>;
}
