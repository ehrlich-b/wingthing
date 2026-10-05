package store

import "time"

// CountConversationLaunchesSince counts an owner's linked launches reserved at
// or after since, excluding launches recorded as definitely failed. It gives
// host mailbox admission a spawn-rate bound that survives process restarts and
// is shared by every process using this state directory.
func (s *Store) CountConversationLaunchesSince(owner string, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM conversations WHERE owner_id = ? AND datetime(created_at) >= datetime(?) AND launch_state != 'failed'`, owner, since.UTC().Format("2006-01-02 15:04:05")).Scan(&count)
	return count, err
}
