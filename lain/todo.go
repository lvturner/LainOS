package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type TodoItem struct {
	ID          int        `json:"id"`
	Task        string     `json:"task"`
	Completed   bool       `json:"completed"`
	Created     time.Time  `json:"created"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type TodoStore struct {
	mu       sync.Mutex
	items    []TodoItem
	nextID   int
	filePath string
	inMemory bool
}

func NewTodoStore(filePath string) *TodoStore {
	s := &TodoStore{
		filePath: filePath,
	}
	s.load()
	return s
}

func NewMemoryTodoStore() *TodoStore {
	return &TodoStore{
		inMemory: true,
	}
}

func (s *TodoStore) load() {
	if s.inMemory || s.filePath == "" {
		return
	}
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			s.items = []TodoItem{}
			return
		}
		return
	}
	if len(data) == 0 {
		s.items = []TodoItem{}
		return
	}
	var items []TodoItem
	if err := json.Unmarshal(data, &items); err != nil {
		return
	}
	s.items = items
	for _, item := range items {
		if item.ID >= s.nextID {
			s.nextID = item.ID + 1
		}
	}
}

func (s *TodoStore) save() error {
	if s.inMemory || s.filePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath, data, 0644)
}

func (s *TodoStore) Add(task string) TodoItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := TodoItem{
		ID:      s.nextID,
		Task:    task,
		Created: time.Now(),
	}
	s.nextID++
	s.items = append(s.items, item)
	s.save()
	return item
}

func (s *TodoStore) List() []TodoItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]TodoItem, len(s.items))
	copy(result, s.items)
	return result
}

func (s *TodoStore) Complete(id int) (TodoItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID == id {
			s.items[i].Completed = true
			now := time.Now()
			s.items[i].CompletedAt = &now
			s.save()
			return s.items[i], nil
		}
	}
	return TodoItem{}, fmt.Errorf("task %d not found", id)
}

func (s *TodoStore) Uncomplete(id int) (TodoItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID == id {
			s.items[i].Completed = false
			s.items[i].CompletedAt = nil
			s.save()
			return s.items[i], nil
		}
	}
	return TodoItem{}, fmt.Errorf("task %d not found", id)
}

func (s *TodoStore) Remove(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID == id {
			s.items = append(s.items[:i], s.items[i+1:]...)
			s.save()
			return nil
		}
	}
	return fmt.Errorf("task %d not found", id)
}

func (s *TodoStore) Clear() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	cleared := 0
	var remaining []TodoItem
	for _, item := range s.items {
		if item.Completed {
			cleared++
		} else {
			remaining = append(remaining, item)
		}
	}
	s.items = remaining
	s.save()
	return cleared
}

func (s *TodoStore) FormatList() string {
	items := s.List()
	if len(items) == 0 {
		return "No tasks."
	}
	var result string
	for _, item := range items {
		marker := "☐"
		if item.Completed {
			marker = "☑"
		}
		result += fmt.Sprintf("%s %d. %s\n", marker, item.ID, item.Task)
	}
	return result
}
