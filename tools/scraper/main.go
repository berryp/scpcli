// scraper fetches the SCP API documentation from the portal into a temporary
// directory, parses it, and writes openapi.yaml. No files are kept in the repo.
//
//	SCP portal → tmpdir → openapi.yaml
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/berryp/scpcli/internal/config"
)

// ─── Scraper ──────────────────────────────────────────────────────────────────

const (
	tocURL  = "https://cloud.samsungsds.com/docs/openapi/en/tocs"
	pageURL = "https://cloud.samsungsds.com/docs/openapi/"
	workers = 5
)

type TocEntry struct {
	ServiceID     string     `json:"serviceId"`
	Name          string     `json:"name"`
	Display       string     `json:"display"`
	Page          string     `json:"page"`
	APIUri        string     `json:"apiUri"`
	State         string     `json:"state"`
	CreatedYmd    string     `json:"createdYmd"`
	ModifiedYmd   string     `json:"modifiedYmd"`
	DeprecatedYmd string     `json:"deprecatedYmd"`
	Version       string     `json:"version"`
	APIID         string     `json:"apiId"`
	Nickname      string     `json:"nickname"`
	ContentType   string     `json:"contentType"`
	Child         []TocEntry `json:"child"`
}

func portalHeaders(cfg config.Config) map[string]string {
	return map[string]string{
		"accept":          "application/json",
		"x-cmp-companyid": "ETC",
		"x-cmp-language":  "en-US",
		"x-cmp-loginid":   cfg.Email,
		"x-cmp-projectid": cfg.ProjectID,
		"x-cmp-useremail": cfg.Email,
		"x-cmp-userid":    cfg.UserID,
		"z-source":        "Console",
		"z-userid":        cfg.UserID,
	}
}

func portalCookies() string {
	return fmt.Sprintf("JSESSIONID=%s; SESSION=%s",
		os.Getenv("SCP_SESSION"),
		os.Getenv("SCP_SPRING_SESSION"),
	)
}

func newPortalRequest(method, url string, cfg config.Config) (*http.Request, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range portalHeaders(cfg) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Cookie", portalCookies())
	return req, nil
}

func fetchTOC(client *http.Client, cfg config.Config) ([]TocEntry, error) {
	req, err := newPortalRequest("GET", tocURL, cfg)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("toc fetch failed: %s", resp.Status)
	}

	var entries []TocEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func collectPages(entries []TocEntry) []string {
	var pages []string
	for _, e := range entries {
		if e.Page != "" && e.Page != "N/A" {
			pages = append(pages, e.Page)
		}
		if len(e.Child) > 0 {
			pages = append(pages, collectPages(e.Child)...)
		}
	}
	return pages
}

func fetchPage(client *http.Client, page string, cfg config.Config) (string, error) {
	req, err := newPortalRequest("GET", pageURL+page, cfg)
	if err != nil {
		return "", err
	}
	req.Header.Set("accept", "text/markdown, text/plain, */*")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("page fetch failed: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func scrape(docsDir string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	for _, env := range []string{"SCP_SESSION", "SCP_SPRING_SESSION"} {
		if os.Getenv(env) == "" {
			return fmt.Errorf("missing env var: %s", env)
		}
	}

	if err := os.MkdirAll(docsDir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", docsDir, err)
	}

	client := &http.Client{Timeout: 30 * time.Second}

	fmt.Println("fetching TOC...")
	toc, err := fetchTOC(client, cfg)
	if err != nil {
		return fmt.Errorf("TOC: %w", err)
	}

	tocJSON, err := json.MarshalIndent(toc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal toc: %w", err)
	}
	if err := os.WriteFile(filepath.Join(docsDir, "tocs.json"), tocJSON, 0644); err != nil {
		return fmt.Errorf("write tocs.json: %w", err)
	}

	fmt.Printf("TOC: %d top-level entries\n", len(toc))

	pages := collectPages(toc)
	fmt.Printf("scraping %d pages...\n", len(pages))

	type job struct{ page string }
	jobs := make(chan job, len(pages))
	for _, p := range pages {
		jobs <- job{p}
	}
	close(jobs)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		success int
		failed  []string
	)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				content, err := fetchPage(client, j.page, cfg)
				if err != nil {
					mu.Lock()
					failed = append(failed, j.page)
					fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", j.page, err)
					mu.Unlock()
					continue
				}
				filename := filepath.Join(docsDir, strings.ReplaceAll(j.page, "/", "_")+".md")
				if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
					mu.Lock()
					failed = append(failed, j.page)
					fmt.Fprintf(os.Stderr, "SAVE FAIL %s: %v\n", j.page, err)
					mu.Unlock()
					continue
				}
				mu.Lock()
				success++
				fmt.Printf("OK  %s\n", j.page)
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	fmt.Printf("scrape done: %d ok, %d failed\n", success, len(failed))
	if len(failed) > 0 {
		fmt.Fprintln(os.Stderr, "failed pages:")
		for _, p := range failed {
			fmt.Fprintln(os.Stderr, " ", p)
		}
	}
	return nil
}

