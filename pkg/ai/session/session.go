package session

import (
	"sync"

	"github.com/flanksource/captain/pkg/ai"
)

type Session struct {
	ID          string
	ProjectName string
	Costs       ai.Costs
	mu          sync.RWMutex
}

func New(id, projectName string) *Session {
	return &Session{
		ID:          id,
		ProjectName: projectName,
	}
}

func (s *Session) AddCost(cost ai.Cost) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Costs = append(s.Costs, cost)
}

func (s *Session) TotalCost() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Costs.Sum().Total()
}

func (s *Session) GetCosts() ai.Costs {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(ai.Costs, len(s.Costs))
	copy(result, s.Costs)
	return result
}
