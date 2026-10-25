// A line diff for showing what a settings change does to an object's
// YAML: the longest common subsequence, as kept, removed and added lines.
export type DiffLine = { op: "same" | "add" | "del"; text: string };

export function lineDiff(before: string, after: string): DiffLine[] {
  const a = before === "" ? [] : before.replace(/\n$/, "").split("\n");
  const b = after === "" ? [] : after.replace(/\n$/, "").split("\n");
  const n = a.length, m = b.length;
  const lcs: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i]![j] = a[i] === b[j] ? lcs[i + 1]![j + 1]! + 1 : Math.max(lcs[i + 1]![j]!, lcs[i]![j + 1]!);
    }
  }
  const out: DiffLine[] = [];
  let i = 0, j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      out.push({ op: "same", text: a[i]! });
      i++;
      j++;
    } else if (lcs[i + 1]![j]! >= lcs[i]![j + 1]!) {
      out.push({ op: "del", text: a[i++]! });
    } else {
      out.push({ op: "add", text: b[j++]! });
    }
  }
  while (i < n) out.push({ op: "del", text: a[i++]! });
  while (j < m) out.push({ op: "add", text: b[j++]! });
  return out;
}