// ─── Generator ────────────────────────────────────────────────────────────────

// OpenAPI 3.0 types

type Spec struct {
	OpenAPI    string                           `yaml:"openapi"`
	Info       Info                             `yaml:"info"`
	Servers    []Server                         `yaml:"servers,omitempty"`
	Tags       []TagObj                         `yaml:"tags,omitempty"`
	Paths      map[string]map[string]*Operation `yaml:"paths"`
	Components Components                       `yaml:"components,omitempty"`
}

type Info struct {
	Title       string `yaml:"title"`
	Version     string `yaml:"version"`
	Description string `yaml:"description,omitempty"`
}

type Server struct {
	URL string `yaml:"url"`
}

type TagObj struct {
	Name        string `yaml:"name"`
	Summary     string `yaml:"summary,omitempty"`
	Description string `yaml:"description,omitempty"`
	Parent      string `yaml:"parent,omitempty"`
	Kind        string `yaml:"kind,omitempty"`
}

type Operation struct {
	Summary     string              `yaml:"summary,omitempty"`
	Description string              `yaml:"description,omitempty"`
	OperationID string              `yaml:"operationId,omitempty"`
	Tags        []string            `yaml:"tags,omitempty"`
	Parameters  []*Parameter        `yaml:"parameters,omitempty"`
	RequestBody *RequestBody        `yaml:"requestBody,omitempty"`
	Responses   map[string]Response `yaml:"responses"`
}

type Parameter struct {
	Name        string  `yaml:"name"`
	In          string  `yaml:"in"`
	Required    bool    `yaml:"required,omitempty"`
	Description string  `yaml:"description,omitempty"`
	Schema      *Schema `yaml:"schema,omitempty"`
}

type RequestBody struct {
	Required bool                 `yaml:"required,omitempty"`
	Content  map[string]MediaType `yaml:"content"`
}

type MediaType struct {
	Schema *Schema `yaml:"schema,omitempty"`
}

type Response struct {
	Ref         string               `yaml:"$ref,omitempty"`
	Description string               `yaml:"description,omitempty"`
	Content     map[string]MediaType `yaml:"content,omitempty"`
}

type Schema struct {
	Ref         string             `yaml:"$ref,omitempty"`
	Type        string             `yaml:"type,omitempty"`
	Format      string             `yaml:"format,omitempty"`
	Description string             `yaml:"description,omitempty"`
	Items       *Schema            `yaml:"items,omitempty"`
	Properties  map[string]*Schema `yaml:"properties,omitempty"`
	Required    []string           `yaml:"required,omitempty"`
}

type Components struct {
	Schemas   map[string]*Schema   `yaml:"schemas,omitempty"`
	Responses map[string]*Response `yaml:"responses,omitempty"`
}

var (
	linkRe    = regexp.MustCompile(`link:[^\[]+\[([^\]]+)\]`)
	arrayRe   = regexp.MustCompile(`<\s*(.*?)\s*>\s*array`)
	boldRe    = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	exampleRe = regexp.MustCompile(`\s*\*\*Example\*\*\s*:.*`)
)

