package commands

// Regenerate CLI command files from the OpenAPI spec at the module root.
// Run "go generate ./internal/commands" after updating openapi.yaml.

//go:generate go run ../../tools/codegen -spec ../../openapi.yaml -out . -out-client ../../internal/client
