// Gaia's icon set: 24×24, stroked, drawn for this UI. Each icon is a list
// of shapes so that it renders without innerHTML.
import type { ReactNode } from "react";

type Shape = ["p", string] | ["c", number, number, number] | ["r", number, number, number, number, number?];

const DOC = "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z";
const HEART = "M19.5 13.6 12 21l-7.5-7.4A5 5 0 1 1 12 6.6a5 5 0 1 1 7.5 7z";
const p = (...ds: string[]): Shape[] => ds.map((d) => ["p", d]);

export const ICONS = {
  heart: p(HEART),
  "heart-crack": p(HEART, "m12 6.6-1.6 4.4 3.2 1.6-1.6 4.4"),
  sync: p("M21 12a9 9 0 0 0-15.4-6.4L3 8", "M3 3v5h5", "M3 12a9 9 0 0 0 15.4 6.4L21 16", "M16 16h5v5"),
  "check-circle": [["c", 12, 12, 10], ...p("m8 12.5 2.8 2.8L16.5 9.5")],
  "x-circle": [["c", 12, 12, 10], ...p("m15 9-6 6", "m9 9 6 6")],
  "arrow-up-circle": [["c", 12, 12, 10], ...p("m16 12-4-4-4 4", "M12 16V8")],
  alert: p("M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z", "M12 9v4", "M12 17h.01"),
  clock: [["c", 12, 12, 10], ...p("M12 6v6l4 2")],
  commit: [["c", 12, 12, 3.5], ...p("M2 12h6.5", "M15.5 12H22")],
  diff: p(DOC, "M14 2v6h6", "M12 10.5v5", "M9.5 13h5", "M9.5 18h5"),
  info: [["c", 12, 12, 10], ...p("M12 16v-4", "M12 8h.01")],
  history: p("M3 12a9 9 0 1 0 2.6-6.4L3 8", "M3 3v5h5", "M12 7v5l3.5 2"),
  code: p("m16 18 6-6-6-6", "m8 6-6 6 6 6"),
  terminal: p("m4 17 6-6-6-6", "M12 19h8"),
  file: p(DOC, "M14 2v6h6", "M8 13h8", "M8 17h8", "M8 9h2"),
  play: [["c", 12, 12, 10], ...p("m10 8 6 4-6 4z")],
  rerun: p("M3 12a9 9 0 1 0 9-9 9.8 9.8 0 0 0-6.7 2.7L3 8", "M3 3v5h5"),
  undo: p("M9 14 4 9l5-5", "M4 9h10.5a5.5 5.5 0 0 1 0 11H11"),
  scan: [...p("M3 7V5a2 2 0 0 1 2-2h2", "M17 3h2a2 2 0 0 1 2 2v2", "M21 17v2a2 2 0 0 1-2 2h-2", "M7 21H5a2 2 0 0 1-2-2v-2"), ["c", 11.5, 11.5, 3.5], ...p("m16 16-2-2")],
  user: [["c", 12, 8, 4], ...p("M4 21a8 8 0 0 1 16 0")],
  plus: p("M12 5v14", "M5 12h14"),
  minus: p("M5 12h14"),
  same: [["c", 12, 12, 5]],
  check: p("M20 6 9 17l-5-5"),
  x: p("M18 6 6 18", "M6 6l12 12"),
  pencil: p("M17 3a2.8 2.8 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5z"),
  filter: p("M22 3H2l8 9.5V19l4 2v-8.5z"),
  search: [["c", 11, 11, 7], ...p("m21 21-4.3-4.3")],
  pr: [["c", 18, 18, 3], ["c", 6, 6, 3], ...p("M13 6h3a2 2 0 0 1 2 2v7", "M6 9v12")],
  grid: [["r", 3, 3, 7, 7, 1], ["r", 14, 3, 7, 7, 1], ["r", 14, 14, 7, 7, 1], ["r", 3, 14, 7, 7, 1]],
  book: p("M4 19.5A2.5 2.5 0 0 1 6.5 17H20V2H6.5A2.5 2.5 0 0 0 4 4.5z", "M4 19.5A2.5 2.5 0 0 0 6.5 22H20v-5"),
  plug: p("M12 22v-5", "M9 8V2", "M15 8V2", "M18 8v5a6 6 0 0 1-12 0V8z"),
  repeat: p("m17 2 4 4-4 4", "M3 11v-1a4 4 0 0 1 4-4h14", "m7 22-4-4 4-4", "M21 13v1a4 4 0 0 1-4 4H3"),
  compass: [["c", 12, 12, 10], ...p("m16.2 7.8-2.1 6.3-6.3 2.1 2.1-6.3z")],
  server: [["r", 2, 3, 20, 8, 2], ["r", 2, 13, 20, 8, 2], ...p("M6 7h.01", "M6 17h.01", "M10 7h4", "M10 17h4")],
  vm: [["r", 2, 3, 20, 14, 2], ...p("M8 21h8", "M12 17v4", "m8 8 3 2-3 2", "M13 12h3")],
  network: [["r", 16, 16, 6, 6, 1], ["r", 2, 16, 6, 6, 1], ["r", 9, 2, 6, 6, 1], ...p("M5 16v-3a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v3", "M12 12V8")],
  activity: p("M22 12h-4l-3 9L9 3l-3 9H2"),
  wrench: p("M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.8-3.8a6 6 0 0 1-7.9 7.9l-6.9 6.9a2.1 2.1 0 0 1-3-3l6.9-6.9a6 6 0 0 1 7.9-7.9z"),
  key: [["c", 7.5, 15.5, 5.5], ...p("m21 2-9.6 9.6", "m15.5 7.5 3 3L22 7l-3-3")],
  plan: [["r", 8, 2, 8, 4, 1], ...p("M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2", "M12 11h4", "M12 16h4", "M8 11h.01", "M8 16h.01")],
  help: [["c", 12, 12, 10], ...p("M9.1 9a3 3 0 0 1 5.8 1c0 2-3 3-3 3", "M12 17h.01")],
  pin: [...p("M20 10c0 6-8 12-8 12s-8-6-8-12a8 8 0 0 1 16 0z"), ["c", 12, 10, 3]],
  disc: [["c", 12, 12, 10], ["c", 12, 12, 2.5]],
  size: p("M15 3h6v6", "M9 21H3v-6", "M21 3l-7 7", "M3 21l7-7"),
  layers: p("m12 2 10 5-10 5L2 7z", "m2 17 10 5 10-5", "m2 12 10 5 10-5"),
  lock: [["r", 4, 11, 16, 11, 2], ...p("M8 11V7a4 4 0 0 1 8 0v4")],
  bell: p("M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9", "M10.3 21a1.9 1.9 0 0 0 3.4 0"),
  eye: [...p("M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7-10-7-10-7z"), ["c", 12, 12, 3]],
  arrow: p("M5 12h14", "m13 6 6 6-6 6"),
  logout: p("M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4", "m16 17 5-5-5-5", "M21 12H9"),
  stop: [["c", 12, 12, 10], ["r", 9, 9, 6, 6, 1]],
  box: p("M21 8 12 3 3 8v8l9 5 9-5z", "m3 8 9 5 9-5", "M12 13v8"),
  gear: [...p("M12.2 2h-.4a2 2 0 0 0-2 2v.2a2 2 0 0 1-1 1.7l-.4.3a2 2 0 0 1-2 0l-.2-.1a2 2 0 0 0-2.7.7l-.2.4a2 2 0 0 0 .7 2.7l.2.1a2 2 0 0 1 1 1.7v.5a2 2 0 0 1-1 1.7l-.2.1a2 2 0 0 0-.7 2.7l.2.4a2 2 0 0 0 2.7.7l.2-.1a2 2 0 0 1 2 0l.4.3a2 2 0 0 1 1 1.7v.2a2 2 0 0 0 2 2h.4a2 2 0 0 0 2-2v-.2a2 2 0 0 1 1-1.7l.4-.3a2 2 0 0 1 2 0l.2.1a2 2 0 0 0 2.7-.7l.2-.4a2 2 0 0 0-.7-2.7l-.2-.1a2 2 0 0 1-1-1.7v-.5a2 2 0 0 1 1-1.7l.2-.1a2 2 0 0 0 .7-2.7l-.2-.4a2 2 0 0 0-2.7-.7l-.2.1a2 2 0 0 1-2 0l-.4-.3a2 2 0 0 1-1-1.7V4a2 2 0 0 0-2-2z"), ["c", 12, 12, 3]],
  users: [["c", 9, 7, 4], ...p("M2 21v-2a4 4 0 0 1 4-4h6a4 4 0 0 1 4 4v2", "M16 3.1a4 4 0 0 1 0 7.8", "M22 21v-2a4 4 0 0 0-3-3.9")],
  calendar: [["r", 3, 4, 18, 18, 2], ...p("M16 2v4", "M8 2v4", "M3 10h18")],
  shield: p("M20 13c0 5-3.5 7.5-7.7 9a1 1 0 0 1-.6 0C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.2-2.7a1.2 1.2 0 0 1 1.6 0C14.5 3.8 17 5 19 5a1 1 0 0 1 1 1z"),
  zap: p("M13 2 4 14h7l-1 8 9-12h-7z"),
} satisfies Record<string, Shape[]>;