func cleanText(s string) string {
	s = strings.ReplaceAll(s, " +\n", " ")
	s = strings.ReplaceAll(s, "+\n", " ")
	s = linkRe.ReplaceAllStringFunc(s, func(m string) string {
		if sub := linkRe.FindStringSubmatch(m); sub != nil {
			return sub[1]
		}
		return ""
	})
	s = exampleRe.ReplaceAllString(s, "")
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

func extractMeta(content string) map[string]string {
	meta := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, ":") {
			continue
		}
		rest := line[1:]
		idx := strings.Index(rest, ":")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(rest[:idx])
		val := strings.TrimSpace(rest[idx+1:])
		if key != "" && val != "" {
			meta[key] = val
		}
	}
	return meta
}

func extractTable(content, sectionHeader string) string {
	if sectionHeader != "" {
		idx := strings.Index(content, sectionHeader)
		if idx == -1 {
			return ""
		}
		content = content[idx:]
	}
	start := strings.Index(content, "|===")
	if start == -1 {
		return ""
	}
	rest := content[start+4:]
	end := strings.Index(rest, "|===")
	if end == -1 {
		return ""
	}
	return rest[:end]
}

func parseTableRows(raw string, numCols int) [][]string {
	lines := strings.Split(raw, "\n")
	var joined []string
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "|") {
			joined = append(joined, line)
		} else if len(joined) > 0 {
			joined[len(joined)-1] += "\n" + line
		}
	}
	if len(joined) == 0 {
		return nil
	}
	joined = joined[1:] // skip header row
	var rows [][]string
	for _, line := range joined {
		parts := strings.Split(line, "|")
		if len(parts) < numCols+1 {
			continue
		}
		row := make([]string, numCols)
		copy(row, parts[1:numCols+1])
		rows = append(rows, row)
	}
	return rows
}

func parseName(cell string) (name string, required bool) {
	if m := boldRe.FindStringSubmatch(cell); m != nil {
		name = m[1]
	}
	required = strings.Contains(cell, "__required__")
	return
}

func parseSchema(raw string) *Schema {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "No Content") {
		return nil
	}
	if m := arrayRe.FindStringSubmatch(raw); m != nil {
		inner := strings.TrimSpace(m[1])
		var item *Schema
		if lm := linkRe.FindStringSubmatch(inner); lm != nil {
			item = &Schema{Ref: "#/components/schemas/" + lm[1]}
		} else {
			item = parsePrimitive(inner)
		}
		if item == nil {
			item = &Schema{Type: "object"}
		}
		return &Schema{Type: "array", Items: item}
	}
	if m := linkRe.FindStringSubmatch(raw); m != nil {
		return &Schema{Ref: "#/components/schemas/" + m[1]}
	}
	if s := parsePrimitive(raw); s != nil {
		return s
	}
	return &Schema{Type: "string"}
}

func parsePrimitive(s string) *Schema {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "string":
		return &Schema{Type: "string"}
	case "string (date-time)":
		return &Schema{Type: "string", Format: "date-time"}
	case "string (date)":
		return &Schema{Type: "string", Format: "date"}
	case "integer (int32)":
		return &Schema{Type: "integer", Format: "int32"}
	case "integer (int64)":
		return &Schema{Type: "integer", Format: "int64"}
	case "integer":
		return &Schema{Type: "integer"}
	case "boolean":
		return &Schema{Type: "boolean"}
	case "number":
		return &Schema{Type: "number"}
	case "object":
		return &Schema{Type: "object"}
	}
	return nil
}

func serviceTag(filename string) string {
	base := strings.TrimSuffix(filepath.Base(filename), ".md")
	for _, sep := range []string{"-operations-", "-definitions-"} {
		if idx := strings.Index(base, sep); idx != -1 {
			service := strings.TrimPrefix(base[:idx], "v3-en-")
			return formatTag(service)
		}
	}
	return base
}

// formatTag turns a service slug or display-name into the hyphen-lowercase
// form used for tag names.
func formatTag(s string) string {
	s = strings.ToLower(s)
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	result := string(out)
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	return strings.Trim(result, "-")
}

type parsedOp struct {
	method      string
	path        string
	summary     string
	description string
	operationID string
	tag         string
	params      []*Parameter
	requestBody *RequestBody
	responses   map[string]Response
}

