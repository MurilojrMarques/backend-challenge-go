package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/MurilojrMarques/backend-challenge-go/internal/config"
)

type Server struct {
	http            *http.Server
	logger          *slog.Logger
	addr            net.Addr
	shutdownTimeout time.Duration
}

func NewServer(cfg config.HTTP, handler http.Handler, logger *slog.Logger) *Server {
	return &Server{
		shutdownTimeout: cfg.ShutdownTimeout,
		http: &http.Server{
			Addr:              cfg.Addr,
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       cfg.ReadTimeout,
			WriteTimeout:      cfg.WriteTimeout,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    16 * 1024,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		},
		logger: logger,
	}
}

func (s *Server) Start(_ context.Context) error {
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return err
	}
	s.addr = ln.Addr()
	go func() {
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("http server stopped unexpectedly", "err", err)
		}
	}()
	s.logger.Info("http server listening", "addr", s.addr.String())
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	if s.shutdownTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.shutdownTimeout)
		defer cancel()
	}
	s.http.SetKeepAlivesEnabled(false)
	if err := s.http.Shutdown(ctx); err != nil {
		closeErr := s.http.Close()
		return errors.Join(err, closeErr)
	}
	s.logger.Info("http server stopped")
	return nil
}

func (s *Server) Addr() net.Addr {
	return s.addr
}
