// The resource tree drawn with React Flow and laid out by ELK.
import { useEffect, useState } from "react";
import { Background, Handle, Position, ReactFlow, type Edge, type Node, type NodeProps } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import ELK from "elkjs/lib/elk.bundled.js";
import type { TreeEdge, TreeNode } from "../lib/tree";
import { Icon } from "./icons";

const W = 272;
const H = 56;
const TONE: Record<TreeNode["tone"], string> = { green: "var(--green)", yellow: "var(--yellow)", red: "var(--red)", gray: "var(--gray)" };

type ResNode = Node<{ n: TreeNode }, "res">;

function Res({ data }: NodeProps<ResNode>) {
  const n = data.n;
  const tone = TONE[n.tone];
  return (
    <div style={{ width: W, height: H, boxSizing: "border-box", padding: "0 12px", display: "flex", alignItems: "center", gap: 10,
      background: n.dashed ? "var(--bg0)" : "var(--bg0s)", border: `1px ${n.dashed ? "dashed var(--bg3)" : "solid var(--bg2)"}`,
      borderLeft: `4px solid ${tone}`, borderRadius: 6, fontSize: 13, lineHeight: "18px", color: "var(--fg)" }}>
      <Handle type="target" position={Position.Left} style={{ opacity: 0 }} />
      <span style={{ flex: "0 0 auto", width: 34, height: 34, borderRadius: 7, background: "var(--bg1)", border: "1px solid var(--bg2)", display: "flex", alignItems: "center", justifyContent: "center", color: n.color }}>
        <Icon name={n.icon} size={18} width={2.2} />
      </span>
      <span style={{ minWidth: 0 }}>
        <b style={{ display: "block", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={n.name}>{n.name}</b>
        <span style={{ display: "flex", alignItems: "center", gap: 4, color: "var(--fg4)", fontSize: 12, whiteSpace: "nowrap" }}>
          {n.kind} ·<span style={{ color: tone, display: "inline-flex", alignItems: "center", gap: 3 }}><Icon name={n.statusIcon} size={12} width={2.4} fill={n.statusIcon === "heart" ? tone : "none"} />{n.status}</span>
        </span>
      </span>
      <Handle type="source" position={Position.Right} style={{ opacity: 0 }} />
    </div>
  );
}

const nodeTypes = { res: Res };
const elk = new ELK();

async function layout(nodes: TreeNode[], edges: TreeEdge[]) {
  const g = await elk.layout({
    id: "root",
    layoutOptions: {
      "elk.algorithm": "layered",
      "elk.direction": "RIGHT",
      "elk.layered.spacing.nodeNodeBetweenLayers": "100",
      "elk.spacing.nodeNode": "20",
      "elk.layered.nodePlacement.strategy": "BRANDES_KOEPF",
    },
    children: nodes.map((n) => ({ id: n.id, width: W, height: H })),
    edges: edges.map((e) => ({ id: e.id, sources: [e.source], targets: [e.target] })),
  });
  const pos = new Map((g.children ?? []).map((c) => [c.id, { x: c.x ?? 0, y: c.y ?? 0 }]));
  const height = Math.max(H, ...[...pos.values()].map((p) => p.y + H));
  return { pos, height };
}

export function ResourceTree({ nodes, edges }: { nodes: TreeNode[]; edges: TreeEdge[] }) {
  const [laid, setLaid] = useState<{ nodes: ResNode[]; height: number } | null>(null);
  useEffect(() => {
    let live = true;
    void layout(nodes, edges).then(({ pos, height }) => {
      if (!live) return;
      setLaid({ height, nodes: nodes.map((n) => ({ id: n.id, type: "res", position: pos.get(n.id) ?? { x: 0, y: 0 }, data: { n }, draggable: false, connectable: false })) });
    });
    return () => {
      live = false;
    };
  }, [nodes, edges]);
  const flowEdges: Edge[] = edges.map((e) => ({
    id: e.id, source: e.source, target: e.target, type: "smoothstep",
    style: { stroke: e.dashed ? "var(--yellow)" : "var(--bg4)", strokeWidth: 2, strokeDasharray: e.dashed ? "6 6" : undefined },
  }));
  if (!laid) return <div className="empty">Ağaç çiziliyor…</div>;
  return (
    <div style={{ height: Math.min(Math.max(laid.height + 48, 160), 720) }} aria-label="Kaynak ağacı" role="img">
      <ReactFlow nodes={laid.nodes} edges={flowEdges} nodeTypes={nodeTypes} colorMode="dark" fitView fitViewOptions={{ padding: 0.08, maxZoom: 1 }}
        nodesDraggable={false} nodesConnectable={false} elementsSelectable={false} zoomOnScroll={false} panOnScroll={false} preventScrolling={false}
        style={{ background: "transparent" }} minZoom={0.3} maxZoom={1.5}>
        <Background color="var(--bg1)" gap={24} />
      </ReactFlow>
    </div>
  );
}