func parseOperation(filename, content string) (*parsedOp, error) {
	meta := extractMeta(content)
	apiURL := meta["api-url"]
	if apiURL == "" {
		return nil, fmt.Errorf("no api-url")
	}
	urlParts := strings.SplitN(apiURL, " ", 2)
	if len(urlParts) != 2 {
		return nil, fmt.Errorf("malformed api-url: %s", apiURL)
	}
	op := &parsedOp{
		method:      strings.ToLower(urlParts[0]),
		path:        urlParts[1],
		summary:     meta["display-name"],
		operationID: meta["x-api-nickname"],
		tag:         serviceTag(filename),
		responses:   make(map[string]Response),
	}

	if idx := strings.Index(content, "==== Description"); idx != -1 {
		sec := content[idx+16:]
		if end := strings.Index(sec, "\n===="); end != -1 {
			sec = sec[:end]
		}
		for _, stop := range []string{"\nState::", "\n[options=", "\n|==="} {
			if i := strings.Index(sec, stop); i != -1 {
				sec = sec[:i]
			}
		}
		op.description = cleanText(sec)
	}

	for _, row := range parseTableRows(extractTable(content, "==== Parameters"), 4) {
		ptype := strings.Trim(row[0], "* \n")
		name, required := parseName(row[1])
		desc := cleanText(row[2])
		schema := parseSchema(row[3])

		switch ptype {
		case "Body":
			if schema == nil {
				schema = &Schema{Type: "object"}
			}
			op.requestBody = &RequestBody{
				Required: required,
				Content:  map[string]MediaType{"application/json": {Schema: schema}},
			}
		case "Header", "Path", "Query":
			in := strings.ToLower(ptype)
			if schema == nil {
				schema = &Schema{Type: "string"}
			}
			isRequired := required || in == "path"
			op.params = append(op.params, &Parameter{
				Name:        name,
				In:          in,
				Required:    isRequired,
				Description: desc,
				Schema:      schema,
			})
		}
	}

	for _, row := range parseTableRows(extractTable(content, "==== Responses"), 3) {
		code := strings.Trim(row[0], "* \n")
		desc := cleanText(row[1])
		if desc == "" {
			desc = defaultDesc(code)
		}
		schema := parseSchema(row[2])
		resp := Response{Description: desc}
		if schema != nil {
			resp.Content = map[string]MediaType{"application/json": {Schema: schema}}
		}
		op.responses[code] = resp
	}

	if len(op.responses) == 0 {
		op.responses["200"] = Response{Description: "OK"}
	}
	return op, nil
}

func defaultDesc(code string) string {
	switch code {
	case "200":
		return "OK"
	case "201":
		return "Created"
	case "202":
		return "Accepted"
	case "204":
		return "No Content"
	case "400":
		return "Bad Request"
	case "401":
		return "Unauthorized"
	case "403":
		return "Forbidden"
	case "404":
		return "Not Found"
	case "409":
		return "Conflict"
	case "500":
		return "Internal Server Error"
	default:
		return code
	}
}

type overviewInfo struct {
	summary     string
	description string
}

// parseTOCParents reads tocs.json and returns parent links in hyphen-slug
// form. tagParents maps a service tag to its immediate category. catParents
// maps a category tag to its own parent category (or "" at the root).
//
// A CATEGORY node is treated as a service when any child is OPERATION or
// OVERVIEW; otherwise it's a navigation grouping.
func parseTOCParents(path string) (map[string]string, map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var roots []TocEntry
	if err := json.Unmarshal(data, &roots); err != nil {
		return nil, nil, err
	}
	tagParents := make(map[string]string)
	catParents := make(map[string]string)

	var walk func(node TocEntry, parentSlug string)
	walk = func(node TocEntry, parentSlug string) {
		if node.ContentType != "CATEGORY" {
			return
		}
		isService := false
		for _, c := range node.Child {
			if c.ContentType == "OPERATION" || c.ContentType == "OVERVIEW" {
				isService = true
				break
			}
		}
		if isService {
			if parentSlug != "" {
				tagParents[formatTag(node.ServiceID)] = parentSlug
			}
			return
		}
		slug := formatTag(node.Name)
		if parentSlug != "" {
			catParents[slug] = parentSlug
		} else if _, seen := catParents[slug]; !seen {
			catParents[slug] = ""
		}
		for _, c := range node.Child {
			walk(c, slug)
		}
	}
	for _, r := range roots {
		walk(r, "")
	}
	return tagParents, catParents, nil
}

