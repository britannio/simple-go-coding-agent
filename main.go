package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/invopop/jsonschema"
)

// LoadDynamicTools loads tool definitions from a configuration file
func LoadDynamicTools(configPath string) ([]ToolDefinition, error) {
	configFile, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read tools config file: %w", err)
	}

	var config DynamicToolConfig
	if err := json.Unmarshal(configFile, &config); err != nil {
		return nil, fmt.Errorf("failed to parse tools config: %w", err)
	}

	tools := make([]ToolDefinition, 0, len(config.Tools))
	for _, dynTool := range config.Tools {
		toolDef, err := createDynamicToolDefinition(dynTool)
		if err != nil {
			fmt.Printf("Warning: Failed to create tool %s: %v\n", dynTool.Name, err)
			continue
		}
		tools = append(tools, toolDef)
	}

	return tools, nil
}

// createDynamicToolDefinition converts a DynamicTool config into a ToolDefinition
func createDynamicToolDefinition(config DynamicTool) (ToolDefinition, error) {
	// For simplicity, we'll use a map-based schema directly
	// We don't need the full JSON Schema features
	properties := make(map[string]interface{})
	
	// Define parameters as properties
	for _, param := range config.Parameters {
		properties[param.Name] = map[string]interface{}{
			"type":        "string",
			"description": param.Description,
		}
	}
	
	// Create a simple schema without required fields
	// This is a workaround to avoid compatibility issues
	schema := anthropic.ToolInputSchemaParam{
		Properties: properties,
	}

	// Create the executor function that will handle this tool
	executor := func(input json.RawMessage) (string, error) {
		// Parse the input as a map
		var params map[string]interface{}
		if err := json.Unmarshal(input, &params); err != nil {
			return "", fmt.Errorf("invalid input: %w", err)
		}

		// Process the command template
		cmdTemplate, err := template.New("command").Parse(config.Command)
		if err != nil {
			return "", fmt.Errorf("invalid command template: %w", err)
		}

		// Prepare the template data
		templateData := make(map[string]interface{})
		
		// Apply defaults and check required parameters
		for _, param := range config.Parameters {
			if value, exists := params[param.Name]; exists {
				// Parameter was provided in the input
				templateData[param.Name] = value
			} else if param.Default != "" {
				// Use default value
				templateData[param.Name] = param.Default
			} else if param.Required {
				// Parameter is required but not provided
				return "", fmt.Errorf("missing required parameter: %s", param.Name)
			}
		}

		// Execute the template to get the final command
		var cmdBuffer bytes.Buffer
		if err := cmdTemplate.Execute(&cmdBuffer, templateData); err != nil {
			return "", fmt.Errorf("failed to process command template: %w", err)
		}
		command := cmdBuffer.String()

		// Set timeout
		timeout := config.Timeout
		if timeout <= 0 {
			timeout = 30 // Default timeout
		}
		if timeout > 300 {
			timeout = 300 // Maximum timeout
		}

		// Execute the command (reusing our existing ExecuteCommand logic)
		return ExecuteCommand(json.RawMessage(fmt.Sprintf(`{"command": %q, "timeout": %d}`, command, timeout)))
	}

	return ToolDefinition{
		Name:        config.Name,
		Description: config.Description,
		InputSchema: schema,
		Function:    executor,
	}, nil
}

func main() {
	// Check if debug mode is requested
	debug := os.Getenv("DEBUG") == "1"
	if debug {
		fmt.Println("Debug mode enabled. Tool responses will be printed to the terminal.")
	}

	// Anthropic Client
	client := anthropic.NewClient()

	// Standard in input
	scanner := bufio.NewScanner(os.Stdin)
	// function literal (lambda)??
	// returns the text and a bool representing whether we got the
	// input or not
	getUserMessage := func() (string, bool) {
		if !scanner.Scan() {
			return "", false
		}
		return scanner.Text(), true
	}

	// Start with the built-in tools
	tools := []ToolDefinition{ReadFileDefinition, ListFilesDefinition, EditFileDefinition, GrepDefinition, ExecuteCommandDefinition}
	
	// Try to load dynamic tools from config
	configPath := "tools_config.json"
	if dynamicTools, err := LoadDynamicTools(configPath); err != nil {
		fmt.Printf("Warning: Failed to load dynamic tools: %v\n", err)
	} else {
		fmt.Printf("Loaded %d dynamic tools from %s\n", len(dynamicTools), configPath)
		tools = append(tools, dynamicTools...)
	}

	agent := NewAgent(&client, getUserMessage, tools)
	err := agent.Run(context.TODO())
	if err != nil {
		fmt.Printf("Error: %s\n", err.Error())
	}
}

