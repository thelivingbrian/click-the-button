package main

import (
	"database/sql"
	"encoding/json"
	"errors"
)

func saveEventChange(tx *sql.Tx, p *Poll, now int64) error {
	p.Version++
	p.Updated = now
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE polls SET state=? WHERE id=? AND status='live'", raw, p.ID); err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO history(poll,version,state) VALUES(?,?,?)", p.ID, p.Version, raw)
	return err
}

func nominateEvent(tx *sql.Tx, candidate Poll, now int64) error {
	var raw []byte
	if err := tx.QueryRow("SELECT p.state FROM polls p JOIN event_schedule e ON e.next=p.id").Scan(&raw); err != nil {
		return err
	}
	var ballot Poll
	if err := json.Unmarshal(raw, &ballot); err != nil {
		return err
	}
	if candidate.Scope != "community" || candidate.Status != "live" || candidate.Ends <= ballot.Ends || now >= ballot.Ends {
		return errors.New("A nominee must stay open beyond the current main event's deadline")
	}
	hidden, err := hiddenEvent(tx, candidate.ID)
	if err != nil {
		return err
	}
	if hidden {
		return errors.New("Restore this event before nominating it")
	}
	found := false
	for i := range ballot.Candidates {
		if ballot.Candidates[i].ID == candidate.ID {
			found = true
			break
		}
	}
	if !found {
		ballot.Candidates = append(ballot.Candidates, candidate)
		ballot.Options = append(ballot.Options, candidate.Title)
		ballot.Counts = append(ballot.Counts, 0)
		ballot.Energy = append(ballot.Energy, 0)
	}
	if _, err = tx.Exec("DELETE FROM withdrawn_candidates WHERE ballot=? AND candidate=?", ballot.ID, candidate.ID); err != nil {
		return err
	}
	return saveEventChange(tx, &ballot, now)
}

func recordGuestParticipation(tx *sql.Tx, session Session) error {
	if session.AccountID != "" {
		return nil
	}
	_, err := tx.Exec("INSERT OR IGNORE INTO participation_prompts(session) VALUES(?)", session.ID)
	return err
}

func (s *Station) showParticipationPrompt(session Session) (bool, error) {
	if session.AccountID != "" || session.ID == "" {
		return false, nil
	}
	var show bool
	err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM participation_prompts WHERE session=? AND dismissed=0)", session.ID).Scan(&show)
	return show, err
}

func (s *Station) nominationState(p Poll) (nominated, eligible bool, err error) {
	if p.Scope != "community" || p.Status != "live" {
		return false, false, nil
	}
	var mainEnds int64
	if err = s.db.QueryRow("SELECT ends FROM event_schedule WHERE id=1").Scan(&mainEnds); err != nil {
		return false, false, err
	}
	var hidden bool
	err = s.db.QueryRow("SELECT COALESCE(hidden,0),COALESCE(nominated,0) FROM moderation_events WHERE poll=?", p.ID).Scan(&hidden, &nominated)
	if err == sql.ErrNoRows {
		err = nil
	} else if err != nil {
		return false, false, err
	}
	return nominated, p.Ends > mainEnds && !hidden, nil
}
