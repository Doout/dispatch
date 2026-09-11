package chartvalues

import (
	"strconv"
	"strings"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
)

// PreviewTpl evaluates known root-scoped tpl inputs without access to Kubernetes.
// Values must already be coalesced and redacted, including chart defaults.
func PreviewTpl(c *chart.Chart, values map[string]any, options chartutil.ReleaseOptions, result *Result) {
	result.Rendered = map[string]string{}
	context := chartutil.Values{
		"Values": values, "Chart": c.Metadata, "Capabilities": chartutil.DefaultCapabilities,
		"Release": map[string]any{"Name": options.Name, "Namespace": options.Namespace,
			"Revision": options.Revision, "IsInstall": options.IsInstall, "IsUpgrade": options.IsUpgrade, "Service": "Helm"},
	}
	attempted := 0
	for _, parts := range result.TplPaths {
		var input any = values
		for _, part := range parts {
			object, _ := input.(map[string]any)
			input = object[part]
		}
		raw, ok := input.(string)
		if !ok || !strings.Contains(raw, "{{") || len(raw) > 65536 {
			continue
		}
		if attempted >= 128 {
			break
		}
		attempted++
		// A separate template keeps a failed expression from hiding other values.
		preview := *c
		preview.Templates = nil
		for _, file := range c.Templates {
			if strings.HasPrefix(file.Name, "templates/_") {
				preview.Templates = append(preview.Templates, file)
			}
		}
		const name = "templates/dispatch-values-preview.txt"
		preview.Templates = append(preview.Templates, &chart.File{Name: name, Data: []byte("{{ tpl " + strconv.Quote(raw) + " $ }}")})
		renderer := engine.Engine{Strict: true}
		output, err := renderer.Render(&preview, context)
		if err != nil {
			continue
		}
		if rendered, exists := output[c.Name()+"/"+name]; exists {
			result.Rendered[strings.Join(parts, ".")] = rendered
		}
	}
	if len(result.Rendered) > 0 {
		result.Notes = append(result.Notes, "Helm tpl previews use the release values. Check Manifests for the exact deployed output.")
	}
}
