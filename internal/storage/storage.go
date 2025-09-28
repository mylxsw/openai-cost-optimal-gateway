package storage

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type UsageRecord struct {
	ID                int64         `json:"id"`
	CreatedAt         time.Time     `json:"created_at"`
	Path              string        `json:"path"`
	Provider          string        `json:"provider"`
	Model             string        `json:"model"`
	OriginalModel     string        `json:"original_model"`
	ProviderRequestID string        `json:"provider_request_id"`
	RequestID         string        `json:"request_id"`
	Attempt           int           `json:"attempt"`
	RequestTokens     int           `json:"request_tokens"`
	ResponseTokens    int           `json:"response_tokens"`
	StatusCode        int           `json:"status_code"`
	Outcome           string        `json:"status"`
	Duration          time.Duration `json:"duration"`
	FirstTokenLatency time.Duration `json:"first_token_latency"`
	Error             string        `json:"error,omitempty"`
}

type UsageQuery struct {
	Limit     int
	RequestID string
}

type Store interface {
	RecordUsage(ctx context.Context, record UsageRecord) error
	QueryUsage(ctx context.Context, query UsageQuery) ([]UsageRecord, error)
	CleanupOldRecords(ctx context.Context, retentionDays int) (int64, error)
	Close(ctx context.Context) error
}

type sqliteStore struct {
	db      *sql.DB
	path    string
	pragmas []string
}

type fileStore struct {
	mu      sync.RWMutex
	path    string
	records []UsageRecord
	nextID  int64
}

func New(ctx context.Context, driver, uri string) (Store, error) {
	driver = normalizeDriver(driver)
	if driver == "" {
		return nil, errors.New("storage driver is required")
	}
	if strings.TrimSpace(uri) == "" {
		return nil, errors.New("storage uri is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	switch driver {
	case "sqlite":
		store, err := newSQLiteStore(ctx, uri)
		if err != nil {
			return nil, err
		}
		return store, nil
	case "mysql":
		path, err := parseMySQLURI(uri)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create storage directory: %w", err)
		}
		fs := &fileStore{path: path}
		if err := fs.load(); err != nil {
			return nil, err
		}
		return fs, nil
	default:
		return nil, fmt.Errorf("unsupported storage driver %s", driver)
	}
}

