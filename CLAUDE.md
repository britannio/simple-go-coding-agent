# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build/Lint/Test Commands
- Build: `go build`
- Run: `go run main.go`
- Run with debug mode: `DEBUG=1 go run main.go`
- Test: `go test ./...`
- Test single file: `go test <file_path>`
- Test single function: `go test -run <TestName>`
- Format code: `go fmt ./...`
- Lint: `golangci-lint run`

## Code Style Guidelines
- **Imports**: Group standard library imports first, then third-party imports
- **Formatting**: Use `go fmt` for standard formatting
- **Types**: Use meaningful names for types; prefer explicit types over inference
- **Error Handling**: Always check errors; wrap errors with `fmt.Errorf` for context
- **Naming**: Use camelCase for variables, PascalCase for exported functions/types
- **Functions**: Keep functions focused on a single responsibility
- **Comments**: Document exported functions with meaningful comments
- **Tool Functions**: Follow the pattern of input parsing, processing, error handling
- **Tools**: Each tool should follow the same structure: input type, schema, function, and definition