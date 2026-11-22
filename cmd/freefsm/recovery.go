package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/hostrecovery"
)

// Called after interrupted-restore recovery, while holding OperationLock, before
// migrations, HTTP admission, or workers. Passwords never appear in argv or logs.
func recoverAdminPassword(cfg *config.Config, email string) (result error) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return errors.New("cannot read password input")
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return errors.New("pipe two matching password lines on stdin from a restricted file or non-echoing prompt")
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 1025))
	if err != nil || len(input) > 1024 {
		return errors.New("invalid password input; supply two matching lines")
	}
	lines := strings.Split(strings.TrimSuffix(string(input), "\n"), "\n")
	if len(lines) != 2 || lines[0] == "" || lines[0] != lines[1] {
		return errors.New("supply two matching nonempty password lines on stdin")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Connect(ctx, cfg.DSN())
	if err != nil {
		return errors.New("cannot connect for Admin recovery; check database configuration")
	}
	defer db.Close()
	if err := db.CheckCompatibility(ctx); err != nil {
		return err
	}
	// The startup owner retains exclusive maintenance through this transaction
	// and verifies the installed instance before publishing admission afterward.
	if err := hostrecovery.ResetExistingAdmin(ctx, db.Pool, email, lines[0], strconv.Itoa(os.Getuid())); err != nil {
		return err
	}
	return nil
}