// asciidocToMarkdown performs a light AsciiDoc→CommonMark conversion covering
// only the constructs used by Samsung SCP overview docs: == / === headings,
// four-dot delimited blocks, and [source, lang] language hints. A full
// AsciiDoc converter is out of scope.
func asciidocToMarkdown(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	inBlock := false
	nextLang := ""
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "...." {
			if inBlock {
				out = append(out, "```")
				inBlock = false
			} else {
				out = append(out, "```"+nextLang)
				inBlock = true
				nextLang = ""
			}
			continue
		}
		if !inBlock && strings.HasPrefix(trim, "[source,") && strings.HasSuffix(trim, "]") {
			rest := strings.TrimSuffix(strings.TrimPrefix(trim, "[source,"), "]")
			nextLang = strings.TrimSpace(rest)
			continue
		}
		if inBlock {
			out = append(out, line)
			continue
		}
		switch {
		case strings.HasPrefix(line, "==== "):
			out = append(out, "#### "+strings.TrimPrefix(line, "==== "))
		case strings.HasPrefix(line, "=== "):
			out = append(out, "### "+strings.TrimPrefix(line, "=== "))
		case strings.HasPrefix(line, "== "):
			out = append(out, "## "+strings.TrimPrefix(line, "== "))
		case strings.HasPrefix(line, "= "):
			// top-level title handled separately; drop.
		default:
			out = append(out, line)
		}
	}
	result := strings.Join(out, "\n")
	result = strings.ReplaceAll(result, " +\n", "\n")
	return strings.TrimSpace(result)
}

// parseRootInfo extracts the title (= heading) and the remainder of the
// content (converted to markdown) for the root info block.
func parseRootInfo(content string) Info {
	info := Info{Title: "Samsung SCP API", Version: "3.0"}
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "= ") {
			info.Title = strings.TrimSpace(strings.TrimPrefix(line, "= "))
			break
		}
	}
	idx := strings.Index(content, "\n== ")
	if idx == -1 {
		return info
	}
	info.Description = asciidocToMarkdown(content[idx+1:])
	return info
}

// parseOverview extracts a service summary and description from a
// v3-en-<slug>-overview.md file. The returned tag name matches what
// serviceTag() emits for operation/definition files in the same service,
// so the two can be cross-referenced.
func parseOverview(filename, content string) (string, overviewInfo) {
	base := strings.TrimSuffix(filepath.Base(filename), ".md")
	const suffix = "-overview"
	if !strings.HasSuffix(base, suffix) {
		return "", overviewInfo{}
	}
	slug := strings.TrimPrefix(strings.TrimSuffix(base, suffix), "v3-en-")
	tagName := formatTag(slug)

	meta := extractMeta(content)
	info := overviewInfo{summary: strings.TrimSpace(meta["display-name"])}

	body := stripMetaLines(content)
	for k, v := range meta {
		body = strings.ReplaceAll(body, "{"+k+"}", v)
	}
	info.description = asciidocToMarkdown(body)
	return tagName, info
}

// stripMetaLines drops top-of-file `:key: value` attribute lines so they
// don't leak into the rendered markdown.
func stripMetaLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, ":") && strings.Count(trim, ":") >= 2 {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func parseDefinition(content string) (name string, schema *Schema) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "=== ") && !strings.HasPrefix(line, "==== ") {
			name = strings.TrimSpace(line[4:])
			break
		}
	}
	if name == "" {
		return "", nil
	}
	schema = &Schema{Type: "object"}
	props := make(map[string]*Schema)
	var required []string
	for _, row := range parseTableRows(extractTable(content, ""), 3) {
		fieldName, isRequired := parseName(row[0])
		if fieldName == "" {
			continue
		}
		desc := cleanText(row[1])
		fieldSchema := parseSchema(row[2])
		if fieldSchema == nil {
			fieldSchema = &Schema{Type: "string"}
		}
		if desc != "" && fieldSchema.Ref == "" {
			fieldSchema.Description = desc
		}
		props[fieldName] = fieldSchema
		if isRequired {
			required = append(required, fieldName)
		}
	}
	if len(props) > 0 {
		schema.Properties = props
	}
	if len(required) > 0 {
		sort.Strings(required)
		schema.Required = required
	}
	return name, schema
}

