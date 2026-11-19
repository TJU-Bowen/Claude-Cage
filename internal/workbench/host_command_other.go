//go:build !windows

package workbench

import "context"

func (s *Server) startHostCommandMonitor(context.Context) {}
