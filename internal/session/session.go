package session

import "llmc/internal/provider"

type Session struct {
	Name     string
	Provider string
	Model    string
	System   *string
	Messages []provider.Message
}

type Store struct{}

func NewStore() *Store { return &Store{} }

func (s *Store) Load(name string) (*Session, error) { return nil, nil }

func (s *Store) Save(session *Session) error { return nil }

func (s *Store) List() ([]string, error) { return nil, nil }
