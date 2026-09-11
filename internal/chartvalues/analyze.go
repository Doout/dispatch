// Package chartvalues identifies chart inputs for display. It never filters Helm's inputs.
package chartvalues

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"text/template/parse"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
)

type Result struct {
	Values   map[string]any
	Notes    []string
	TplPaths [][]string
	Rendered map[string]string
}
type value struct {
	data    any
	path    []string
	fields  map[string]value
	unknown bool
}
type scope struct {
	dot  value
	vars map[string]value
}
type analyzer struct {
	trees    map[string]*parse.Tree
	selected map[string][]string
	tplPaths map[string][]string
	notes    map[string]bool
	depth    int
	steps    int
}

func Analyze(c *chart.Chart, supplied map[string]any) (Result, error) {
	merged, err := chartutil.CoalesceValues(c, supplied)
	if err != nil {
		return Result{}, err
	}
	a := &analyzer{trees: map[string]*parse.Tree{}, selected: map[string][]string{}, tplPaths: map[string][]string{}, notes: map[string]bool{}}
	a.register(c)
	a.chart(c, merged, []string{})
	if a.steps > 100000 {
		a.keep([]string{})
		a.notes["Template analysis reached its limit; all chart inputs remain visible."] = true
	}
	out := map[string]any{}
	for _, p := range a.selected {
		copyPath(out, merged, p)
	}
	notes := []string{}
	for n := range a.notes {
		notes = append(notes, n)
	}
	sort.Strings(notes)
	tplPaths := [][]string{}
	for _, p := range a.tplPaths {
		tplPaths = append(tplPaths, p)
	}
	sort.Slice(tplPaths, func(i, j int) bool { return strings.Join(tplPaths[i], ".") < strings.Join(tplPaths[j], ".") })
	return Result{Values: out, Notes: notes, TplPaths: tplPaths}, nil
}
func (a *analyzer) register(c *chart.Chart) {
	for _, dep := range c.Dependencies() {
		a.register(dep)
	}
	for _, f := range c.Templates {
		t := parse.New(f.Name)
		t.Mode = parse.SkipFuncCheck
		trees := map[string]*parse.Tree{}
		if _, err := t.Parse(string(f.Data), "{{", "}}", trees); err == nil {
			for name, tree := range trees {
				a.trees[name] = tree
			}
		}
	}
}
func (a *analyzer) chart(c *chart.Chart, data map[string]any, prefix []string) {
	a.defaults(c.Values, prefix)
	var schema map[string]any
	if json.Unmarshal(c.Schema, &schema) == nil {
		a.schema(schema, prefix, schema, map[string]bool{})
	}
	entries := []string{}
	for _, f := range c.Templates {
		t := parse.New(f.Name)
		t.Mode = parse.SkipFuncCheck
		trees := map[string]*parse.Tree{}
		if _, err := t.Parse(string(f.Data), "{{", "}}", trees); err != nil {
			a.keep(prefix)
			a.notes["Template analysis was incomplete; the affected chart values remain visible."] = true
			continue
		}
		for name, tree := range trees {
			a.trees[name] = tree
		}
		if !strings.HasPrefix(path.Base(f.Name), "_") {
			entries = append(entries, f.Name)
		}
	}
	root := value{fields: map[string]value{"Values": {data: data, path: prefix}, "Chart": {data: map[string]any{"Name": c.Name()}}}}
	for _, entry := range entries {
		a.walk(a.trees[entry].Root, scope{root, map[string]value{"$": root}})
	}
	for _, dep := range c.Dependencies() {
		child, _ := data[dep.Name()].(map[string]any)
		a.chart(dep, child, appendPath(prefix, dep.Name()))
	}
}
func appendPath(p []string, k string) []string { return append(append([]string{}, p...), k) }
func (a *analyzer) keep(p []string)            { a.selected[strings.Join(p, "\x00")] = append([]string{}, p...) }
func (a *analyzer) defaults(m map[string]any, p []string) {
	for k, v := range m {
		next := appendPath(p, k)
		if child, ok := v.(map[string]any); ok && len(child) > 0 {
			a.defaults(child, next)
		} else {
			a.keep(next)
		}
	}
}
func (a *analyzer) schema(s map[string]any, p []string, root map[string]any, seen map[string]bool) {
	if ref, ok := s["$ref"].(string); ok {
		if seen[ref] || !strings.HasPrefix(ref, "#/") {
			a.keep(p)
			return
		}
		seenNext := map[string]bool{}
		for k, v := range seen {
			seenNext[k] = v
		}
		seenNext[ref] = true
		var target any = root
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			m, _ := target.(map[string]any)
			target = m[strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")]
		}
		if resolved, ok := target.(map[string]any); ok {
			a.schema(resolved, p, root, seenNext)
		} else {
			a.keep(p)
		}
	}
	if len(p) > 0 {
		if open, ok := s["additionalProperties"].(bool); ok && open {
			a.keep(p)
		}
		if _, ok := s["additionalProperties"].(map[string]any); ok {
			a.keep(p)
		}
	}

	props, _ := s["properties"].(map[string]any)
	for k, raw := range props {
		child, _ := raw.(map[string]any)
		next := appendPath(p, k)
		if sub, _ := child["properties"].(map[string]any); len(sub) > 0 || child["$ref"] != nil {
			a.schema(child, next, root, seen)
		} else {
			a.keep(next)
		}
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		branches, _ := s[keyword].([]any)
		for _, raw := range branches {
			if m, ok := raw.(map[string]any); ok {
				a.schema(m, p, root, seen)
			}
		}
	}
}
func copyPath(dst, src map[string]any, p []string) {
	if len(p) == 0 {
		for k, v := range src {
			dst[k] = v
		}
		return
	}
	v, ok := src[p[0]]
	if !ok {
		return
	}
	if len(p) == 1 {
		dst[p[0]] = v
		return
	}
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	target, _ := dst[p[0]].(map[string]any)
	// Allocate a projection rather than modifying maps belonging to Helm.
	if target == nil {
		target = map[string]any{}
		dst[p[0]] = target
	} else {
		clone := map[string]any{}
		for k, v := range target {
			clone[k] = v
		}
		target = clone
		dst[p[0]] = target
	}
	copyPath(target, m, p[1:])
}
func cloneScope(s scope) scope {
	vars := map[string]value{}
	for k, v := range s.vars {
		vars[k] = v
	}
	return scope{s.dot, vars}
}
func (a *analyzer) walk(n parse.Node, s scope) {
	if n == nil || a.steps > 100000 {
		return
	}
	a.steps++
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, node := range n.Nodes {
			a.walk(node, s)
		}
	case *parse.ActionNode:
		v := a.pipe(n.Pipe, s)
		if len(n.Pipe.Decl) == 0 {
			a.consume(v, s)
		}
	case *parse.IfNode:
		child := cloneScope(s)
		v := a.pipe(n.Pipe, child)
		a.consume(v, child)
		if b, ok := v.data.(bool); ok {
			if b {
				a.walk(n.List, cloneScope(child))
			} else {
				a.walk(n.ElseList, cloneScope(child))
			}
		} else {
			a.walk(n.List, cloneScope(child))
			a.walk(n.ElseList, cloneScope(child))
		}
	case *parse.WithNode:
		child := cloneScope(s)
		v := a.pipe(n.Pipe, child)
		child.dot = v
		a.walk(n.List, child)
		a.walk(n.ElseList, cloneScope(s))
	case *parse.RangeNode:
		child := cloneScope(s)
		v := a.pipe(n.Pipe, child)
		switch m := v.data.(type) {
		case map[string]any:
			keys := []string{}
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				a.iterate(n, s, value{data: k}, a.field(v, []string{k}, s))
			}
		case []any:
			a.consume(v, s)
			for i, item := range m {
				a.iterate(n, s, value{data: i}, value{data: item})
			}
		default:
			a.consume(v, s)
			a.walk(n.List, child)
		}
		a.walk(n.ElseList, cloneScope(s))
	case *parse.TemplateNode:
		a.include(n.Name, a.pipe(n.Pipe, s), s)
	}
}
func (a *analyzer) iterate(n *parse.RangeNode, s scope, key, v value) {
	child := cloneScope(s)
	child.dot = v
	if len(n.Pipe.Decl) == 1 {
		child.vars[n.Pipe.Decl[0].Ident[0]] = v
	}
	if len(n.Pipe.Decl) > 1 {
		child.vars[n.Pipe.Decl[0].Ident[0]] = key
		child.vars[n.Pipe.Decl[1].Ident[0]] = v
	}
	a.walk(n.List, child)
}
func (a *analyzer) field(v value, parts []string, s scope) value {
	for _, k := range parts {
		if v.fields != nil {
			v = v.fields[k]
			continue
		}
		m, ok := v.data.(map[string]any)
		if !ok {
			return value{}
		}
		p := v.path
		if p != nil {
			p = appendPath(p, k)
		}
		v = value{data: m[k], path: p}
	}
	if _, ok := v.data.(map[string]any); !ok && v.path != nil {
		a.consume(v, s)
	}
	return v
}
func (a *analyzer) eval(n parse.Node, s scope) value {
	switch n := n.(type) {
	case *parse.DotNode:
		return s.dot
	case *parse.FieldNode:
		return a.field(s.dot, n.Ident, s)
	case *parse.VariableNode:
		return a.field(s.vars[n.Ident[0]], n.Ident[1:], s)
	case *parse.ChainNode:
		return a.field(a.eval(n.Node, s), n.Field, s)
	case *parse.StringNode:
		return value{data: n.Text}
	case *parse.BoolNode:
		return value{data: n.True}
	case *parse.NumberNode:
		return value{data: n.Int64}
	case *parse.PipeNode:
		return a.pipe(n, s)
	}
	return value{}
}
func (a *analyzer) pipe(p *parse.PipeNode, s scope) value {
	if p == nil {
		return value{}
	}
	v := value{}
	for i, c := range p.Cmds {
		args := []value{}
		name := ""
		if id, ok := c.Args[0].(*parse.IdentifierNode); ok {
			name = id.Ident
		}
		start := 0
		if name != "" {
			start = 1
		}
		for _, arg := range c.Args[start:] {
			args = append(args, a.eval(arg, s))
		}
		if i > 0 {
			args = append(args, v)
		}
		if name == "" {
			if len(args) > 0 {
				v = args[0]
			}
		} else {
			v = a.call(name, args, s)
		}
	}
	for _, decl := range p.Decl {
		s.vars[decl.Ident[0]] = v
	}
	return v
}
func (a *analyzer) call(name string, args []value, s scope) value {
	arg := func(i int) value {
		if i < len(args) {
			return args[i]
		}
		return value{unknown: true}
	}
	switch name {
	case "and":
		for _, v := range args {
			if v.data == false {
				return value{data: false}
			}
		}
		return value{unknown: true}
	case "or":
		for _, v := range args {
			if v.data == true {
				return value{data: true}
			}
		}
		return value{unknown: true}
	case "not":
		if b, ok := arg(0).data.(bool); ok {
			return value{data: !b}
		}
		return value{unknown: true}
	case "eq", "ne":
		if arg(0).data != nil && arg(1).data != nil {
			same := fmt.Sprint(arg(0).data) == fmt.Sprint(arg(1).data)
			return value{data: same == (name == "eq")}
		}
		return value{unknown: true}
	case "dict":
		fields := map[string]value{}
		for i := 0; i+1 < len(args); i += 2 {
			if k, ok := args[i].data.(string); ok {
				fields[k] = args[i+1]
			}
		}
		return value{fields: fields}
	case "get", "index", "hasKey":
		base := arg(0)
		for _, key := range args[1:] {
			k, ok := key.data.(string)
			if !ok || key.unknown {
				a.consume(base, s)
				a.notes["Some dynamic lookups could not be resolved; their input sections remain visible."] = true
				return value{unknown: true}
			}
			base = a.field(base, []string{k}, s)
		}
		if name == "hasKey" {
			return value{data: base.data != nil}
		}
		return base
	case "coalesce":
		for _, v := range args {
			if v.unknown {
				for _, input := range args {
					a.consume(input, s)
				}
				return value{unknown: true}
			}
			if !empty(v) {
				return v
			}
		}
		return value{unknown: true}
	case "default":
		for i := len(args) - 1; i >= 0; i-- {
			if args[i].unknown {
				for _, input := range args {
					a.consume(input, s)
				}
				return value{unknown: true}
			}
			if !empty(args[i]) {
				return args[i]
			}
		}
		return arg(0)
	case "deepCopy":
		return arg(0)
	case "include", "template":
		if name, ok := arg(0).data.(string); ok {
			return a.include(name, arg(1), s)
		}
		a.consume(arg(1), s)
		a.notes["An unresolved helper retains its input section."] = true
		return value{unknown: true}
	case "tpl":
		// Only preview calls using the chart root; custom tpl scopes need their own context.
		if root, ok := arg(1).fields["Values"]; ok && root.path != nil && len(root.path) == 0 && arg(0).path != nil {
			a.tplPaths[strings.Join(arg(0).path, "\x00")] = arg(0).path
		}
		a.consume(arg(0), s)
		if text, ok := arg(0).data.(string); ok {
			a.templateString(text, arg(1))
		}
		return value{unknown: true}
	case "toString":
		a.consume(arg(0), s)
		return value{data: fmt.Sprint(arg(0).data), path: arg(0).path}
	case "quote":
		a.consume(arg(0), s)
		return value{data: fmt.Sprint(arg(0).data)}
	case "printf":
		for _, v := range args {
			a.consume(v, s)
		}
		if f, ok := arg(0).data.(string); ok {
			raw := []any{}
			for _, v := range args[1:] {
				if v.data == nil {
					return value{unknown: true}
				}
				raw = append(raw, v.data)
			}
			return value{data: fmt.Sprintf(f, raw...)}
		}
	case "merge", "mergeOverwrite", "mustMergeOverwrite":
		// Keep all contributing sections when merge semantics obscure a later lookup.
		for _, v := range args {
			a.consume(v, s)
		}
		return arg(0)
	default:
		for _, v := range args {
			a.consume(v, s)
		}
	}
	return value{unknown: true}
}
func empty(v value) bool { return v.data == nil && v.fields == nil || v.data == "" || v.data == false }
func (a *analyzer) include(name string, ctx value, s scope) value {
	if a.depth >= 32 {
		a.consume(ctx, s)
		a.notes["Recursive helper analysis retains its input section."] = true
		return value{unknown: true}
	}
	t := a.trees[name]
	if t == nil {
		a.consume(ctx, s)
		return value{unknown: true}
	}
	a.depth++
	a.walk(t.Root, scope{ctx, map[string]value{"$": ctx}})
	a.depth--
	return value{unknown: true}
}
func (a *analyzer) consume(v value, s scope) {
	if v.path != nil {
		a.keep(v.path)
	}
	// Follow Helm expressions only within sections selected by the chart.
	if a.depth >= 32 {
		return
	}
	switch d := v.data.(type) {
	case string:
		if strings.Contains(d, "{{") {
			a.templateString(d, s.vars["$"])
		}
	case map[string]any:
		for _, d := range d {
			a.consume(value{data: d}, s)
		}
	case []any:
		for _, d := range d {
			a.consume(value{data: d}, s)
		}
	}
	for _, field := range v.fields {
		a.consume(field, s)
	}
}
func (a *analyzer) templateString(text string, ctx value) {
	if a.depth >= 32 || a.steps > 100000 {
		return
	}
	t := parse.New("value")
	t.Mode = parse.SkipFuncCheck
	if _, err := t.Parse(text, "{{", "}}", map[string]*parse.Tree{}); err != nil {
		a.notes["A value expression could not be analyzed; its input context remains visible."] = true
		if v, ok := ctx.fields["Values"]; ok {
			a.keep(v.path)
		}
		return
	}
	a.depth++
	a.walk(t.Root, scope{ctx, map[string]value{"$": ctx}})
	a.depth--
}
