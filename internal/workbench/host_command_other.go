//go:build !windows && !darwin

package workbench

import "context"

func (s *Server) startHostCommandMonitor(context.Context) {}
