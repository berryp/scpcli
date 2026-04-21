// codegen generates two outputs from an OpenAPI spec:
//
//	internal/commands/*_gen.go   — Kong CLI subcommand per service tag
//	internal/commands/cli_gen.go — top-level Kong CLI struct (aggregated)
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"unicode"

	"gopkg.in/yaml.v3"
)

// ─── OpenAPI types (only what codegen needs) ──────────────────────────────────

type Spec struct {
	Paths      map[string]map[string]*Operation `yaml:"paths"`
	Tags       []TagObj                         `yaml:"tags"`
	Components Components                       `yaml:"components"`
}

type TagObj struct {
	Name string `yaml:"name"`
	Kind string `yaml:"kind"`
}

type Components struct {
	Schemas   map[string]*Schema   `yaml:"schemas"`
	Responses map[string]*Response `yaml:"responses"`
}

type Schema struct {
	Type        string             `yaml:"type"`
	Format      string             `yaml:"format"`
	Ref         string             `yaml:"$ref"`
	Description string             `yaml:"description"`
	Properties  map[string]*Schema `yaml:"properties"`
	Items       *Schema            `yaml:"items"`
	Required    []string           `yaml:"required"`
}

type Response struct {
	Description string                `yaml:"description"`
	Content     map[string]*MediaType `yaml:"content"`
	Ref         string                `yaml:"$ref"`
}

type MediaType struct {
	Schema *Schema `yaml:"schema"`
}

type Operation struct {
	Summary     string               `yaml:"summary"`
	OperationID string               `yaml:"operationId"`
	Tags        []string             `yaml:"tags"`
	Parameters  []Parameter          `yaml:"parameters"`
	RequestBody *RequestBodyDef      `yaml:"requestBody"`
	Responses   map[string]*Response `yaml:"responses"`
}

type RequestBodyDef struct {
	Content map[string]*MediaType `yaml:"content"`
}

type Parameter struct {
	Name        string `yaml:"name"`
	In          string `yaml:"in"`
	Required    bool   `yaml:"required"`
	Description string `yaml:"description"`
}

// ─── Name conversions ─────────────────────────────────────────────────────────

var camelSplitRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)

func camelToKebab(s string) string {
	s = camelSplitRe.ReplaceAllString(s, "${1}-${2}")
	return strings.ToLower(s)
}

