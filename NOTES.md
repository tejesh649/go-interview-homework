# Implementation Notes

## Part 1 — Seed Script

The seed script contained an invalid PostgreSQL INSERT statement. Column names were wrapped in MySQL-style backticks, which PostgreSQL does not support.

I corrected the SQL statement to use standard PostgreSQL identifiers while preserving parameterized queries.

I verified that the seed script runs successfully multiple times without accumulating duplicate records.

## Part 2 — GraphQL API

I implemented the GraphQL API in Go using the graphql-go/graphql library and pgx for PostgreSQL access.

The API supports querying users and tasks, filtering tasks, creating tasks, and updating task statuses.

Resolvers read and write data directly from PostgreSQL. Task relationships and tags are retrieved from the database rather than hardcoded.

I kept the implementation lightweight to fit the scope of the exercise.

## Part 3 — Frontend

I selected the task description field.

I added description to the GraphQL query in web/app.js and updated renderTask() to display it below each task title.

Descriptions are rendered using textContent, and null descriptions are omitted.

I added a small CSS rule to maintain the existing visual design.

## Tradeoffs and Improvements

- Kept the API implementation simple rather than introducing additional architectural layers.
- Used a static JavaScript frontend without a build system.
- For production, I would add more automated tests, structured logging, configuration validation, and database connection tuning.
- I would also improve transaction handling in the seed script so database resets and inserts are atomic.

## How to Run

Requirements: Go 1.25+, Docker, and Docker Compose.

1. Start PostgreSQL:

   docker compose up -d

2. Set the database connection string if PostgreSQL is mapped to a non-default port.

   Example:

   export DATABASE_URL="postgres://admin:todo@localhost:5433/homework?sslmode=disable"

3. Seed the database:

   go run ./cmd/seed

4. Start the GraphQL server:

   go run ./cmd/server

5. Start the static frontend in a separate terminal:

   python3 -m http.server 8090 --directory web

6. Open http://localhost:8090 in a browser.

GraphQL endpoint: http://localhost:8080/graphql

Reviewed by: ST
