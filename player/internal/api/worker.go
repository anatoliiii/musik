package api

// EnsureWorker preserves the server lifecycle API while delegating process
// management to the application layer.
func (s *Server) EnsureWorker() {
	s.App.EnsureWorker()
}

// WatchJobs preserves the server lifecycle API while delegating polling to the
// application layer.
func (s *Server) WatchJobs() {
	// Server.Reload refreshes the shared catalog and every cached profile.
	s.App.WatchJobs(s.Reload)
}

func (s *Server) ensureWorkerBeforeEnqueue() {
	if !s.App.WorkerHealthy() {
		s.App.EnsureWorker()
	}
}
