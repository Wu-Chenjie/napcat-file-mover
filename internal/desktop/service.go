package desktop

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"

	"napcat-file-mover/internal/app"
	"napcat-file-mover/internal/config"
)

type Service struct {
	cfg  *config.Config
	core *app.Core
	ctx  context.Context
	stop context.CancelFunc
}

func NewService() (*Service, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, err
	}
	return &Service{cfg: cfg}, nil
}

func (s *Service) Start() error {
	if s.core != nil {
		return nil
	}
	core, err := app.New(s.cfg)
	if err != nil {
		return err
	}
	s.ctx, s.stop = context.WithCancel(context.Background())
	if err := core.Start(s.ctx); err != nil {
		return err
	}
	s.core = core
	return nil
}

func (s *Service) Stop() {
	if s.stop != nil {
		s.stop()
	}
	if s.core != nil {
		s.core.Stop(context.Background())
		s.core = nil
	}
}

func (s *Service) Status() map[string]any {
	return map[string]any{
		"running": s.core != nil,
		"listen":  s.cfg.Server.Listen,
		"config":  s.cfg.Paths.Config,
		"logs":    s.cfg.Paths.LogDir,
	}
}

func (s *Service) OpenLogDir() error { return openPath(s.cfg.Paths.LogDir) }

func (s *Service) OpenConfigDir() error { return openPath(s.cfg.Paths.BaseDir) }

func openPath(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Start()
	case "windows":
		return exec.Command("explorer", path).Start()
	default:
		return fmt.Errorf("unsupported desktop open on %s", runtime.GOOS)
	}
}