// initialises the agent struct with an anthropic client and a function to get a user message.
func NewAgent(
	client *anthropic.Client,
	getUserMessage func() (string, bool),
	tools []ToolDefinition,
) *Agent {
	// Check if DEBUG environment variable is set
	debugMode := os.Getenv("DEBUG") == "1"

	return &Agent{
		client:         client,
		getUserMessage: getUserMessage,
		tools:          tools,
		debugMode:      debugMode,
	}
}

type Agent struct {
	client         *anthropic.Client
	getUserMessage func() (string, bool)
	tools          []ToolDefinition
	debugMode      bool
}

func (a *Agent) Run(ctx context.Context) error {
	// the running conversation
	conversation := []anthropic.MessageParam{}
	fmt.Println("Chat with Claude (use 'ctrl-c' to quit)")

	readUserInput := true
	for {
		if readUserInput {
			fmt.Print("\u001b[94mYou\u001b[0m: ")
			// Get a message from the user
			userInput, ok := a.getUserMessage()
			if !ok {
				break
			}

			// Add the user message to the conversation history
			userMessage := anthropic.NewUserMessage(anthropic.NewTextBlock(userInput))
			conversation = append(conversation, userMessage)
		}

		// Send the message to Anthropic for inference
		message, err := a.runInference(ctx, conversation)
		if err != nil {
			return err
		}
		// Add the assistant message to the conversation history
		conversation = append(conversation, message.ToParam())

		toolResults := []anthropic.ContentBlockParamUnion{}
		// Display the AI response
		for _, content := range message.Content {
			switch content.Type {
			case "text":
				fmt.Printf("\u001b[93mClaude\u001b[0m: %s\n", content.Text)
			case "tool_use":
				result := a.executeTool(content.ID, content.Name, content.Input)
				toolResults = append(toolResults, result)
			}
		}
		// If we have tool call results, we should reply with them
		// Otherwise, we don't have anything to reply with until we ask the user
		if len(toolResults) == 0 {
			readUserInput = true
			continue
		}
		// if we're here, we performed a tool call and got a result
		readUserInput = false
		conversation = append(conversation, anthropic.NewUserMessage(toolResults...))
	}

	return nil
}

func (a *Agent) executeTool(id, name string, input json.RawMessage) anthropic.ContentBlockParamUnion {
	var toolDef ToolDefinition
	var found bool
	// Find the tool from our agent's collection of tools
	for _, tool := range a.tools {
		if tool.Name == name {
			toolDef = tool
			found = true
			break
		}
	}

	if !found {
		return anthropic.NewToolResultBlock(id, "tool not found", true)
	}

	fmt.Printf("\u001b[92mtool\u001b[0m: %s(%s)\n", name, input)
	// execute the tool
	response, err := toolDef.Function(input)
	
	// If debug mode is enabled, print the tool response or error
	if a.debugMode {
		if err != nil {
			fmt.Printf("\u001b[96mdebug\u001b[0m: Tool error: %s\n", err.Error())
		} else {
			fmt.Printf("\u001b[96mdebug\u001b[0m: Tool response: %s\n", response)
		}
	}
	
	if err != nil {
		return anthropic.NewToolResultBlock(id, err.Error(), true)
	}
	
	return anthropic.NewToolResultBlock(id, response, false)
}

func (a *Agent) runInference(ctx context.Context, conversation []anthropic.MessageParam) (*anthropic.Message, error) {
	anthropicTools := []anthropic.ToolUnionParam{}
	// go through each tool defined in our agent, and convert it to the type accepted here.
	for _, tool := range a.tools {
		anthropicTools = append(anthropicTools, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        tool.Name,
				Description: anthropic.String(tool.Description),
				InputSchema: tool.InputSchema,
			},
		})
	}

	message, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaude3_7SonnetLatest,
		MaxTokens: int64(1024),
		Messages:  conversation,
		Tools:     anthropicTools,
	})
	return message, err
}

// PathFilter defines a reusable interface for filtering files and directories
type PathFilter interface {
	// ShouldInclude returns true if the path should be included, false otherwise
	ShouldInclude(path string, isDir bool) bool
	// ShouldSkipDir returns true if the directory should be skipped entirely
	ShouldSkipDir(path string) bool
}

// DefaultPathFilter implements basic filtering with common exclusions
type DefaultPathFilter struct {
	// IncludeGit determines whether .git directories should be included
	IncludeGit bool
	// IncludeHidden determines whether hidden files (starting with .) should be included
	IncludeHidden bool
	// CustomExcludes contains additional patterns to exclude
	CustomExcludes []string
}

