package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/alecthomas/kong"

	"github.com/berryp/scpcli/internal/client"
	"github.com/berryp/scpcli/internal/config"
	"github.com/berryp/scpcli/internal/version"
)

var (
	flagOutputFile  string
	flagPrintAs     string
	flagCompletion  string
)

var cachedClient *client.Client

func withClient() (*client.Client, error) {
	if cachedClient != nil {
		return cachedClient, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	cachedClient = client.New(cfg)
	return cachedClient, nil
}

// Run parses CLI arguments and executes the matched command.
//
// Global flags are stripped from os.Args before Kong processes them to avoid
// panics when an API operation has a parameter with the same flag name.
func Run() error {
	remaining, done := parseGlobals(os.Args[1:])
	if done {
		return nil
	}

	cli := &CLI{}
	k, err := kong.New(cli,
		kong.Name("scpcli"),
		kong.Description("SCP (Samsung SDS Cloud) OpenAPI CLI\n\nAPI documentation: https://cloud.samsungsds.com/openapiguide/"),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{NoExpandSubcommands: true}),
	)
	if err != nil {
		panic(err)
	}
	if flagCompletion != "" {
		return generateCompletion(k, flagCompletion)
	}
	ctx, err := k.Parse(remaining)
	k.FatalIfErrorf(err)
	return ctx.Run()
}

// parseGlobals strips --version, -output-file and --filter from args, sets package-level vars, and returns remaining
// args for Kong. Returns done=true if the caller should exit (e.g. --version).
func parseGlobals(args []string) (remaining []string, done bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--version":
			fmt.Println(version.String())
			return nil, true
		case arg == "--output-file":
			if i+1 < len(args) {
				i++
				flagOutputFile = args[i]
			}
		case strings.HasPrefix(arg, "--output-file="):
			flagOutputFile = strings.TrimPrefix(arg, "--output-file=")
		case arg == "--print":
			if i+1 < len(args) {
				i++
				flagPrintAs = args[i]
			}
		case strings.HasPrefix(arg, "--print="):
			flagPrintAs = strings.TrimPrefix(arg, "--print=")
		case arg == "--completion":
			if i+1 < len(args) {
				i++
				flagCompletion = args[i]
			}
		case strings.HasPrefix(arg, "--completion="):
			flagCompletion = strings.TrimPrefix(arg, "--completion=")
		default:
			remaining = append(remaining, arg)
		}
	}
	return parseFilter(remaining), false
}

func writeOutput(data []byte) error {
	w := os.Stdout
	if flagOutputFile != "" {
		f, err := os.Create(flagOutputFile)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		w = f
	}
	if handled, err := applyFilter(w, data); handled || err != nil {
		return err
	}
	return PrintJSON(w, data)
}

// RunOutput marshals a typed value to JSON and writes it through the output pipeline.
func RunOutput(v any) error {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	return writeOutput(data)
}

// RunRequest is used by generated commands that have no typed response.
func RunRequest(method, path string, pathParams, queryParams map[string]string, body []byte) error {
	c, err := withClient()
	if err != nil {
		return err
	}
	if flagPrintAs != "" {
		req, err := c.BuildRequest(method, path, pathParams, queryParams, body)
		if err != nil {
			return err
		}
		return printRequest(req, body, flagPrintAs, c.SecretKey())
	}
	data, err := c.Do(method, path, pathParams, queryParams, body)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return writeOutput(data)
}
