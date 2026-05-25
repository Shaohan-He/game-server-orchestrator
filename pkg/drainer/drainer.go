package drainer

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/noneedtostudy/game-server-orchestrator/api/v1alpha1"
)

type DrainPhase string

const (
	PhaseCordon       DrainPhase = "CORDON"
	PhaseDrain        DrainPhase = "DRAIN"
	PhaseDecommission DrainPhase = "DECOMMISSION"
	PhaseDone         DrainPhase = "DONE"
	PhaseForced       DrainPhase = "FORCED"
)

type ServerState struct {
	Name           string
	Endpoint       string
	Phase          DrainPhase
	ActiveSessions int32
	EnteredAt      time.Time
	LastCheckedAt  time.Time
	ForcedDeadline time.Time
}

// SessionQuerier queries active session counts from game servers.
type SessionQuerier interface {
	ActiveSessions(ctx context.Context, endpoint string) (int32, error)
}

type Drainer struct {
	timeout   time.Duration
	interval  time.Duration
	forceTime time.Duration
	sessions  SessionQuerier
}

func New(policy v1alpha1.DrainConfig, sq SessionQuerier) *Drainer {
	return &Drainer{
		timeout:   time.Duration(policy.TimeoutSeconds) * time.Second,
		interval:  time.Duration(policy.IntervalSeconds) * time.Second,
		forceTime: time.Duration(policy.ForceAfterSeconds) * time.Second,
		sessions:  sq,
	}
}

// StartDrain begins the three-phase drain protocol for a server.
func (d *Drainer) StartDrain(serverName, endpoint string) *ServerState {
	now := time.Now()
	s := &ServerState{
		Name:           serverName,
		Endpoint:       endpoint,
		Phase:          PhaseCordon,
		ActiveSessions: 0,
		EnteredAt:      now,
		LastCheckedAt:  now,
		ForcedDeadline: now.Add(d.forceTime),
	}
	log.Printf("[OK] [drainer] %s → %s (drain started)", serverName, PhaseCordon)
	return s
}

// Advance moves the drain state machine forward.
// Returns true if the server is ready for decommission (i.e. the caller should delete the Pod).
func (d *Drainer) Advance(ctx context.Context, s *ServerState) (bool, error) {
	endpoint := s.Endpoint
	now := time.Now()

	switch s.Phase {
	case PhaseCordon:
		// Cordon is immediate — transition to DRAIN phase.
		s.Phase = PhaseDrain
		s.LastCheckedAt = now
		log.Printf("[OK] [drainer] %s → %s (cordon complete)", s.Name, PhaseDrain)
		return false, nil

	case PhaseDrain:
		// Check if we've exceeded the force deadline.
		if d.forceTime > 0 && now.After(s.ForcedDeadline) {
			s.Phase = PhaseForced
			log.Printf("[WARN] [drainer] %s → %s (force deadline exceeded)", s.Name, PhaseForced)
			return true, nil
		}

		// Query active sessions.
		count, err := d.sessions.ActiveSessions(ctx, endpoint)
		if err != nil {
			log.Printf("[WARN] [drainer] %s → session query failed: %v", s.Name, err)
			return false, fmt.Errorf("session query for %s: %w", s.Name, err)
		}

		s.ActiveSessions = count
		s.LastCheckedAt = now

		if count == 0 {
			// Decommission when sessions reach zero and minimum drain time has elapsed.
			if now.Sub(s.EnteredAt) >= d.timeout {
				s.Phase = PhaseDecommission
				log.Printf("[OK] [drainer] %s → %s (all sessions ended)", s.Name, PhaseDecommission)
				return true, nil
			}
		}

		log.Printf("[OK] [drainer] %s → %s (active sessions: %d)", s.Name, PhaseDrain, count)
		return false, nil

	case PhaseDecommission, PhaseForced:
		s.Phase = PhaseDone
		log.Printf("[OK] [drainer] %s → %s (pod reclaimed)", s.Name, PhaseDone)
		return true, nil

	default:
		return false, nil
	}
}

// ShouldCheck returns true when it's time to poll the server for session count.
func (d *Drainer) ShouldCheck(s *ServerState) bool {
	if s.Phase != PhaseDrain {
		return false
	}
	return time.Since(s.LastCheckedAt) >= d.interval
}
