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

	type counts struct{ create, keep, delete int }
	addrs := map[string]*counts{} // per IPAM provider
	vms := map[string]*counts{}   // per compute provider
	bump := func(m map[string]*counts, k, action string) {
		if m[k] == nil {
			m[k] = &counts{}
		}
		switch action {
		case Create:
			m[k].create++
		case Keep:
			m[k].keep++
		case Delete:
			m[k].delete++
		}
	}
	configure, changes := 0, 0
	needApproval := []string{}
	for _, c := range p.Contracts {
		pr.f("\nServer/%s", c.Name)
		if c.Env != "" {
			pr.f("  env %s", c.Env)
		}
		pr.f("  approval %s\n", c.Approval)
		if len(c.Servers) == 0 {
			pr.f("  no servers\n")
		}
		disk := fmt.Sprintf("%d GiB disk", c.Size.DiskGiB)
		for _, d := range c.Disks {
			disk += fmt.Sprintf(" + %d GiB %s", d.SizeGiB, d.Mount)
		}
		changed, running := false, 0
		for _, s := range c.Servers {
			switch s.Action {
			case Delete:
				pr.f("  - %-14s %s / %s · uid %s (%s)\n", s.Hostname, s.Site, s.Network, s.UID, s.Status)
				bump(vms, "", Delete)
				bump(addrs, "", Delete)
				changed = true
				continue
			case Create:
				pr.f("  + %-14s %s / %s · %s %s · template %s · %s\n",
					s.Hostname, s.Site, s.Network, s.Compute.Type, s.Compute.Provider, s.Compute.Template, s.Compute.Attachment)
				changed = true
			case Keep:
				pr.f("  = %-14s %s / %s · uid %s (%s)\n", s.Hostname, s.Site, s.Network, s.UID, s.Status)
			}
			pr.f("    %-14s %s: %d vCPU, %d GiB, %s · IP from %s (%s)\n",
				"", c.Size.Name, c.Size.CPU, c.Size.MemoryGiB, disk, s.IPAM.Pool, s.IPAM.Provider)
			bump(addrs, s.IPAM.Provider, s.Action)
			bump(vms, s.Compute.Provider, s.Action)
			running++
		}
		if changed {
			changes++
			if c.Approval == "required" {
				needApproval = append(needApproval, c.Name)
			}
		}
		if running > 0 && (c.Baseline != nil || len(c.Roles) > 0) {
			configure += running
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
	line := func(what string, m map[string]*counts) {
		for _, k := range keys(m) {
			n := m[k]
			if k == "" {
				continue
			}
			pr.f("  %-10s +%d =%d (%s)\n", what, n.create, n.keep, k)
		}
		if gone := m[""]; gone != nil && gone.delete > 0 {
			pr.f("  %-10s -%d\n", what, gone.delete)
		}
	}
	line("ipam", addrs)
	line("provision", vms)
	if configure > 0 {
		pr.f("  configure  %d server(s)\n", configure)
	}
	if changes == 0 {
		pr.f("  no changes to servers\n")
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
