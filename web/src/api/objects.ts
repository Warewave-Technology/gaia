// Catalog objects as the list screens read them: by kinds, with specs.
import { useQuery } from "@tanstack/react-query";
import { api } from "./client";

export type CatalogObj = {
  kind: string;
  name: string;
  labels?: Record<string, string>;
  source: string;
  generatedBy?: string;
  patches?: number;
  usedBy: number;
  contracts: number;
  // With specs: the spec without params, and how many params it takes.
  spec?: Record<string, unknown>;
  params?: number;
};

export type CatalogList = { commit: string; objects: CatalogObj[] };

export function useObjects(kinds: string[]) {
  const k = kinds.join(",");
  return useQuery({ queryKey: ["catalog", "spec", k], queryFn: () => api<CatalogList>(`/catalog?kind=${k}&spec=1`) });
}

// field reads a dotted path from a spec ("compute.provider").
export function field(spec: Record<string, unknown> | undefined, path: string): unknown {
  let v: unknown = spec;
  for (const p of path.split(".")) {
    if (v === null || typeof v !== "object") return undefined;
    v = (v as Record<string, unknown>)[p];
  }
  return v;
}

export function str(v: unknown): string {
  if (v === undefined || v === null) return "";
  return typeof v === "string" ? v : typeof v === "number" || typeof v === "boolean" ? String(v) : JSON.stringify(v);
}

export function strs(v: unknown): string[] {
  if (Array.isArray(v)) return v.map(str);
  return v === undefined || v === null || v === "" ? [] : [str(v)];
}

// matches reads "key=value" (or just text) against an object's name,
// labels and the given text.
export function matches(o: { name: string; labels?: Record<string, string> }, q: string, text = ""): boolean {
  const t = q.trim().toLowerCase();
  if (!t) return true;
  return t.split(/\s+/).every((part) => {
    const [k, v] = part.split("=");
    if (v !== undefined) return (o.labels?.[k ?? ""] ?? "").toLowerCase() === v;
    return o.name.toLowerCase().includes(part) || text.toLowerCase().includes(part) || Object.entries(o.labels ?? {}).some(([a, b]) => `${a}=${b}`.toLowerCase().includes(part));
  });
}
