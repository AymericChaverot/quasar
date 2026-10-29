package db

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// A read goes ahead while a write is in progress, rather than queueing behind
// it for the one connection there used to be.
func TestReadsDoNotWaitForAWrite(t *testing.T) {
	database := openTestDB(t)
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO settings (key, value) VALUES ('k', 'v')"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var n int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM settings WHERE key = 'k'").Scan(&n); err != nil {
		t.Fatalf("a read during a write: %v", err)
	}
	if n != 0 {
		t.Error("a read saw a write that has not been committed")
	}
}

// Writers still take turns: transactions started at once from many
// goroutines, each reading then writing, all land rather than one failing on
// a lock it found taken.
func TestConcurrentWritersAllLand(t *testing.T) {
	database := openTestDB(t)
	const writers = 16
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx, err := database.Begin()
			if err != nil {
				errs <- err
				return
			}
			defer tx.Rollback()
			var n int
			if err := tx.QueryRow("SELECT COUNT(*) FROM settings").Scan(&n); err != nil {
				errs <- err
				return
			}
			if _, err := tx.Exec("INSERT INTO settings (key, value) VALUES (?, ?)", fmt.Sprint("k", i), fmt.Sprint(n)); err != nil {
				errs <- err
				return
			}
			errs <- tx.Commit()
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("a writer failed: %v", err)
		}
	}
	var n int
	if err := database.QueryRow("SELECT COUNT(*) FROM settings WHERE key LIKE 'k%'").Scan(&n); err != nil || n != writers {
		t.Errorf("%d rows written, want %d (%v)", n, writers, err)
	}
}
