package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"
	"gopkg.in/yaml.v3"
)

type Session struct {
	ID       string
	Title    string
	Profile  string
	Created  time.Time
	Updated  time.Time
	FilePath string
}

type sessionFrontmatter struct {
	ID      string    `yaml:"id"`
	Title   string    `yaml:"title"`
	Profile string    `yaml:"profile"`
	Created time.Time `yaml:"created"`
	Updated time.Time `yaml:"updated"`
}

func SessionDir(profileName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "lain", "profiles", profileName, "sessions")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

func NewSession(profileName string) *Session {
	now := time.Now()
	return &Session{
		ID:      now.Format("20060102-150405"),
		Title:   "New Session",
		Profile: profileName,
		Created: now,
		Updated: now,
	}
}

func (s *Session) Path() (string, error) {
	dir, err := SessionDir(s.Profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, s.ID+".md"), nil
}

func SaveSession(session *Session, messages []ChatMessage) error {
	path, err := session.Path()
	if err != nil {
		return err
	}
	session.Updated = time.Now()

	fm := sessionFrontmatter{
		ID:      session.ID,
		Title:   session.Title,
		Profile: session.Profile,
		Created: session.Created,
		Updated: session.Updated,
	}
	fmBytes, err := yaml.Marshal(fm)
	if err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.Write(fmBytes)
	b.WriteString("---\n\n")
	b.WriteString(marshalMessages(messages))

	return os.WriteFile(path, []byte(b.String()), 0644)
}

func LoadSession(path string) (*Session, []ChatMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	content := string(data)

	if !strings.HasPrefix(content, "---\n") {
		return nil, nil, fmt.Errorf("invalid session file: missing frontmatter")
	}
	endIdx := strings.Index(content[4:], "\n---\n")
	if endIdx == -1 {
		return nil, nil, fmt.Errorf("invalid session file: unterminated frontmatter")
	}

	fmData := content[4 : 4+endIdx]
	body := content[4+endIdx+5:]

	var fm sessionFrontmatter
	if err := yaml.Unmarshal([]byte(fmData), &fm); err != nil {
		return nil, nil, fmt.Errorf("invalid frontmatter: %w", err)
	}

	session := &Session{
		ID:       fm.ID,
		Title:    fm.Title,
		Profile:  fm.Profile,
		Created:  fm.Created,
		Updated:  fm.Updated,
		FilePath: path,
	}

	messages, err := unmarshalMessages(body)
	if err != nil {
		return nil, nil, err
	}

	return session, messages, nil
}

func ListSessions(profileName string) ([]Session, error) {
	dir, err := SessionDir(profileName)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []Session
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := string(data)
		if !strings.HasPrefix(content, "---\n") {
			continue
		}
		endIdx := strings.Index(content[4:], "\n---\n")
		if endIdx == -1 {
			continue
		}

		var fm sessionFrontmatter
		if err := yaml.Unmarshal([]byte(content[4:4+endIdx]), &fm); err != nil {
			continue
		}

		sessions = append(sessions, Session{
			ID:       fm.ID,
			Title:    fm.Title,
			Profile:  fm.Profile,
			Created:  fm.Created,
			Updated:  fm.Updated,
			FilePath: path,
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Updated.After(sessions[j].Updated)
	})

	return sessions, nil
}

func RenameSession(session *Session, title string) error {
	session.Title = title
	path, err := session.Path()
	if err != nil {
		return err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	content := string(data)
	if !strings.HasPrefix(content, "---\n") {
		return fmt.Errorf("invalid session file")
	}
	endIdx := strings.Index(content[4:], "\n---\n")
	if endIdx == -1 {
		return fmt.Errorf("invalid session file")
	}

	var fm sessionFrontmatter
	if err := yaml.Unmarshal([]byte(content[4:4+endIdx]), &fm); err != nil {
		return err
	}
	fm.Title = title
	fmBytes, err := yaml.Marshal(fm)
	if err != nil {
		return err
	}

	body := content[4+endIdx+5:]

	var b strings.Builder
	b.WriteString("---\n")
	b.Write(fmBytes)
	b.WriteString("---\n\n")
	b.WriteString(body)

	return os.WriteFile(path, []byte(b.String()), 0644)
}

func SessionPreview(path string, maxLen int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	content := string(data)
	if strings.HasPrefix(content, "---\n") {
		endIdx := strings.Index(content[4:], "\n---\n")
		if endIdx != -1 {
			content = content[4+endIdx+5:]
		}
	}
	content = strings.TrimSpace(content)
	if len(content) > maxLen {
		content = content[:maxLen] + "..."
	}
	return content
}

func GenerateTitle(client *openai.Client, model string, userMsg, assistantMsg string) string {
	truncated := assistantMsg
	if len(truncated) > 500 {
		truncated = truncated[:500]
	}

	req := openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: "system", Content: "Generate a very short title (3-6 words) for this conversation. Output only the title, nothing else."},
			{Role: "user", Content: userMsg},
			{Role: "assistant", Content: truncated},
			{Role: "user", Content: "Title:"},
		},
		Temperature: 0.3,
		MaxTokens:   30,
	}

	resp, err := client.CreateChatCompletion(context.Background(), req)
	if err != nil || len(resp.Choices) == 0 {
		return ""
	}

	title := strings.TrimSpace(resp.Choices[0].Message.Content)
	title = strings.Trim(title, "\"'")
	return title
}