export type IconName = keyof typeof ICONS;

function shape(s: Shape, i: number): ReactNode {
  switch (s[0]) {
    case "p":
      return <path key={i} d={s[1]} />;
    case "c":
      return <circle key={i} cx={s[1]} cy={s[2]} r={s[3]} />;
    case "r":
      return <rect key={i} x={s[1]} y={s[2]} width={s[3]} height={s[4]} rx={s[5] ?? 2} />;
  }
}

export function Icon({ name, size = 16, fill = "none", width = 2, color, title }: {
  name: IconName;
  size?: number;
  fill?: string;
  width?: number;
  color?: string;
  title?: string;
}) {
  return (
    <svg className="icon" width={size} height={size} viewBox="0 0 24 24" fill={fill} stroke={color ?? "currentColor"}
      strokeWidth={width} strokeLinecap="round" strokeLinejoin="round" aria-hidden={title ? undefined : true}
      role={title ? "img" : undefined} aria-label={title}>
      {(ICONS[name] as Shape[]).map(shape)}
    </svg>
  );
}

// The run record's shield: filled green with a white check.
export function Shield({ size = 18 }: { size?: number }) {
  return (
    <svg className="icon" width={size} height={size} viewBox="0 0 24 24" aria-hidden="true">
      <path d="M12 2.2 4.2 5.1v5.8c0 5 3.3 9.4 7.8 10.9 4.5-1.5 7.8-5.9 7.8-10.9V5.1L12 2.2z" fill="var(--dgreen)" stroke="var(--dgreen)" strokeWidth="1" strokeLinejoin="round" />
      <path d="m8.3 12.2 2.6 2.6 5-5.4" fill="none" stroke="var(--white)" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

export function Logo({ size = 26 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 26 26" aria-hidden="true">
      <circle cx="13" cy="13" r="11" fill="none" stroke="var(--yellow)" strokeWidth="2" />
      <path d="M5 15c4-1 6-6 16-5" fill="none" stroke="var(--aqua)" strokeWidth="2" />
    </svg>
  );
}
