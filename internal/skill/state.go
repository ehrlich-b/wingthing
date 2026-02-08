package skill

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const stateFile = "skill_state.json"

type State struct {
	Disabled []string `json:"disabled"`
}

func LoadState(dir string) (*State, error) {
	data, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		if os.IsNotExist(err) {
			return &State{}, nil
		}
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *State) Save(dir string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, stateFile), data, 0644)
}

func (s *State) IsEnabled(name string) bool {
	for _, d := range s.Disabled {
		if d == name {
			return false
		}
	}
	return true
}

func (s *State) Enable(name string) {
	out := s.Disabled[:0]
	for _, d := range s.Disabled {
		if d != name {
			out = append(out, d)
		}
	}
	s.Disabled = out
}

func (s *State) Disable(name string) {
	if !s.IsEnabled(name) {
		return
	}
	s.Disabled = append(s.Disabled, name)
}