// NewDefaultPathFilter creates a new filter with sensible defaults
func NewDefaultPathFilter() *DefaultPathFilter {
	return &DefaultPathFilter{
		IncludeGit:    false,
		IncludeHidden: false,
		CustomExcludes: []string{
			// Common binary or large file directories
			"node_modules",
			"vendor",
			"dist",
			"build",
			".venv",
			"__pycache__",
		},
	}
}

// ShouldInclude checks if a path should be included based on the filter settings
func (f *DefaultPathFilter) ShouldInclude(path string, isDir bool) bool {
	// Extract the base name for comparison
	base := filepath.Base(path)

	// Skip .git directory unless explicitly included
	if !f.IncludeGit && (base == ".git" || strings.Contains(path, string(os.PathSeparator)+".git"+string(os.PathSeparator))) {
		return false
	}

	// Skip hidden files/directories if not included
	if !f.IncludeHidden && strings.HasPrefix(base, ".") && base != "." {
		return false
	}

	// Check custom exclusions
	for _, exclude := range f.CustomExcludes {
		// Simple matching for now, could be extended to use glob patterns
		if base == exclude || strings.Contains(path, string(os.PathSeparator)+exclude+string(os.PathSeparator)) {
			return false
		}
	}

	return true
}

// ShouldSkipDir checks if directory traversal should skip this directory
func (f *DefaultPathFilter) ShouldSkipDir(path string) bool {
	base := filepath.Base(path)

	// Always skip .git directory traversal unless explicitly included
	if !f.IncludeGit && base == ".git" {
		return true
	}

	// Skip hidden directories if not included
	if !f.IncludeHidden && strings.HasPrefix(base, ".") && base != "." {
		return true
	}

	// Skip directories in the custom exclude list
	for _, exclude := range f.CustomExcludes {
		if base == exclude {
			return true
		}
	}

	return false
}

type ToolDefinition struct {
	Name        string                         `json:"name"`
	Description string                         `json:"description"`
	InputSchema anthropic.ToolInputSchemaParam `json:"input_schema"`
	Function    func(input json.RawMessage) (string, error)
}

// The read file tool
var ReadFileDefinition = ToolDefinition{
	Name:        "read_file",
	Description: "Read the contents of a given relative file path. Use this when you want to see what's inside a file. Do not use this with directory names.",
	InputSchema: ReadFileInputSchema,
	Function:    ReadFile,
}

// The list files tool
var ListFilesDefinition = ToolDefinition{
	Name:        "list_files",
	Description: "List files and directories at a given path. If no path is provided, lists files in the current directory. By default excludes .git directory, hidden files, and common directories like node_modules. Use include_git, include_hidden, and exclude parameters to customize filtering.",
	InputSchema: ListFilesInputSchema,
	Function:    ListFiles,
}

var EditFileDefinition = ToolDefinition{
	Name: "edit_file",
	Description: `Make edits to a text file.

Replaces 'old_str' with 'new_str' in the given file. 'old_str' and 'new_str' MUST be different from each other.

If the file specified with path doesn't exist, it will be created.
`,
	InputSchema: EditFileInputSchema,
	Function:    EditFile,
}

// The grep tool
var GrepDefinition = ToolDefinition{
	Name:        "grep",
	Description: "Search for a regular expression pattern in files. Returns matching lines with file names and line numbers. By default excludes .git directory, hidden files, and common directories like node_modules. Use include_git, include_hidden, and exclude parameters to customize filtering.",
	InputSchema: GrepInputSchema,
	Function:    Grep,
}

// The execute command tool
var ExecuteCommandDefinition = ToolDefinition{
	Name:        "execute",
	Description: "Execute a shell command and return its output. The command is executed in a bash shell on Unix-like systems and cmd on Windows. Has a configurable timeout (default 30 seconds, max 5 minutes). Returns stdout, stderr, and exit code.",
	InputSchema: ExecuteCommandInputSchema,
	Function:    ExecuteCommand,
}

