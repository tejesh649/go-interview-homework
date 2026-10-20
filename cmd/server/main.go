package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/graphql-go/graphql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const defaultDSN = "postgres://admin:todo@localhost:5432/homework?sslmode=disable"

type User struct {
	ID          int64
	Email, Name string
}
type Task struct {
	ID, UserID  int64
	Title       string
	Description sql.NullString
	Status      string
	DueDate     sql.NullTime
}

type server struct {
	db     *sql.DB
	schema graphql.Schema
}

func parseID(value any) (int64, error) {
	id, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("ID must be a positive integer")
	}
	return id, nil
}
func statusToDB(value any) string {
	switch value {
	case "PENDING":
		return "pending"
	case "IN_PROGRESS":
		return "in_progress"
	case "DONE":
		return "done"
	}
	return ""
}
func statusToGraphQL(value string) string {
	switch value {
	case "pending":
		return "PENDING"
	case "in_progress":
		return "IN_PROGRESS"
	case "done":
		return "DONE"
	}
	return value
}
func (s *server) getUser(ctx context.Context, id int64) (*User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `SELECT id, email, name FROM users WHERE id=$1`, id).Scan(&u.ID, &u.Email, &u.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}
func (s *server) listUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, email, name FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []*User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Name); err != nil {
			return nil, err
		}
		users = append(users, &u)
	}
	return users, rows.Err()
}

const taskColumns = `id, user_id, title, description, status::text, due_date`

func scanTask(row interface{ Scan(...any) error }) (*Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.UserID, &t.Title, &t.Description, &t.Status, &t.DueDate)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}
func (s *server) getTask(ctx context.Context, id int64) (*Task, error) {
	return scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id=$1`, id))
}
func (s *server) listTasks(ctx context.Context, userID *int64, status string) ([]*Task, error) {
	query := `SELECT ` + taskColumns + ` FROM tasks WHERE 1=1`
	args := []any{}
	if userID != nil {
		args = append(args, *userID)
		query += fmt.Sprintf(" AND user_id=$%d", len(args))
	}
	if status != "" {
		args = append(args, status)
		query += fmt.Sprintf(" AND status=$%d", len(args))
	}
	query += ` ORDER BY id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []*Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}