func generate(docsDir, outFile string) error {
	entries, err := os.ReadDir(docsDir)
	if err != nil {
		return fmt.Errorf("read %s: %w", docsDir, err)
	}

	var ops []*parsedOp
	schemas := make(map[string]*Schema)
	overviews := make(map[string]overviewInfo)
	rootInfo := Info{Title: "Samsung SCP API", Version: "3.0"}
	var parseErrs int

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		fpath := filepath.Join(docsDir, entry.Name())
		data, err := os.ReadFile(fpath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", entry.Name(), err)
			continue
		}
		content := string(data)
		name := entry.Name()
		switch {
		case strings.Contains(name, "-operations-"):
			op, err := parseOperation(fpath, content)
			if err != nil {
				fmt.Fprintf(os.Stderr, "skip %s: %v\n", name, err)
				parseErrs++
			} else {
				ops = append(ops, op)
			}
		case strings.Contains(name, "-definitions-"):
			if defName, schema := parseDefinition(content); defName != "" && schema != nil {
				schemas[defName] = schema
			}
		case name == "v3-en-overview-overview.md":
			rootInfo = parseRootInfo(content)
		case strings.HasSuffix(strings.TrimSuffix(name, ".md"), "-overview"):
			if tagName, info := parseOverview(fpath, content); tagName != "" {
				overviews[tagName] = info
			}
		}
	}

	sort.Slice(ops, func(i, j int) bool {
		if ops[i].path != ops[j].path {
			return ops[i].path < ops[j].path
		}
		return ops[i].method < ops[j].method
	})

	responses := extractResponseComponents(ops, schemas)

	spec := &Spec{
		OpenAPI: "3.2.0",
		Info:    rootInfo,
		Servers: []Server{{URL: "https://openapi.samsungsdscloud.com"}},
		Paths:   make(map[string]map[string]*Operation),
		Components: Components{
			Schemas:   schemas,
			Responses: responses,
		},
	}

	tagSet := make(map[string]struct{})
	for _, op := range ops {
		tagSet[op.tag] = struct{}{}
		if spec.Paths[op.path] == nil {
			spec.Paths[op.path] = make(map[string]*Operation)
		}
		spec.Paths[op.path][op.method] = &Operation{
			Summary:     op.summary,
			Description: op.description,
			OperationID: op.operationID,
			Tags:        []string{op.tag},
			Parameters:  op.params,
			RequestBody: op.requestBody,
			Responses:   op.responses,
		}
	}

	tagParents, catParents, tocErr := parseTOCParents(filepath.Join(docsDir, "tocs.json"))
	if tocErr != nil {
		fmt.Fprintf(os.Stderr, "toc parents: %v\n", tocErr)
	}

	tags := make([]string, 0, len(tagSet))
	for t := range tagSet {
		tags = append(tags, t)
	}
	sort.Strings(tags)

	// Determine which navigation categories are transitively referenced by
	// service tags, and drop any that collide with a service tag name.
	neededCats := make(map[string]bool)
	for _, t := range tags {
		for cat := tagParents[t]; cat != ""; cat = catParents[cat] {
			if neededCats[cat] {
				break
			}
			neededCats[cat] = true
		}
	}
	for cat := range neededCats {
		if _, clash := tagSet[cat]; clash {
			delete(neededCats, cat)
		}
	}

	for _, t := range tags {
		tag := TagObj{Name: t, Kind: "nav"}
		if info, ok := overviews[t]; ok {
			tag.Summary = info.summary
			tag.Description = info.description
		}
		if p, ok := tagParents[t]; ok && neededCats[p] {
			tag.Parent = p
		}
		spec.Tags = append(spec.Tags, tag)
	}

	catNames := make([]string, 0, len(neededCats))
	for c := range neededCats {
		catNames = append(catNames, c)
	}
	sort.Strings(catNames)
	for _, c := range catNames {
		tag := TagObj{Name: c, Kind: "nav"}
		if p := catParents[c]; p != "" && neededCats[p] {
			tag.Parent = p
		}
		spec.Tags = append(spec.Tags, tag)
	}

	out, err := yaml.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(outFile, out, 0644); err != nil {
		return fmt.Errorf("write %s: %w", outFile, err)
	}
	fmt.Printf("wrote %s  ops=%d  schemas=%d  responses=%d  tags=%d  overviews=%d  errors=%d\n", outFile, len(ops), len(schemas), len(responses), len(spec.Tags), len(overviews), parseErrs)
	return nil
}