type ReadFileInput struct {
	// the file path input, annotated with a json name and description
	Path string `json:"path" jsonschema_description:"The relative path of a file in the working directory."`
}
type ListFilesInput struct {
	Path          string   `json:"path,omitempty" jsonschema_description:"Optional relative path to list files from. Defaults to current directory if not provided."`
	IncludeGit    bool     `json:"include_git,omitempty" jsonschema_description:"Set to true to include .git directory in results. Defaults to false."`
	IncludeHidden bool     `json:"include_hidden,omitempty" jsonschema_description:"Set to true to include hidden files and directories (starting with .). Defaults to false."`
	Exclude       []string `json:"exclude,omitempty" jsonschema_description:"Optional list of directories or files to exclude from results."`
}
type EditFileInput struct {
	Path   string `json:"path" jsonschema_description:"The path to the file"`
	OldStr string `json:"old_str" jsonschema_description:"Text to search for - must match exactly and must only have one match exactly"`
	NewStr string `json:"new_str" jsonschema_description:"Text to replace old_str with"`
}
type GrepInput struct {
	Pattern       string   `json:"pattern" jsonschema_description:"The regular expression pattern to search for in files"`
	Path          string   `json:"path,omitempty" jsonschema_description:"Optional relative path to search in. Defaults to current directory if not provided"`
	IncludeGit    bool     `json:"include_git,omitempty" jsonschema_description:"Set to true to include .git directory in search. Defaults to false."`
	IncludeHidden bool     `json:"include_hidden,omitempty" jsonschema_description:"Set to true to include hidden files and directories (starting with .). Defaults to false."`
	Exclude       []string `json:"exclude,omitempty" jsonschema_description:"Optional list of directories or files to exclude from search."`
}

type ExecuteCommandInput struct {
	Command string `json:"command" jsonschema_description:"The shell command to execute (bash on Unix/Linux/macOS, cmd on Windows)"`
	Timeout int    `json:"timeout,omitempty" jsonschema_description:"Optional timeout in seconds. Default is 30 seconds. Maximum is 300 seconds (5 minutes)."`
}

// Configuration for dynamic tool loading
type DynamicToolConfig struct {
	Tools []DynamicTool `json:"tools"`
}

type DynamicTool struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Command     string             `json:"command"`
	Timeout     int                `json:"timeout"`
	Parameters  []ToolParameter    `json:"parameters"`
}

type ToolParameter struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Default     string `json:"default,omitempty"`
}

var ReadFileInputSchema = GenerateSchema[ReadFileInput]()
var ListFilesInputSchema = GenerateSchema[ListFilesInput]()
var EditFileInputSchema = GenerateSchema[EditFileInput]()
var GrepInputSchema = GenerateSchema[GrepInput]()
var ExecuteCommandInputSchema = GenerateSchema[ExecuteCommandInput]()

// generics magic?
func GenerateSchema[T any]() anthropic.ToolInputSchemaParam {
	reflector := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		DoNotReference:            true,
	}
	var v T

	schema := reflector.Reflect(v)

	return anthropic.ToolInputSchemaParam{
		Properties: schema.Properties,
	}
}

func ReadFile(input json.RawMessage) (string, error) {
	readFileInput := ReadFileInput{}
	// Parse the JSON supplied by the LLM (conforms to our json schema definition of the tool)
	err := json.Unmarshal(input, &readFileInput)
	if err != nil {
		panic(err)
	}

	// Read a file from the OS based on the path
	content, err := os.ReadFile(readFileInput.Path)
	if err != nil {
		return "", err
	}
	// return the contents of the file
	return string(content), nil
}

func ListFiles(input json.RawMessage) (string, error) {
	listFilesInput := ListFilesInput{}
	err := json.Unmarshal(input, &listFilesInput)
	if err != nil {
		panic(err)
	}

	dir := "."
	if listFilesInput.Path != "" {
		dir = listFilesInput.Path
	}

	// Create path filter based on user options
	filter := &DefaultPathFilter{
		IncludeGit:    listFilesInput.IncludeGit,
		IncludeHidden: listFilesInput.IncludeHidden,
		CustomExcludes: listFilesInput.Exclude,
	}

	var files []string
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		// Skip current directory
		if relPath == "." {
			return nil
		}

		// Check if the directory should be skipped entirely
		if info.IsDir() && filter.ShouldSkipDir(relPath) {
			return filepath.SkipDir
		}

		// Check if the file/directory should be included
		if filter.ShouldInclude(relPath, info.IsDir()) {
			if info.IsDir() {
				files = append(files, relPath+"/")
			} else {
				files = append(files, relPath)
			}
		}
		return nil
	})

	if err != nil {
		return "", err
	}

	result, err := json.Marshal(files)
	if err != nil {
		return "", err
	}

	return string(result), nil
}

