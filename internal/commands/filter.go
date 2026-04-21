package commands

import (
	"bytes"
	"container/list"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mikefarah/yq/v4/pkg/yqlib"
)

var flagFilter string

// parseFilter strips --filter and value, and returns the remaining args for Kong.
func parseFilter(args []string) []string {
	var remaining []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--filter":
			if i+1 < len(args) {
				i++
				flagFilter = args[i]
			}
		case strings.HasPrefix(arg, "--filter="):
			flagFilter = strings.TrimPrefix(arg, "--filter=")
		default:
			remaining = append(remaining, arg)
		}
	}
	return remaining
}

// applyFilter runs the response through yq if a filter expression or a
// non-JSON output format is requested. Returns handled=false when the caller
// should fall through to its own JSON printer (plain JSON, no filter).
func applyFilter(w io.Writer, data []byte) (bool, error) {
	if flagFilter == "" {
		return false, nil
	}
	out, err := filterYQ(data, flagFilter)
	if err != nil {
		return true, err
	}
	_, err = w.Write(out)
	return true, err
}

// filterYQ evaluates expr against data using the given output format.
// It pre-populates $ENV with the current process environment so that
// expressions like $ENV.MY_VAR work without any extra setup.
func filterYQ(data []byte, expr string) ([]byte, error) {
	yqlib.InitExpressionParser()
	expressionNode, err := yqlib.ExpressionParser.ParseExpression(expr)
	if err != nil {
		return nil, fmt.Errorf("filter: parse: %w", err)
	}

	documents, err := yqlib.ReadDocuments(strings.NewReader(string(data)), yqlib.NewJSONDecoder())
	if err != nil {
		return nil, fmt.Errorf("filter: decode input: %w", err)
	}

	envNode, err := envToNode()
	if err != nil {
		return nil, fmt.Errorf("filter: build $ENV: %w", err)
	}

	envList := list.New()
	envList.PushBack(envNode)
	ctx := yqlib.Context{MatchingNodes: documents}
	ctx.SetVariable("ENV", envList)

	result, err := yqlib.NewDataTreeNavigator().GetMatchingNodes(ctx, expressionNode)
	if err != nil {
		return nil, fmt.Errorf("filter: %w", err)
	}

	encoder := yqlib.NewJSONEncoder(yqlib.JsonPreferences{
		Indent:        2,
		ColorsEnabled: false,
		UnwrapScalar:  true,
	})
	out := new(bytes.Buffer)
	printer := yqlib.NewPrinter(encoder, yqlib.NewSinglePrinterWriter(out))
	if err := printer.PrintResults(result.MatchingNodes); err != nil {
		return nil, fmt.Errorf("filter: render: %w", err)
	}
	return bytes.TrimRight(out.Bytes(), "\n"), nil
}

// envToNode encodes the current process environment as a yq CandidateNode
// (a JSON-decoded mapping), ready to be bound to the $ENV variable.
func envToNode() (*yqlib.CandidateNode, error) {
	envMap := make(map[string]string)
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		envMap[k] = v
	}
	jsonBytes, err := json.Marshal(envMap)
	if err != nil {
		return nil, err
	}
	dec := yqlib.NewJSONDecoder()
	if err := dec.Init(strings.NewReader(string(jsonBytes))); err != nil {
		return nil, err
	}
	return dec.Decode()
}
