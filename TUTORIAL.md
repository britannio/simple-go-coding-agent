# Claude Go Agent Tutorial

This tutorial explains how to use the Claude Go Agent, a command-line interface for interacting with Claude AI while providing it access to your local filesystem.

## Getting Started

### Prerequisites

- Go 1.24+ installed
- An Anthropic API key set in the environment (export ANTHROPIC_API_KEY=your_key_here)

### Running the Agent

```bash
go run main.go
```

To enable debug mode (showing tool responses):

```bash
DEBUG=1 go run main.go
```

## Available Tools

The agent comes with several built-in tools that Claude can use to interact with your filesystem.

### Reading Files

Claude can read the contents of files using the `read_file` tool:

```
read_file({"path": "path/to/file.txt"})
```

This reads and returns the content of the specified file. The path should be relative to the current directory.

### Listing Files

Claude can list files and directories using the `list_files` tool:

```
list_files({"path": "path/to/directory"})
```

This returns a JSON array of files and directories at the specified path. By default, it excludes:
- `.git` directory
- Hidden files (starting with `.`)
- Common large directories like `node_modules`, `vendor`, `dist`, etc.

#### Advanced Filtering Options

The `list_files` tool supports advanced filtering:

```
list_files({
  "path": "path/to/directory",
  "include_git": true,
  "include_hidden": true,
  "exclude": ["node_modules", "dist"]
})
```

- `include_git`: Set to `true` to include the `.git` directory
- `include_hidden`: Set to `true` to include hidden files and directories
- `exclude`: Array of patterns to exclude from the results

### Searching in Files

Claude can search for patterns in files using the `grep` tool:

```
grep({"pattern": "function main", "path": "./"})
```

This performs a regular expression search in all files at the specified path and returns matches with file names and line numbers. The tool uses the same filtering system as `list_files`.

#### Advanced Grep Options

```
grep({
  "pattern": "TODO:",
  "path": "./src",
  "include_git": false,
  "include_hidden": true,
  "exclude": ["generated"]
})
```

### Editing Files

Claude can edit files using the `edit_file` tool:

```
edit_file({
  "path": "path/to/file.txt",
  "old_str": "text to replace",
  "new_str": "replacement text"
})
```

This replaces all occurrences of `old_str` with `new_str` in the specified file. If the file doesn't exist and `old_str` is empty, it will create a new file with `new_str` as its content.

### Executing Commands

Claude can execute shell commands using the `execute` tool:

```
execute({
  "command": "ls -la",
  "timeout": 30
})
```

This executes the specified command in a shell environment and returns the output. The `timeout` parameter is optional and defaults to 30 seconds (maximum 300 seconds).

The output is returned as a JSON object containing:
- `stdout`: Standard output from the command
- `stderr`: Standard error output from the command
- `exit_code`: The command's exit code (0 typically means success)

### Dynamic Custom Tools

The agent supports dynamically loading custom tools from a configuration file (`tools_config.json`). These tools are backed by shell commands but appear as first-class tools to Claude.

Example configuration:

```json
{
  "tools": [
    {
      "name": "git_status",
      "description": "Shows the working tree status. Lists changed files, staged changes, and untracked files.",
      "command": "git status",
      "timeout": 30,
      "parameters": []
    },
    {
      "name": "go_test",
      "description": "Run Go tests in the specified package. If no package is specified, runs all tests.",
      "command": "go test {{.package}}",
      "timeout": 60,
      "parameters": [
        {
          "name": "package",
          "description": "The package to test. Use ./... for all packages.",
          "required": false,
          "default": "./..."
        }
      ]
    }
  ]
}
```

Each dynamic tool configuration must include:
- `name`: The tool's name (used to invoke it)
- `description`: A description of what the tool does
- `command`: The command template to execute
- `timeout`: Maximum execution time in seconds
- `parameters`: List of parameters the tool accepts

Parameters can be templated into the command using Go template syntax (`{{.paramName}}`). Required parameters must be provided, while optional parameters will use their default value if not specified.

#### Examples

List directory contents:
```
execute({"command": "ls -la"})
```

Check Git status:
```
execute({"command": "git status"})
```

Run a script with a longer timeout:
```
execute({
  "command": "./long_running_script.sh",
  "timeout": 120
})
```

## Practical Examples

### Example 1: Finding and Fixing a Bug

Ask Claude to find and fix a bug:

```
Can you find where error handling might be missing in our code and fix it?
```

Claude will:
1. Use `list_files` to see what files are available
2. Use `grep` to search for patterns that might indicate missing error checks
3. Use `read_file` to examine suspicious files
4. Use `edit_file` to fix the issues it finds

### Example 2: Creating a New Feature

Ask Claude to add a new feature:

```
Can you create a new feature that counts the lines of code in the repository?
```

Claude will:
1. Create a new file or modify an existing one using `edit_file`
2. Implement the line counting functionality
3. Test the feature by running it

### Example 3: Code Analysis

Ask Claude to analyze your code:

```
Can you check our codebase for best practices and suggest improvements?
```

Claude will:
1. Use `list_files` and `grep` to explore the codebase
2. Analyze the code structure, patterns, and practices
3. Provide recommendations for improvements

### Example 4: Running Tests and Building the Project

Ask Claude to run tests and build the project:

```
Can you run our test suite and build the project?
```

Claude will:
1. Use `execute` to run test commands (e.g., `go test ./...` or `npm test`)
2. Fix any failing tests using `read_file` and `edit_file`
3. Use `execute` to build the project
4. Report the results

## Tips and Best Practices

1. **Be Specific**: When asking Claude to make changes, be specific about what you want.

2. **Debug Mode**: Run with `DEBUG=1` to see the raw tool responses, which helps with troubleshooting.

3. **Large Files**: For very large files, Claude might have limitations. Consider specifying which parts to focus on.

4. **Complex Changes**: For complex changes, break them down into smaller steps.

5. **Security**: The agent has access to your local filesystem and can execute commands, so be careful not to run it in sensitive directories unless you trust the implementation.

6. **Command Execution**: Be cautious when using the execute tool, as it runs commands with the same permissions as the user running the agent.

7. **Dynamic Tools**: The agent can load custom tools from the `tools_config.json` file. These tools are automatically registered at startup.