func EditFile(input json.RawMessage) (string, error) {
	editFileInput := EditFileInput{}
	err := json.Unmarshal(input, &editFileInput)
	if err != nil {
		return "", err
	}

	if editFileInput.Path == "" || editFileInput.OldStr == editFileInput.NewStr {
		return "", fmt.Errorf("invalid input parameters")
	}

	content, err := os.ReadFile(editFileInput.Path)
	if err != nil {
		if os.IsNotExist(err) && editFileInput.OldStr == "" {
			return createNewFile(editFileInput.Path, editFileInput.NewStr)
		}
		return "", err
	}

	oldContent := string(content)
	newContent := strings.Replace(oldContent, editFileInput.OldStr, editFileInput.NewStr, -1)

	if oldContent == newContent && editFileInput.OldStr != "" {
		return "", fmt.Errorf("old_str not found in file")
	}

	err = os.WriteFile(editFileInput.Path, []byte(newContent), 0644)
	if err != nil {
		return "", err
	}

	return "OK", nil
}

func createNewFile(filePath, content string) (string, error) {
	dir := path.Dir(filePath)
	if dir != "." {
		err := os.MkdirAll(dir, 0755)
		if err != nil {
			return "", fmt.Errorf("failed to create directory: %w", err)
		}
	}

	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}

	return fmt.Sprintf("Successfully created file %s", filePath), nil
}

func Grep(input json.RawMessage) (string, error) {
	grepInput := GrepInput{}
	err := json.Unmarshal(input, &grepInput)
	if err != nil {
		return "", err
	}

	if grepInput.Pattern == "" {
		return "", fmt.Errorf("pattern cannot be empty")
	}

	// Compile the regular expression
	regex, err := regexp.Compile(grepInput.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regular expression: %w", err)
	}

	// Set the search directory
	searchDir := "."
	if grepInput.Path != "" {
		searchDir = grepInput.Path
	}

	// Create path filter based on user options
	filter := &DefaultPathFilter{
		IncludeGit:     grepInput.IncludeGit,
		IncludeHidden:  grepInput.IncludeHidden,
		CustomExcludes: grepInput.Exclude,
	}

	// Store matches as a slice of map entries for JSON serialization
	type Match struct {
		File    string `json:"file"`
		Line    int    `json:"line"`
		Content string `json:"content"`
	}
	matches := []Match{}

	// Walk through all files in the directory
	err = filepath.Walk(searchDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Get relative path
		relPath, err := filepath.Rel(searchDir, path)
		if err != nil {
			return err
		}
		
		// Skip current directory
		if relPath == "." {
			return nil
		}

		// Check if the directory should be skipped entirely
		if info.IsDir() && filter.ShouldSkipDir(relPath) {
			return filepath.SkipDir
		}

		// Skip directories and files that should not be included
		if !filter.ShouldInclude(relPath, info.IsDir()) || info.IsDir() {
			return nil
		}

		// Read the file
		data, err := os.ReadFile(path)
		if err != nil {
			return nil // Skip files we can't read
		}

		// Skip binary files (simple check)
		if len(data) > 0 && data[0] == 0 {
			return nil
		}

		// Process the file line by line
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if regex.MatchString(line) {
				matches = append(matches, Match{
					File:    relPath,
					Line:    lineNum,
					Content: line,
				})
			}
		}

		return nil
	})

	if err != nil {
		return "", err
	}

	if len(matches) == 0 {
		return "No matches found.", nil
	}

	// Convert to JSON
	result, err := json.MarshalIndent(matches, "", "  ")
	if err != nil {
		return "", err
	}

	return string(result), nil
}

func ExecuteCommand(input json.RawMessage) (string, error) {
	executeCommandInput := ExecuteCommandInput{}
	err := json.Unmarshal(input, &executeCommandInput)
	if err != nil {
		return "", err
	}

	if executeCommandInput.Command == "" {
		return "", fmt.Errorf("command cannot be empty")
	}

	// Set default timeout if not specified
	timeout := 30
	if executeCommandInput.Timeout > 0 {
		timeout = executeCommandInput.Timeout
	}
	// Cap timeout at 5 minutes
	if timeout > 300 {
		timeout = 300
	}

	// Define shell to use based on OS
	var cmd *exec.Cmd
	if os.PathSeparator == '/' { // Unix-like
		cmd = exec.Command("bash", "-c", executeCommandInput.Command)
	} else { // Windows
		cmd = exec.Command("cmd", "/C", executeCommandInput.Command)
	}

	// Capture stdout and stderr
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	// Make the command use the context
	cmd = exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run the command
	err = cmd.Run()

	// Create a structured response with both stdout and stderr
	type CommandResult struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exit_code"`
	}

	exitCode := 0
	if err != nil {
		// Try to get the exit code
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("command timed out after %d seconds", timeout)
		} else {
			return "", fmt.Errorf("failed to execute command: %w", err)
		}
	}

	// Create the result
	result := CommandResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}

	// Convert to JSON
	resultJson, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}

	return string(resultJson), nil
}