// extractResponseComponents promotes object-typed operation responses to
// shared components under components/responses. Each operation response that
// references a schema (directly or as an array item) is replaced with a
// $ref to a reusable response component keyed by the schema name. Primitive
// responses and responses without content stay inline.
func extractResponseComponents(ops []*parsedOp, schemas map[string]*Schema) map[string]*Response {
	responses := make(map[string]*Response)
	for _, op := range ops {
		for code, resp := range op.responses {
			if resp.Ref != "" || resp.Content == nil {
				continue
			}
			mt, ok := resp.Content["application/json"]
			if !ok || mt.Schema == nil {
				continue
			}
			var compName string
			switch {
			case mt.Schema.Ref != "":
				compName = strings.TrimPrefix(mt.Schema.Ref, "#/components/schemas/")
			case mt.Schema.Type == "array" && mt.Schema.Items != nil && mt.Schema.Items.Ref != "":
				compName = strings.TrimPrefix(mt.Schema.Items.Ref, "#/components/schemas/") + "List"
			}
			if compName == "" {
				continue
			}
			if _, exists := schemas[strings.TrimSuffix(compName, "List")]; !exists {
				continue
			}
			if _, exists := responses[compName]; !exists {
				responses[compName] = &Response{
					Description: resp.Description,
					Content:     resp.Content,
				}
			}
			op.responses[code] = Response{Ref: "#/components/responses/" + compName}
		}
	}
	return responses
}

// ─── Main ─────────────────────────────────────────────────────────────────────

// moduleRoot walks up from dir until it finds a go.mod file, returning that
// directory. Falls back to dir if not found.
func moduleRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
		os.Exit(1)
	}
	defaultOut := filepath.Join(moduleRoot(cwd), "openapi.yaml")

	outFile := flag.String("out", defaultOut, "output OpenAPI spec path")
	docsOut := flag.String("docs-out", "", "copy scraped markdown files to this directory")
	docsIn := flag.String("docs-in", "", "skip scrape and generate from this existing markdown directory")
	flag.Parse()

	if *docsIn != "" {
		fmt.Printf("generating OpenAPI spec from %s...\n", *docsIn)
		if err := generate(*docsIn, *outFile); err != nil {
			fmt.Fprintf(os.Stderr, "generate: %v\n", err)
			os.Exit(1)
		}
		return
	}

	tmpDir, err := os.MkdirTemp("", "scpcli-docs-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mktemp: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	if err := scrape(tmpDir); err != nil {
		fmt.Fprintf(os.Stderr, "scrape: %v\n", err)
		os.Exit(1)
	}

	if *docsOut != "" {
		if err := copyDocs(tmpDir, *docsOut); err != nil {
			fmt.Fprintf(os.Stderr, "copy docs: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Println("\ngenerating OpenAPI spec...")
	if err := generate(tmpDir, *outFile); err != nil {
		fmt.Fprintf(os.Stderr, "generate: %v\n", err)
		os.Exit(1)
	}
}

func copyDocs(src, dst string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") && name != "tocs.json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, name), data, 0644); err != nil {
			return err
		}
	}
	fmt.Printf("copied docs to %s\n", dst)
	return nil
}
