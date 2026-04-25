package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const startupLogRetention = 24 * time.Hour

type startupLogSession struct {
	file        *os.File
	currentPath string
	outputPath  string
	prefix      string
	ext         string
	stopCleanup chan struct{}
}

func newStartupLogSession(outputPath string) (*startupLogSession, error) {
	dir := filepath.Dir(outputPath)
	base := filepath.Base(outputPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	if name == "" {
		name = "V2bX"
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create log dir failed: %w", err)
	}

	s := &startupLogSession{
		outputPath: outputPath,
		prefix:     name + ".",
		ext:        ext,
		stopCleanup: make(chan struct{}),
	}
	if err := s.cleanupOlderThan(startupLogRetention); err != nil {
		log.WithField("err", err).Warn("Cleanup old startup logs failed")
	}

	ts := time.Now().Format("20060102-150405")
	fileName := fmt.Sprintf("%s%s%s", s.prefix, ts, s.ext)
	s.currentPath = filepath.Join(dir, fileName)

	f, err := os.OpenFile(s.currentPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open startup log file failed: %w", err)
	}
	s.file = f
	log.SetOutput(f)
	s.startAutoCleanup()

	return s, nil
}

func (s *startupLogSession) MarkStartupSuccess() {
	if err := s.cleanupOlderThan(startupLogRetention); err != nil {
		log.WithField("err", err).Warn("Cleanup old startup logs failed")
	}
	if err := s.keepCurrentOnly(); err != nil {
		log.WithField("err", err).Warn("Keep current startup log failed")
	}
}

func (s *startupLogSession) keepCurrentOnly() error {
	entries, err := os.ReadDir(filepath.Dir(s.currentPath))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !s.isStartupLogFile(name) {
			continue
		}
		fullPath := filepath.Join(filepath.Dir(s.currentPath), name)
		if fullPath == s.currentPath {
			continue
		}
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *startupLogSession) cleanupOlderThan(maxAge time.Duration) error {
	cutoff := time.Now().Add(-maxAge)
	entries, err := os.ReadDir(filepath.Dir(s.outputPath))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !s.isStartupLogFile(name) {
			continue
		}
		fullPath := filepath.Join(filepath.Dir(s.outputPath), name)
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(fullPath)
		}
	}
	return nil
}

func (s *startupLogSession) isStartupLogFile(name string) bool {
	return strings.HasPrefix(name, s.prefix) && strings.HasSuffix(name, s.ext)
}

func (s *startupLogSession) Close() error {
	if s.stopCleanup != nil {
		close(s.stopCleanup)
		s.stopCleanup = nil
	}
	if s.file == nil {
		return nil
	}
	return s.file.Close()
}

func (s *startupLogSession) startAutoCleanup() {
	// Long-running service still needs 24h retention enforcement.
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := s.cleanupOlderThan(startupLogRetention); err != nil {
					log.WithField("err", err).Warn("Periodic startup log cleanup failed")
				}
			case <-s.stopCleanup:
				return
			}
		}
	}()
}