func newSQLiteStore(ctx context.Context, uri string) (*sqliteStore, error) {
	path, pragmas, err := parseSQLiteURI(uri)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create sqlite directory: %w", err)
	}

	// Build connection string with pragmas
	connStr := path
	if len(pragmas) > 0 {
		params := make([]string, len(pragmas))
		for i, pragma := range pragmas {
			params[i] = "_pragma=" + pragma
		}
		connStr += "?" + strings.Join(params, "&")
	}

	db, err := sql.Open("sqlite3", connStr)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	store := &sqliteStore{db: db, path: path, pragmas: pragmas}
	if err := store.initSchema(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *sqliteStore) RecordUsage(ctx context.Context, record UsageRecord) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now()
	}
	if record.Attempt <= 0 {
		record.Attempt = 1
	}

	query := `INSERT INTO usage_records 
		(created_at, path, provider, model, original_model, provider_request_id, request_id, attempt, request_tokens, response_tokens, status, outcome, error, duration, first_token_latency) 
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := s.db.ExecContext(ctx, query,
		record.CreatedAt.Format(time.RFC3339Nano),
		record.Path,
		record.Provider,
		record.Model,
		record.OriginalModel,
		record.ProviderRequestID,
		record.RequestID,
		record.Attempt,
		record.RequestTokens,
		record.ResponseTokens,
		record.StatusCode,
		record.Outcome,
		record.Error,
		record.Duration.Nanoseconds(),
		record.FirstTokenLatency.Nanoseconds(),
	)

	if err != nil {
		return fmt.Errorf("insert usage record: %w", err)
	}
	return nil
}

func (s *sqliteStore) QueryUsage(ctx context.Context, query UsageQuery) ([]UsageRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}

	querySQL := `SELECT id, created_at, path, provider, model, original_model, provider_request_id, request_id, attempt, request_tokens, response_tokens, status, outcome, error, duration, first_token_latency 
		FROM usage_records`
	args := []interface{}{}

	if strings.TrimSpace(query.RequestID) != "" {
		querySQL += " WHERE request_id = ?"
		args = append(args, query.RequestID)
	}

	querySQL += " ORDER BY datetime(created_at) DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("query usage records: %w", err)
	}
	defer rows.Close()

	var records []UsageRecord
	for rows.Next() {
		var record UsageRecord
		var createdAtStr string
		var durationNs, firstTokenLatencyNs int64

		err := rows.Scan(
			&record.ID,
			&createdAtStr,
			&record.Path,
			&record.Provider,
			&record.Model,
			&record.OriginalModel,
			&record.ProviderRequestID,
			&record.RequestID,
			&record.Attempt,
			&record.RequestTokens,
			&record.ResponseTokens,
			&record.StatusCode,
			&record.Outcome,
			&record.Error,
			&durationNs,
			&firstTokenLatencyNs,
		)
		if err != nil {
			return nil, fmt.Errorf("scan usage record: %w", err)
		}

		// Parse created_at
		if createdAt, err := time.Parse(time.RFC3339Nano, createdAtStr); err == nil {
			record.CreatedAt = createdAt
		}

		// Convert nanoseconds to Duration
		record.Duration = time.Duration(durationNs)
		record.FirstTokenLatency = time.Duration(firstTokenLatencyNs)

		// Set default values
		if record.Attempt <= 0 {
			record.Attempt = 1
		}
		if record.Outcome == "" {
			if record.StatusCode >= 200 && record.StatusCode < 400 {
				record.Outcome = "success"
			} else if record.StatusCode != 0 {
				record.Outcome = "failure"
			}
		}

		records = append(records, record)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage records: %w", err)
	}

	return records, nil
}

func (s *sqliteStore) CleanupOldRecords(ctx context.Context, retentionDays int) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Calculate the cutoff time
	cutoffTime := time.Now().AddDate(0, 0, -retentionDays)

	// Delete records older than the cutoff time
	query := `DELETE FROM usage_records WHERE datetime(created_at) < datetime(?)`
	result, err := s.db.ExecContext(ctx, query, cutoffTime.Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("cleanup old records: %w", err)
	}

	// Get the number of affected rows
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("get rows affected: %w", err)
	}

	return rowsAffected, nil
}

func (s *sqliteStore) Close(ctx context.Context) error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *sqliteStore) initSchema(ctx context.Context) error {
	// Create main table
	createTableSQL := `CREATE TABLE IF NOT EXISTS usage_records (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        created_at TEXT NOT NULL,
        path TEXT,
        provider TEXT,
        model TEXT,
        original_model TEXT,
        provider_request_id TEXT,
        request_id TEXT,
        attempt INTEGER NOT NULL DEFAULT 1,
        request_tokens INTEGER NOT NULL DEFAULT 0,
        response_tokens INTEGER NOT NULL DEFAULT 0,
        status INTEGER NOT NULL DEFAULT 0,
        outcome TEXT,
        error TEXT,
        duration INTEGER NOT NULL DEFAULT 0,
        first_token_latency INTEGER NOT NULL DEFAULT 0
    )`

	if _, err := s.db.ExecContext(ctx, createTableSQL); err != nil {
		return fmt.Errorf("create usage_records table: %w", err)
	}

	// Create index
	createIndexSQL := `CREATE INDEX IF NOT EXISTS idx_usage_records_created_at ON usage_records (created_at DESC)`
	if _, err := s.db.ExecContext(ctx, createIndexSQL); err != nil {
		return fmt.Errorf("create usage_records index: %w", err)
	}

	// Try to add columns that might not exist in older schemas
	alterStatements := []string{
		"ALTER TABLE usage_records ADD COLUMN original_model TEXT",
		"ALTER TABLE usage_records ADD COLUMN provider_request_id TEXT",
		"ALTER TABLE usage_records ADD COLUMN request_id TEXT",
		"ALTER TABLE usage_records ADD COLUMN attempt INTEGER NOT NULL DEFAULT 1",
		"ALTER TABLE usage_records ADD COLUMN outcome TEXT",
		"ALTER TABLE usage_records ADD COLUMN error TEXT",
		"ALTER TABLE usage_records ADD COLUMN first_token_latency INTEGER NOT NULL DEFAULT 0",
	}

	for _, stmt := range alterStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			errText := strings.ToLower(err.Error())
			if strings.Contains(errText, "duplicate column name") {
				continue
			}
			return fmt.Errorf("alter usage_records: %w", err)
		}
	}

	return nil
}

func parseSQLiteURI(uri string) (string, []string, error) {
	trimmed := strings.TrimSpace(uri)
	if trimmed == "" {
		return "", nil, errors.New("sqlite uri is empty")
	}
	if trimmed == ":memory:" {
		return "", nil, errors.New(":memory: sqlite databases are not supported")
	}

	var path string
	pragmas := make([]string, 0)

	if strings.HasPrefix(trimmed, "file:") {
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return "", nil, fmt.Errorf("parse sqlite uri: %w", err)
		}
		if parsed.Path != "" {
			path = parsed.Path
		} else {
			path = parsed.Opaque
		}

		path = strings.TrimPrefix(path, "//")
		for key, values := range parsed.Query() {
			if strings.EqualFold(key, "_pragma") {
				for _, value := range values {
					if value != "" {
						pragmas = append(pragmas, value)
					}
				}
			}
		}
	} else {
		rawPath := trimmed
		if idx := strings.Index(rawPath, "?"); idx >= 0 {
			queryValues, err := url.ParseQuery(rawPath[idx+1:])
			if err != nil {
				return "", nil, fmt.Errorf("parse sqlite uri query: %w", err)
			}
			for key, values := range queryValues {
				if strings.EqualFold(key, "_pragma") {
					for _, value := range values {
						if value != "" {
							pragmas = append(pragmas, value)
						}
					}
				}
			}
			rawPath = rawPath[:idx]
		}
		path = rawPath
	}

	if path == "" {
		return "", nil, errors.New("sqlite uri missing path")
	}
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err == nil {
			path = abs
		}
	}
	return path, pragmas, nil
}

func parseMySQLURI(uri string) (string, error) {
	trimmed := strings.TrimSpace(uri)
	if trimmed == "" {
		return "", errors.New("mysql uri is empty")
	}

	base := trimmed
	if idx := strings.Index(base, "?"); idx >= 0 {
		base = base[:idx]
	}
	if strings.Contains(base, "://") {
		parts := strings.SplitN(base, "://", 2)
		if len(parts) == 2 {
			base = parts[1]
		}
	}
	slash := strings.LastIndex(base, "/")
	if slash == -1 || slash == len(base)-1 {
		return "", errors.New("mysql uri missing database name")
	}
	dbName := base[slash+1:]
	host := "default"
	at := strings.LastIndex(base[:slash], "@")
	if at >= 0 {
		hostPart := base[at+1 : slash]
		hostPart = strings.Trim(hostPart, "()")
		if hostPart != "" {
			host = hostPart
		}
	}
	sanitized := sanitizeFilename(fmt.Sprintf("%s_%s.json", host, dbName))
	return filepath.Join("data", "gateway-mysql", sanitized), nil
}

func (f *fileStore) RecordUsage(_ context.Context, record UsageRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if record.ID == 0 {
		f.nextID++
		record.ID = f.nextID
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now()
	}

	f.records = append(f.records, record)

	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode usage record: %w", err)
	}

	file, err := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open usage file: %w", err)
	}
	defer file.Close()

	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write usage record: %w", err)
	}
	return nil
}

func (f *fileStore) QueryUsage(_ context.Context, query UsageQuery) ([]UsageRecord, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}

	records := make([]UsageRecord, 0, len(f.records))
	requestID := strings.TrimSpace(query.RequestID)
	for _, rec := range f.records {
		if requestID != "" && rec.RequestID != requestID {
			continue
		}
		records = append(records, rec)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func (f *fileStore) CleanupOldRecords(ctx context.Context, retentionDays int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Calculate the cutoff time
	cutoffTime := time.Now().AddDate(0, 0, -retentionDays)

	// Filter records to keep only those within retention period
	var keptRecords []UsageRecord
	var removedCount int64

	for _, record := range f.records {
		if record.CreatedAt.After(cutoffTime) {
			keptRecords = append(keptRecords, record)
		} else {
			removedCount++
		}
	}

	f.records = keptRecords

	// Save the updated records to file by rewriting the entire file
	file, err := os.OpenFile(f.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open usage file for cleanup: %w", err)
	}
	defer file.Close()

	for _, record := range f.records {
		data, err := json.Marshal(record)
		if err != nil {
			return 0, fmt.Errorf("encode usage record during cleanup: %w", err)
		}
		if _, err := file.Write(append(data, '\n')); err != nil {
			return 0, fmt.Errorf("write usage record during cleanup: %w", err)
		}
	}

	return removedCount, nil
}

func (f *fileStore) Close(ctx context.Context) error {
	return nil
}

func (f *fileStore) load() error {
	file, err := os.OpenFile(f.path, os.O_RDONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("open usage store: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record UsageRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return fmt.Errorf("decode usage record: %w", err)
		}
		if record.Attempt <= 0 {
			record.Attempt = 1
		}
		if record.Outcome == "" {
			if record.StatusCode >= 200 && record.StatusCode < 400 {
				record.Outcome = "success"
			} else if record.StatusCode != 0 {
				record.Outcome = "failure"
			}
		}
		f.records = append(f.records, record)
		if record.ID > f.nextID {
			f.nextID = record.ID
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read usage records: %w", err)
	}
	return nil
}

func sanitizeFilename(name string) string {
	builder := strings.Builder{}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	return builder.String()
}

func normalizeDriver(driver string) string {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "sqlite", "sqlite3":
		return "sqlite"
	case "mysql":
		return "mysql"
	default:
		return driver
	}
}