func (s *server) tags(ctx context.Context, id int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tag FROM task_tags WHERE task_id=$1 ORDER BY tag`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := []string{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}
func (s *server) createTask(ctx context.Context, userID int64, title string, description, dueDate *string, tags []string) (*Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var descriptionValue, dueValue any
	if description != nil {
		descriptionValue = *description
	}
	if dueDate != nil {
		if _, err := time.Parse("2006-01-02", *dueDate); err != nil {
			return nil, errors.New("dueDate must use YYYY-MM-DD")
		}
		dueValue = *dueDate
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO tasks (user_id,title,description,due_date) VALUES ($1,$2,$3,$4) RETURNING id`, userID, title, descriptionValue, dueValue).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}
	for _, tag := range tags {
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_tags (task_id,tag) VALUES ($1,$2) ON CONFLICT DO NOTHING`, id, tag); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getTask(ctx, id)
}
func (s *server) updateStatus(ctx context.Context, id int64, status string) (*Task, error) {
	row := s.db.QueryRowContext(ctx, `UPDATE tasks SET status=$1, updated_at=now() WHERE id=$2 RETURNING `+taskColumns, status, id)
	task, err := scanTask(row)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, errors.New("task not found")
	}
	return task, nil
}
func stringPtr(args map[string]any, key string) *string {
	v, ok := args[key].(string)
	if !ok {
		return nil
	}
	return &v
}
func (s *server) initSchema() error {
	statusEnum := graphql.NewEnum(graphql.EnumConfig{Name: "TaskStatus", Values: graphql.EnumValueConfigMap{
		"PENDING": &graphql.EnumValueConfig{Value: "PENDING"}, "IN_PROGRESS": &graphql.EnumValueConfig{Value: "IN_PROGRESS"}, "DONE": &graphql.EnumValueConfig{Value: "DONE"},
	}})
	var userType, taskType *graphql.Object
	userType = graphql.NewObject(graphql.ObjectConfig{Name: "User", Fields: graphql.FieldsThunk(func() graphql.Fields {
		return graphql.Fields{
			"id":    &graphql.Field{Type: graphql.NewNonNull(graphql.ID), Resolve: func(p graphql.ResolveParams) (any, error) { return strconv.FormatInt(p.Source.(*User).ID, 10), nil }},
			"email": &graphql.Field{Type: graphql.NewNonNull(graphql.String), Resolve: func(p graphql.ResolveParams) (any, error) { return p.Source.(*User).Email, nil }},
			"name":  &graphql.Field{Type: graphql.NewNonNull(graphql.String), Resolve: func(p graphql.ResolveParams) (any, error) { return p.Source.(*User).Name, nil }},
			"tasks": &graphql.Field{Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(taskType))), Args: graphql.FieldConfigArgument{"status": &graphql.ArgumentConfig{Type: statusEnum}}, Resolve: func(p graphql.ResolveParams) (any, error) {
				id := p.Source.(*User).ID
				return s.listTasks(p.Context, &id, statusToDB(p.Args["status"]))
			}},
		}
	})})
	taskType = graphql.NewObject(graphql.ObjectConfig{Name: "Task", Fields: graphql.FieldsThunk(func() graphql.Fields {
		return graphql.Fields{
			"id":    &graphql.Field{Type: graphql.NewNonNull(graphql.ID), Resolve: func(p graphql.ResolveParams) (any, error) { return strconv.FormatInt(p.Source.(*Task).ID, 10), nil }},
			"user":  &graphql.Field{Type: graphql.NewNonNull(userType), Resolve: func(p graphql.ResolveParams) (any, error) { return s.getUser(p.Context, p.Source.(*Task).UserID) }},
			"title": &graphql.Field{Type: graphql.NewNonNull(graphql.String), Resolve: func(p graphql.ResolveParams) (any, error) { return p.Source.(*Task).Title, nil }},
			"description": &graphql.Field{Type: graphql.String, Resolve: func(p graphql.ResolveParams) (any, error) {
				v := p.Source.(*Task).Description
				if !v.Valid {
					return nil, nil
				}
				return v.String, nil
			}},
			"status": &graphql.Field{Type: graphql.NewNonNull(statusEnum), Resolve: func(p graphql.ResolveParams) (any, error) { return statusToGraphQL(p.Source.(*Task).Status), nil }},
			"dueDate": &graphql.Field{Type: graphql.String, Resolve: func(p graphql.ResolveParams) (any, error) {
				v := p.Source.(*Task).DueDate
				if !v.Valid {
					return nil, nil
				}
				return v.Time.Format("2006-01-02"), nil
			}},
			"tags": &graphql.Field{Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(graphql.String))), Resolve: func(p graphql.ResolveParams) (any, error) { return s.tags(p.Context, p.Source.(*Task).ID) }},
		}
	})})
	idArg := &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.ID)}
	query := graphql.NewObject(graphql.ObjectConfig{Name: "Query", Fields: graphql.Fields{
		"user": &graphql.Field{Type: userType, Args: graphql.FieldConfigArgument{"id": idArg}, Resolve: func(p graphql.ResolveParams) (any, error) {
			id, err := parseID(p.Args["id"])
			if err != nil {
				return nil, err
			}
			return s.getUser(p.Context, id)
		}},
		"users": &graphql.Field{Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(userType))), Resolve: func(p graphql.ResolveParams) (any, error) { return s.listUsers(p.Context) }},
		"task": &graphql.Field{Type: taskType, Args: graphql.FieldConfigArgument{"id": idArg}, Resolve: func(p graphql.ResolveParams) (any, error) {
			id, err := parseID(p.Args["id"])
			if err != nil {
				return nil, err
			}
			return s.getTask(p.Context, id)
		}},
		"tasks": &graphql.Field{Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(taskType))), Args: graphql.FieldConfigArgument{"status": &graphql.ArgumentConfig{Type: statusEnum}, "userId": &graphql.ArgumentConfig{Type: graphql.ID}}, Resolve: func(p graphql.ResolveParams) (any, error) {
			var id *int64
			if raw, ok := p.Args["userId"]; ok {
				parsed, err := parseID(raw)
				if err != nil {
					return nil, err
				}
				id = &parsed
			}
			return s.listTasks(p.Context, id, statusToDB(p.Args["status"]))
		}},
	}})
	mutation := graphql.NewObject(graphql.ObjectConfig{Name: "Mutation", Fields: graphql.Fields{
		"createTask": &graphql.Field{Type: graphql.NewNonNull(taskType), Args: graphql.FieldConfigArgument{
			"userId": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.ID)}, "title": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)}, "description": &graphql.ArgumentConfig{Type: graphql.String}, "dueDate": &graphql.ArgumentConfig{Type: graphql.String}, "tags": &graphql.ArgumentConfig{Type: graphql.NewList(graphql.NewNonNull(graphql.String))},
		}, Resolve: func(p graphql.ResolveParams) (any, error) {
			id, err := parseID(p.Args["userId"])
			if err != nil {
				return nil, err
			}
			title := p.Args["title"].(string)
			if title == "" {
				return nil, errors.New("title cannot be empty")
			}
			tags := []string{}
			if values, ok := p.Args["tags"].([]any); ok {
				for _, v := range values {
					tags = append(tags, v.(string))
				}
			}
			return s.createTask(p.Context, id, title, stringPtr(p.Args, "description"), stringPtr(p.Args, "dueDate"), tags)
		}},
		"updateTaskStatus": &graphql.Field{Type: graphql.NewNonNull(taskType), Args: graphql.FieldConfigArgument{"id": idArg, "status": &graphql.ArgumentConfig{Type: graphql.NewNonNull(statusEnum)}}, Resolve: func(p graphql.ResolveParams) (any, error) {
			id, err := parseID(p.Args["id"])
			if err != nil {
				return nil, err
			}
			return s.updateStatus(p.Context, id, statusToDB(p.Args["status"]))
		}},
	}})
	schema, err := graphql.NewSchema(graphql.SchemaConfig{Query: query, Mutation: mutation})
	if err != nil {
		return err
	}
	s.schema = schema
	return nil
}
func (s *server) handleGraphQL(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		Query         string         `json:"query"`
		Variables     map[string]any `json:"variables"`
		OperationName string         `json:"operationName"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if request.Query == "" {
		http.Error(w, "query is required", http.StatusBadRequest)
		return
	}
	result := graphql.Do(graphql.Params{Schema: s.schema, RequestString: request.Query, VariableValues: request.Variables, OperationName: request.OperationName, Context: r.Context()})
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		log.Printf("write GraphQL response: %v", err)
	}
}
func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = defaultDSN
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("database connection: %v", err)
	}
	s := &server{db: db}
	if err := s.initSchema(); err != nil {
		log.Fatalf("GraphQL schema: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", s.handleGraphQL)
	log.Println("GraphQL listening on http://localhost:8080/graphql")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
