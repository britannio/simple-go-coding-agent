package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/invopop/jsonschema"
)

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

	tools := []ToolDefinition{ReadFileDefinition, ListFilesDefinition, EditFileDefinition, GrepDefinition}
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

type ToolDefinition struct {
	Name        string                         `json:"name`
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
	Description: "List files and directories at a given path. If no path is provided, lists files in the current directory.",
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
	Description: "Search for a regular expression pattern in files. Returns matching lines with file names and line numbers.",
	InputSchema: GrepInputSchema,
	Function:    Grep,
}

type ReadFileInput struct {
	// the file path input, annotated with a json name and description
	Path string `json:"path" jsonschema_description:"The relative path of a file in the working directory."`
}
type ListFilesInput struct {
	Path string `json:"path,omitempty" jsonschema_description:"Optional relative path to list files from. Defaults to current directory if not provided."`
}
type EditFileInput struct {
	Path   string `json:"path" jsonschema_description:"The path to the file"`
	OldStr string `json:"old_str" jsonschema_description:"Text to search for - must match exactly and must only have one match exactly"`
	NewStr string `json:"new_str" jsonschema_description:"Text to replace old_str with"`
}
type GrepInput struct {
	Pattern string `json:"pattern" jsonschema_description:"The regular expression pattern to search for in files"`
	Path    string `json:"path,omitempty" jsonschema_description:"Optional relative path to search in. Defaults to current directory if not provided"`
}

var ReadFileInputSchema = GenerateSchema[ReadFileInput]()
var ListFilesInputSchema = GenerateSchema[ListFilesInput]()
var EditFileInputSchema = GenerateSchema[EditFileInput]()
var GrepInputSchema = GenerateSchema[GrepInput]()

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

	var files []string
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		if relPath != "." {
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

		// Skip directories
		if info.IsDir() {
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
				// Get relative path
				relPath, err := filepath.Rel(searchDir, path)
				if err != nil {
					relPath = path
				}
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