func BuildHistoryFromMessages(messages []ChatMessage) []openai.ChatCompletionMessage {
	var history []openai.ChatCompletionMessage
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			var content string
			for _, block := range msg.Blocks {
				if block.Type == "content" {
					content = block.Content
					break
				}
			}
			if content != "" {
				history = append(history, openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleUser,
					Content: content,
				})
			}
		case "assistant":
			var parts []string
			for _, block := range msg.Blocks {
				if block.Type == "content" && block.Content != "" {
					parts = append(parts, block.Content)
				}
			}
			if len(parts) > 0 {
				history = append(history, openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleAssistant,
					Content: strings.Join(parts, "\n\n"),
				})
			}
		}
	}
	return history
}

func marshalMessages(messages []ChatMessage) string {
	var b strings.Builder
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			for _, block := range msg.Blocks {
				if block.Type == "content" {
					b.WriteString("## User\n\n")
					b.WriteString(block.Content)
					b.WriteString("\n\n")
				}
			}
		case "assistant":
			for _, block := range msg.Blocks {
				switch block.Type {
				case "content":
					if block.Content != "" {
						b.WriteString("## Assistant\n\n")
						b.WriteString(block.Content)
						b.WriteString("\n\n")
					}
				case "tool_call":
					b.WriteString("## Tool: ")
					name := block.Name
					if parenIdx := strings.Index(name, "("); parenIdx > 0 {
						b.WriteString(name[:parenIdx])
						args := name[parenIdx+1:]
						if strings.HasSuffix(args, ")") {
							args = args[:len(args)-1]
						}
						b.WriteString("\n\n**Args:** `" + args + "`\n\n**Output:**\n```\n")
					} else {
						b.WriteString(name)
						b.WriteString("\n\n**Output:**\n```\n")
					}
					if block.Output != "" {
						output := block.Output
						if colonIdx := strings.Index(output, ": "); colonIdx > 0 {
							output = output[colonIdx+2:]
						}
						b.WriteString(output)
					}
					b.WriteString("\n```\n\n")
				case "compaction":
					b.WriteString("## Compaction\n\n")
					b.WriteString(block.Content)
					b.WriteString("\n\n")
				case "error":
					b.WriteString("## Error\n\n")
					b.WriteString(block.Content)
					b.WriteString("\n\n")
				}
			}
		}
	}
	return b.String()
}

type markdownSection struct {
	Heading string
	Body    string
}

func parseMarkdownSections(body string) []markdownSection {
	var sections []markdownSection
	inCodeBlock := false
	var current *markdownSection

	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			if current != nil {
				current.Body += line + "\n"
			}
			continue
		}

		if !inCodeBlock && strings.HasPrefix(trimmed, "## ") {
			if current != nil {
				sections = append(sections, *current)
			}
			current = &markdownSection{Heading: strings.TrimPrefix(trimmed, "## ")}
		} else if current != nil {
			current.Body += line + "\n"
		}
	}

	if current != nil {
		sections = append(sections, *current)
	}
	return sections
}

func unmarshalMessages(body string) ([]ChatMessage, error) {
	var messages []ChatMessage
	var currentAssistant *ChatMessage

	for _, sec := range parseMarkdownSections(body) {
		switch {
		case sec.Heading == "User":
			if currentAssistant != nil {
				messages = append(messages, *currentAssistant)
				currentAssistant = nil
			}
			messages = append(messages, ChatMessage{
				Role:   "user",
				Blocks: []MessageBlock{{Type: "content", Content: strings.TrimSpace(sec.Body), Done: true}},
			})
		case sec.Heading == "Assistant":
			if currentAssistant == nil {
				currentAssistant = &ChatMessage{Role: "assistant"}
			}
			content := strings.TrimSpace(sec.Body)
			if content != "" {
				currentAssistant.Blocks = append(currentAssistant.Blocks, MessageBlock{
					Type:    "content",
					Content: content,
					Done:    true,
				})
			}
		case strings.HasPrefix(sec.Heading, "Tool: "):
			if currentAssistant == nil {
				currentAssistant = &ChatMessage{Role: "assistant"}
			}
			toolName := strings.TrimPrefix(sec.Heading, "Tool: ")
			currentAssistant.Blocks = append(currentAssistant.Blocks, MessageBlock{
				Type:   "tool_call",
				Name:   toolName,
				Output: extractToolOutput(sec.Body),
				Done:   true,
			})
		case sec.Heading == "Compaction":
			if currentAssistant == nil {
				currentAssistant = &ChatMessage{Role: "assistant"}
			}
			currentAssistant.Blocks = append(currentAssistant.Blocks, MessageBlock{
				Type:    "compaction",
				Content: strings.TrimSpace(sec.Body),
				Done:    true,
			})
		case sec.Heading == "Error":
			if currentAssistant == nil {
				currentAssistant = &ChatMessage{Role: "assistant"}
			}
			currentAssistant.Blocks = append(currentAssistant.Blocks, MessageBlock{
				Type:    "error",
				Content: strings.TrimSpace(sec.Body),
				Done:    true,
			})
		}
	}

	if currentAssistant != nil {
		messages = append(messages, *currentAssistant)
	}
	return messages, nil
}

func extractToolOutput(body string) string {
	marker := "**Output:**"
	idx := strings.Index(body, marker)
	if idx == -1 {
		return ""
	}
	after := body[idx+len(marker):]
	codeStart := strings.Index(after, "```\n")
	if codeStart == -1 {
		return ""
	}
	after = after[codeStart+4:]
	codeEnd := strings.Index(after, "\n```")
	if codeEnd == -1 {
		return strings.TrimSpace(after)
	}
	return strings.TrimSpace(after[:codeEnd])
}
