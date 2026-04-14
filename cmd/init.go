package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/murdinc/lcdata/internal/lcdata"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init [name] [type]",
	Short: "Scaffold a new node directory",
	Long: `Create a new node directory with a type-appropriate JSON config and stubs.

Supported types: llm, pipeline, http, command, transform, database, stt, tts, search, file

Examples:
  lcdata init my_agent llm
  lcdata init fetch_page http
  lcdata init process_docs pipeline`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		nodeType := strings.ToLower(args[1])

		cfg, err := lcdata.LoadConfig()
		if err != nil {
			return err
		}

		dirPath := filepath.Join(cfg.NodesPath, name)
		if _, err := os.Stat(dirPath); err == nil {
			return fmt.Errorf("node directory already exists: %s", dirPath)
		}

		if err := os.MkdirAll(dirPath, 0755); err != nil {
			return fmt.Errorf("failed to create node directory: %w", err)
		}

		configPath := filepath.Join(dirPath, name+".json")
		configContent, err := nodeTemplate(name, nodeType, dirPath)
		if err != nil {
			os.RemoveAll(dirPath)
			return err
		}

		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			os.RemoveAll(dirPath)
			return fmt.Errorf("failed to write config: %w", err)
		}

		// Write system prompt stub for LLM nodes
		if nodeType == "llm" {
			systemPath := filepath.Join(dirPath, "system.md")
			stub := "You are a helpful assistant.\n\n# Instructions\n\nDescribe the assistant's role and constraints here.\n"
			if err := os.WriteFile(systemPath, []byte(stub), 0644); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to write system.md: %v\n", err)
			}
		}

		fmt.Printf("Created node %q (%s) in %s\n", name, nodeType, dirPath)
		fmt.Printf("  %s\n", configPath)
		if nodeType == "llm" {
			fmt.Printf("  %s\n", filepath.Join(dirPath, "system.md"))
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}

func nodeTemplate(name, nodeType, dir string) (string, error) {
	switch nodeType {
	case "llm":
		return fmt.Sprintf(`{
  "name": %q,
  "description": "LLM node",
  "type": "llm",
  "provider": "anthropic",
  "model": "claude-sonnet-4-6",
  "system_prompt_file": "system.md",
  "temperature": 0.7,
  "max_tokens": 4096,
  "stream": true,
  "input": {
    "message": { "type": "string", "required": true }
  },
  "output": {
    "response": { "type": "string" }
  }
}
`, name), nil

	case "pipeline":
		return fmt.Sprintf(`{
  "name": %q,
  "description": "Pipeline node",
  "type": "pipeline",
  "input": {
    "message": { "type": "string", "required": true }
  },
  "steps": [
    {
      "id": "step1",
      "node": "some_node",
      "input": {
        "message": "{{.input.message}}"
      }
    }
  ],
  "output": {
    "result": "{{.step1.response}}"
  }
}
`, name), nil

	case "http":
		return fmt.Sprintf(`{
  "name": %q,
  "description": "HTTP node",
  "type": "http",
  "method": "GET",
  "url": "https://example.com/api/endpoint",
  "headers": {
    "Accept": "application/json"
  },
  "strip_html": false,
  "input": {
    "query": { "type": "string", "required": true }
  },
  "output": {
    "body":        { "type": "string" },
    "status_code": { "type": "number" }
  }
}
`, name), nil

	case "command":
		return fmt.Sprintf(`{
  "name": %q,
  "description": "Command node",
  "type": "command",
  "command": "echo",
  "args": ["{{.input.message}}"],
  "timeout": "30s",
  "input": {
    "message": { "type": "string", "required": true }
  },
  "output": {
    "stdout":      { "type": "string" },
    "exit_code":   { "type": "number" }
  }
}
`, name), nil

	case "transform":
		return fmt.Sprintf(`{
  "name": %q,
  "description": "Transform node",
  "type": "transform",
  "template": "{{.input.value}}",
  "input": {
    "value": { "type": "string", "required": true }
  },
  "output": {
    "result": { "type": "string" }
  }
}
`, name), nil

	case "database":
		return fmt.Sprintf(`{
  "name": %q,
  "description": "Database node",
  "type": "database",
  "driver": "sqlite",
  "connection": "./data.db",
  "query": "SELECT * FROM table WHERE id = ?",
  "params": ["{{.input.id}}"],
  "input": {
    "id": { "type": "string", "required": true }
  },
  "output": {
    "rows":  { "type": "array" },
    "count": { "type": "number" }
  }
}
`, name), nil

	case "stt":
		return fmt.Sprintf(`{
  "name": %q,
  "description": "Speech-to-text node",
  "type": "stt",
  "provider": "deepgram",
  "model": "nova-2",
  "language": "en",
  "input": {
    "url": { "type": "string", "required": true }
  },
  "output": {
    "transcript": { "type": "string" },
    "confidence": { "type": "number" }
  }
}
`, name), nil

	case "tts":
		return fmt.Sprintf(`{
  "name": %q,
  "description": "Text-to-speech node",
  "type": "tts",
  "provider": "elevenlabs",
  "model": "eleven_multilingual_v2",
  "voice_id": "21m00Tcm4TlvDq8ikWAM",
  "input": {
    "text": { "type": "string", "required": true }
  },
  "output": {
    "audio_base64": { "type": "string" },
    "content_type":  { "type": "string" },
    "size_bytes":    { "type": "number" }
  }
}
`, name), nil

	case "search":
		return fmt.Sprintf(`{
  "name": %q,
  "description": "Search node",
  "type": "search",
  "search_provider": "brave",
  "search_count": 5,
  "input": {
    "query": { "type": "string", "required": true }
  },
  "output": {
    "results": { "type": "array" },
    "count":   { "type": "number" }
  }
}
`, name), nil

	case "file":
		return fmt.Sprintf(`{
  "name": %q,
  "description": "File node",
  "type": "file",
  "operation": "read",
  "input": {
    "path": { "type": "string", "required": true }
  },
  "output": {
    "content": { "type": "string" }
  }
}
`, name), nil

	default:
		os.RemoveAll(dir)
		return "", fmt.Errorf("unknown node type %q — supported: llm, pipeline, http, command, transform, database, stt, tts, search, file", nodeType)
	}
}
