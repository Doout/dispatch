package store

import "time"

// ConfigureTenantPool bounds the connections held by each hosted runtime. The
// single-tenant controller keeps its existing connection policy.
func (s *SQLStore) ConfigureTenantPool() {
	if !s.postgres {
		return
	}
	s.db.SetMaxOpenConns(4)
	s.db.SetMaxIdleConns(1)
	s.db.SetConnMaxIdleTime(time.Minute)
	s.db.SetConnMaxLifetime(15 * time.Minute)
}