// kebabToPascal converts a hyphen-slug to PascalCase: "api-gateway" → "ApiGateway".
func kebabToPascal(s string) string {
	parts := strings.Split(s, "-")
	for i, p := range parts {
		if len(p) == 0 {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "")
}

// kebabToCamel converts a hyphen-slug to camelCase: "api-gateway" → "apiGateway".
func kebabToCamel(s string) string {
	p := kebabToPascal(s)
	if len(p) == 0 {
		return p
	}
	runes := []rune(p)
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
}

func tagToCmdName(tag string) string    { return tag }
func tagToFuncPrefix(tag string) string { return kebabToCamel(tag) }
func tagToFuncName(tag string) string   { return tagToFuncPrefix(tag) + "Cmd" }

func tagToFilename(tag string) string {
	return strings.ReplaceAll(tag, "-", "_") + "_gen.go"
}

// opToFuncName produces a package-unique function name (legacy, kept for reference).
func opToFuncName(servicePrefix, opID string) string {
	if opID == "" {
		return servicePrefix + "UnknownCmd"
	}
	return servicePrefix + strings.ToUpper(opID[:1]) + opID[1:] + "Cmd"
}

// cmdNameToIdent converts a command name (kebab-case, possibly with underscores)
// to a PascalCase Go identifier for struct type and field names.
func cmdNameToIdent(s string) string {
	s = strings.ReplaceAll(s, "_", "-")
	return kebabToPascal(s)
}

// sanitizeTag removes characters that would break Kong struct tag parsing.
func sanitizeTag(s string) string {
	s = strings.ReplaceAll(s, `\`, "")
	s = strings.ReplaceAll(s, `"`, `'`)
	s = strings.ReplaceAll(s, "`", "'")
	return s
}

// ─── Schema → Go type ─────────────────────────────────────────────────────────

func sanitizeTypeName(name string) string {
	if name == "" {
		return "Unknown"
	}
	var out []rune
	for i, r := range name {
		if i == 0 && !unicode.IsLetter(r) && r != '_' {
			out = append(out, '_')
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			out = append(out, r)
		}
	}
	return string(out)
}

func propToFieldName(name string) string {
	if name == "" {
		return "Field"
	}
	runes := []rune(name)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func schemaToGoType(s *Schema) string {
	return resolveSchema(s, false)
}

func resolveSchema(s *Schema, elem bool) string {
	if s == nil {
		return "interface{}"
	}
	if s.Ref != "" {
		name := strings.TrimPrefix(s.Ref, "#/components/schemas/")
		ptr := "*"
		if elem {
			ptr = "*"
		}
		return ptr + sanitizeTypeName(name)
	}
	switch s.Type {
	case "string":
		return "string"
	case "integer":
		if s.Format == "int64" {
			return "int64"
		}
		return "int32"
	case "number":
		return "float64"
	case "boolean":
		return "bool"
	case "array":
		return "[]" + resolveSchema(s.Items, true)
	case "object":
		return "map[string]interface{}"
	}
	return "interface{}"
}

// responseGoType returns the Go type for the 200 response of an operation.
// Returns "" if the operation has no typed response.
func responseGoType(op *Operation, spec *Spec) string {
	if op.Responses == nil {
		return ""
	}
	resp := op.Responses["200"]
	if resp == nil {
		return ""
	}
	if resp.Ref != "" {
		compName := strings.TrimPrefix(resp.Ref, "#/components/responses/")
		comp := spec.Components.Responses[compName]
		if comp == nil {
			return ""
		}
		return responseContentGoType(comp)
	}
	return responseContentGoType(resp)
}

func responseContentGoType(resp *Response) string {
	if resp.Content == nil {
		return ""
	}
	mt := resp.Content["application/json"]
	if mt == nil || mt.Schema == nil {
		return ""
	}
	return schemaToGoType(mt.Schema)
}

// ─── Variable name safety ─────────────────────────────────────────────────────

var goReserved = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
	"func": true, "go": true, "goto": true, "if": true, "import": true,
	"interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true,
	"cmd": true, "args": true, "body": true,
}

func safeVarName(name string) string {
	if len(name) == 0 {
		return "param"
	}
	v := strings.ToLower(name[:1]) + name[1:]
	if goReserved[v] {
		return "p" + strings.ToUpper(v[:1]) + v[1:]
	}
	return v
}

// ─── Command template data ────────────────────────────────────────────────────

type ServiceData struct {
	ServiceTypeName  string // e.g. "KeyPairCmd"
	ServiceFieldName string // e.g. "KeyPair"
	ServiceFuncName  string // legacy reference
	ServiceCmdName   string
	ServiceShort     string
	Operations       []OpData
}

type OpData struct {
	StructTypeName string // e.g. "KeyPairDetailKeyPairCmd"
	FieldName      string // field name in service struct, e.g. "DetailKeyPair"
	FuncName       string // legacy reference
	CmdName        string
	Short          string
	Method         string
	Path           string
	HasBody        bool
	Params         []ParamData
	PathParams     []ParamData
	QueryParams    []ParamData
	ClientMethod   string
}

type ParamData struct {
	FieldName string // struct field name, e.g. "KeyPairId"
	Name      string
	VarName   string // legacy reference
	FlagName  string
	Desc      string
	Required  bool
}

// CLIServiceEntry is used to build the top-level CLI struct.
type CLIServiceEntry struct {
	ServiceTypeName  string
	ServiceFieldName string
	ServiceCmdName   string
	ServiceShort     string
}

// ─── Group operations by tag ──────────────────────────────────────────────────

type opEntry struct {
	path   string
	method string
	op     *Operation
}

func groupByTag(spec *Spec) map[string][]opEntry {
	groups := make(map[string][]opEntry)
	for path, methods := range spec.Paths {
		for method, op := range methods {
			tag := "misc"
			if len(op.Tags) > 0 {
				tag = op.Tags[0]
			}
			groups[tag] = append(groups[tag], opEntry{path: path, method: method, op: op})
		}
	}
	for tag := range groups {
		sort.Slice(groups[tag], func(i, j int) bool {
			oi, oj := groups[tag][i].op.OperationID, groups[tag][j].op.OperationID
			if oi != oj {
				return oi < oj
			}
			return groups[tag][i].method < groups[tag][j].method
		})
	}
	return groups
}

// buildClientMethodMap returns a map from "path:METHOD" to the typed client method
// name for operations that have a non-empty response type.
func buildClientMethodMap(spec *Spec) map[string]string {
	opIDCount := make(map[string]int)
	type opRef struct {
		tag, path, method string
		op                *Operation
	}
	var refs []opRef
	for path, methods := range spec.Paths {
		for method, op := range methods {
			if op.OperationID != "" {
				opIDCount[op.OperationID]++
			}
			tag := "misc"
			if len(op.Tags) > 0 {
				tag = op.Tags[0]
			}
			refs = append(refs, opRef{tag, path, strings.ToUpper(method), op})
		}
	}
	result := make(map[string]string)
	for _, r := range refs {
		opID := r.op.OperationID
		if opID == "" || responseGoType(r.op, spec) == "" {
			continue
		}
		upper := strings.ToUpper(opID[:1]) + opID[1:]
		var funcName string
		if opIDCount[opID] > 1 {
			funcName = kebabToPascal(r.tag) + upper
		} else {
			funcName = upper
		}
		result[r.path+":"+r.method] = funcName
	}
	return result
}

func buildServiceData(tag string, entries []opEntry, spec *Spec) ServiceData {
	prefix := tagToFuncPrefix(tag)
	pascalTag := kebabToPascal(tag)
	svc := ServiceData{
		ServiceTypeName:  pascalTag + "Cmd",
		ServiceFieldName: pascalTag,
		ServiceFuncName:  tagToFuncName(tag),
		ServiceCmdName:   tagToCmdName(tag),
		ServiceShort:     tag,
	}

	seen := make(map[string]int)
	clientMethodMap := buildClientMethodMap(spec)

	for _, e := range entries {
		opID := e.op.OperationID
		method := strings.ToUpper(e.method)

		cmdName := camelToKebab(opID)
		if opID == "" {
			cmdName = strings.Trim(camelToKebab(strings.ReplaceAll(e.path, "/", "-")), "-")
		}
		if n := seen[cmdName]; n > 0 {
			cmdName = fmt.Sprintf("%s-%d", cmdName, n+1)
		}
		seen[cmdName]++

		hasBody := e.op.RequestBody != nil || method == "POST" || method == "PUT" || method == "PATCH"

		opIdent := cmdNameToIdent(cmdName)
		od := OpData{
			StructTypeName: pascalTag + opIdent + "Cmd",
			FieldName:      opIdent,
			FuncName:       opToFuncName(prefix, opID),
			CmdName:        cmdName,
			Short:          e.op.Summary,
			Method:         method,
			Path:           e.path,
			HasBody:        hasBody,
			ClientMethod:   clientMethodMap[e.path+":"+method],
		}

		for _, p := range e.op.Parameters {
			if p.In == "header" {
				continue
			}
			pd := ParamData{
				FieldName: propToFieldName(p.Name),
				Name:      p.Name,
				VarName:   safeVarName(p.Name),
				FlagName:  camelToKebab(p.Name),
				Desc:      p.Description,
				Required:  p.Required,
			}
			if p.In == "path" {
				pd.Required = true
			}
			od.Params = append(od.Params, pd)
			switch p.In {
			case "path":
				od.PathParams = append(od.PathParams, pd)
			case "query":
				od.QueryParams = append(od.QueryParams, pd)
			}
		}

		svc.Operations = append(svc.Operations, od)
	}
	return svc
}

// ─── Command template ─────────────────────────────────────────────────────────

const cmdTmpl = `// Code generated by codegen. DO NOT EDIT.

package commands
{{range .Operations}}
type {{.StructTypeName}} struct {
{{- range .Params}}
	{{.FieldName}} string ` + "`" + `name:"{{.FlagName}}" help:"{{sanitizeTag .Desc}}"{{if .Required}} required:""{{end}}` + "`" + `
{{- end}}
{{- if .HasBody}}
	Body string ` + "`" + `name:"body" help:"request body as JSON string or @filename"` + "`" + `
{{- end}}
}

func (c *{{.StructTypeName}}) Run() error {
{{- if .HasBody}}
	b, err := resolveBody(c.Body)
	if err != nil {
		return err
	}
{{- end}}
	return RunRequest({{printf "%q" .Method}}, {{printf "%q" .Path}},
		map[string]string{ {{- range .PathParams}}{{printf "%q" .Name}}: c.{{.FieldName}},{{end}} },
		map[string]string{ {{- range .QueryParams}}{{printf "%q" .Name}}: c.{{.FieldName}},{{end}} },
		{{if .HasBody}}b{{else}}nil{{end}})
}
{{end}}
// {{.ServiceTypeName}} groups all {{.ServiceCmdName}} operations.
type {{.ServiceTypeName}} struct {
{{- range .Operations}}
	{{.FieldName}} {{.StructTypeName}} ` + "`" + `cmd:"" name:"{{.CmdName}}" help:"{{sanitizeTag .Short}}"` + "`" + `
{{- end}}
}
`

var cmdTemplate = template.Must(template.New("cmd").Funcs(template.FuncMap{
	"sanitizeTag": sanitizeTag,
}).Parse(cmdTmpl))

func generateCmdFile(data ServiceData) ([]byte, error) {
	var buf bytes.Buffer
	if err := cmdTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("template: %w", err)
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format (output follows):\n%s\nerr: %w", buf.String(), err)
	}
	return formatted, nil
}

// ─── CLI aggregate template ───────────────────────────────────────────────────

const cliTmpl = `// Code generated by codegen. DO NOT EDIT.

package commands

// CLI is the top-level Kong CLI structure.
// Global flags (--version, --output-file, --format) are pre-parsed
// in Run() to avoid conflicts with identically-named operation parameters.
type CLI struct {
{{range .}}
	{{.ServiceFieldName}} {{.ServiceTypeName}} ` + "`" + `cmd:"" name:"{{.ServiceCmdName}}" help:"{{.ServiceShort}}"` + "`" + `
{{- end}}
}
`

var cliTemplate = template.Must(template.New("cli").Parse(cliTmpl))

func generateCLIFile(entries []CLIServiceEntry) ([]byte, error) {
	var buf bytes.Buffer
	if err := cliTemplate.Execute(&buf, entries); err != nil {
		return nil, fmt.Errorf("cli template: %w", err)
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("cli format (output follows):\n%s\nerr: %w", buf.String(), err)
	}
	return formatted, nil
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func main() {
	specFile := flag.String("spec", "openapi.yaml", "path to OpenAPI YAML spec")
	outCmd := flag.String("out", filepath.Join("internal", "commands"), "output dir for commands")
	dryRun := flag.Bool("dry-run", false, "print stats without writing files")
	flag.Parse()

	data, err := os.ReadFile(*specFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read spec: %v\n", err)
		os.Exit(1)
	}

	var spec Spec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		fmt.Fprintf(os.Stderr, "parse spec: %v\n", err)
		os.Exit(1)
	}

	// ── 1. Commands ──────────────────────────────────────────────────────────

	if !*dryRun {
		existing, _ := filepath.Glob(filepath.Join(*outCmd, "*_gen.go"))
		for _, f := range existing {
			_ = os.Remove(f)
		}
		if len(existing) > 0 {
			fmt.Printf("removed %d stale command _gen.go files\n", len(existing))
		}
	}

	groups := groupByTag(&spec)
	tags := make([]string, 0, len(groups))
	for t := range groups {
		tags = append(tags, t)
	}
	sort.Strings(tags)

	var cliEntries []CLIServiceEntry
	var totalOps, totalFiles int
	for _, tag := range tags {
		svcData := buildServiceData(tag, groups[tag], &spec)
		if len(svcData.Operations) == 0 {
			continue
		}
		src, err := generateCmdFile(svcData)
		if err != nil {
			fmt.Fprintf(os.Stderr, "generate cmd %s: %v\n", tag, err)
			continue
		}
		filename := filepath.Join(*outCmd, tagToFilename(tag))
		if *dryRun {
			fmt.Printf("would write %-50s  ops=%d\n", filename, len(svcData.Operations))
		} else {
			if err := os.WriteFile(filename, src, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "write %s: %v\n", filename, err)
				continue
			}
			fmt.Printf("wrote %-50s  ops=%d\n", filename, len(svcData.Operations))
		}
		cliEntries = append(cliEntries, CLIServiceEntry{
			ServiceTypeName:  svcData.ServiceTypeName,
			ServiceFieldName: svcData.ServiceFieldName,
			ServiceCmdName:   svcData.ServiceCmdName,
			ServiceShort:     svcData.ServiceShort,
		})
		totalOps += len(svcData.Operations)
		totalFiles++
	}
	fmt.Printf("\ncommands: %d files, %d operations\n", totalFiles, totalOps)

	// ── 2. CLI aggregate ──────────────────────────────────────────────────────

	cliPath := filepath.Join(*outCmd, "cli_gen.go")
	if *dryRun {
		fmt.Printf("would write %-50s  services=%d\n", cliPath, len(cliEntries))
	} else {
		src, err := generateCLIFile(cliEntries)
		if err != nil {
			fmt.Fprintf(os.Stderr, "generate cli: %v\n", err)
		} else if err := os.WriteFile(cliPath, src, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", cliPath, err)
		} else {
			fmt.Printf("wrote %-50s  services=%d\n", cliPath, len(cliEntries))
		}
	}

}
