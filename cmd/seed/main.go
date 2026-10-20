// Package main seeds the todo database with sample data.
//
// Usage:
//
//	go run ./cmd/seed
//
// Connection settings are read from DATABASE_URL, falling back to a sensible
// default that matches docker-compose.yml.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const defaultDSN = "postgres://admin:todo@localhost:5432/homework?sslmode=disable"

type seedUser struct {
	Email string
	Name  string
	Tasks []seedTask
}

type seedTask struct {
	Title       string
	Description string
	Status      string
	DueDate     *time.Time
	Tags        []string
}

func ptrDate(s string) *time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return &t
}

var seedData = []seedUser{
	{
		Email: "ada@ally.com",
		Name:  "Ada Lovelace",
		Tasks: []seedTask{
			{Title: "Draft analytical engine notes", Status: "in_progress", DueDate: ptrDate("2026-07-01"), Tags: []string{"writing", "math"}},
			{Title: "Review Babbage correspondence", Description: "Reply to last 3 letters", Status: "pending", Tags: []string{"writing"}},
		},
	},
	{
		Email: "alan@ally.com",
		Name:  "Alan Turing",
		Tasks: []seedTask{
			{Title: "Decrypt sample message", Status: "done", Tags: []string{"crypto"}},
			{Title: "Write paper on computable numbers", Status: "in_progress", DueDate: ptrDate("2026-07-15"), Tags: []string{"writing", "research"}},
			{Title: "Build new bombe prototype", Status: "pending", Tags: []string{"hardware"}},
		},
	},
	{
		Email: "grace@ally.com",
		Name:  "Grace Hopper",
		Tasks: []seedTask{
			{Title: "Find the moth in relay 70", Status: "done", Tags: []string{"debugging"}},
			{Title: "Draft COBOL spec", Status: "pending", DueDate: ptrDate("2026-08-01"), Tags: []string{"language", "writing"}},
		},
	},
}

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = defaultDSN
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("ping db: %v", err)
	}

	if err := reset(ctx, db); err != nil {
		log.Fatalf("reset: %v", err)
	}

	if err := seed(ctx, db); err != nil {
		log.Fatalf("seed: %v", err)
	}

	fmt.Println("seed complete")
}

func reset(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		"TRUNCATE task_tags, tasks, users RESTART IDENTITY CASCADE",
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("exec %q: %w", s, err)
		}
	}
	return nil
}

func seed(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, u := range seedData {
		var userID int64
		err := tx.QueryRowContext(ctx,
			`INSERT INTO users (email, name) VALUES ($1, $2) RETURNING id`,
			u.Email, u.Name,
		).Scan(&userID)
		if err != nil {
			return fmt.Errorf("insert user %s: %w", u.Email, err)
		}

		for _, t := range u.Tasks {
			var taskID int64
			err := tx.QueryRowContext(ctx,
				`INSERT INTO tasks (user_id, title, description, status, due_date) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
				userID, t.Title, nullableString(t.Description), t.Status, t.DueDate,
			).Scan(&taskID)
			if err != nil {
				return fmt.Errorf("insert task %q: %w", t.Title, err)
			}

			for _, tag := range t.Tags {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO task_tags (task_id, tag) VALUES ($1, $2)`,
					taskID, tag,
				); err != nil {
					return fmt.Errorf("insert tag %q on task %d: %w", tag, taskID, err)
				}
			}
		}
	}

	return tx.Commit()
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
