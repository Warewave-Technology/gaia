package plan

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// WriteText prints the plan for people: per contract its servers and what
// runs on them, then a summary per system. The commit is printed by whoever
// read it.
func WriteText(w io.Writer, p *Plan) error {
	pr := &printer{w: w}
	if len(p.Contracts) == 0 {
		pr.f("\nno Server contracts\n")
	}

	addrs := map[string]int{} // ipam provider -> count
	vms := map[string]int{}   // compute provider -> count
	configure := 0
	needApproval := []string{}
	for _, c := range p.Contracts {
		pr.f("\nServer/%s", c.Name)
		if c.Env != "" {
			pr.f("  env %s", c.Env)
		}
		pr.f("  approval %s\n", c.Approval)
		if c.Approval == "required" {
			needApproval = append(needApproval, c.Name)
		}
		if len(c.Servers) == 0 {
			pr.f("  no servers\n")
		}
		disk := fmt.Sprintf("%d GiB disk", c.Size.DiskGiB)
		for _, d := range c.Disks {
			disk += fmt.Sprintf(" + %d GiB %s", d.SizeGiB, d.Mount)
		}
		for _, s := range c.Servers {
			pr.f("  + %-14s %s / %s · %s %s · template %s · %s\n",
				s.Hostname, s.Site, s.Network, s.Compute.Type, s.Compute.Provider, s.Compute.Template, s.Compute.Attachment)
			pr.f("    %-14s %s: %d vCPU, %d GiB, %s · IP from %s (%s)\n",
				"", c.Size.Name, c.Size.CPU, c.Size.MemoryGiB, disk, s.IPAM.Pool, s.IPAM.Provider)
			addrs[s.IPAM.Provider]++
			vms[s.Compute.Provider]++
		}
		if len(c.Servers) > 0 && (c.Baseline != nil || len(c.Roles) > 0) {
			configure += len(c.Servers)
			pr.f("  configure\n")
			if c.Baseline != nil {
				pr.f("    baseline %s (%s)\n", c.Baseline.Name, c.Baseline.Playbook)
			}
			for _, r := range c.Roles {
				pr.f("    role %s (%s)%s\n", r.Name, r.Role, params(r.Params))
			}
		}
	}

	pr.f("\nsummary\n")
	for _, k := range keys(addrs) {
		pr.f("  ipam       + %d address(es) (%s)\n", addrs[k], k)
	}
	for _, k := range keys(vms) {
		pr.f("  provision  + %d VM(s) (%s)\n", vms[k], k)
	}
	if configure > 0 {
		pr.f("  configure  %d server(s)\n", configure)
	}
	if len(needApproval) > 0 {
		pr.f("  approval   required for %s\n", strings.Join(needApproval, ", "))
	}
	pr.f("\nplan %s\n", p.Hash)
	return pr.err
}

type printer struct {
	w   io.Writer
	err error
}

func (p *printer) f(format string, a ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, a...)
	}
}

func params(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	var parts []string
	for _, k := range keys(m) {
		parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
	}
	return " " + strings.Join(parts, " ")
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
