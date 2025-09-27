package storage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type UsageRecord struct {
	ID             int64         `json:"id"`
	CreatedAt      time.Time     `json:"created_at"`
	Path           string        `json:"path"`
	Provider       string        `json:"provider"`
	Model          string        `json:"model"`
	RequestTokens  int           `json:"request_tokens"`
	ResponseTokens int           `json:"response_tokens"`
	Status         int           `json:"status"`
	Duration       time.Duration `json:"duration"`
}

type Store interface {
	RecordUsage(ctx context.Context, record UsageRecord) error
	QueryUsage(ctx context.Context, limit int) ([]UsageRecord, error)
	Close(ctx context.Context) error
}

type sqliteStore struct {
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
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, fmt.Errorf("sqlite3 binary not found: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create sqlite directory: %w", err)
	}

	store := &sqliteStore{path: path, pragmas: pragmas}
	if err := store.initSchema(ctx); err != nil {
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

	stmt := fmt.Sprintf(
		"INSERT INTO usage_records (created_at, path, provider, model, request_tokens, response_tokens, status, duration) VALUES (%s, %s, %s, %s, %d, %d, %d, %d)",
		quoteSQLiteTime(record.CreatedAt),
		quoteLiteral(record.Path),
		quoteLiteral(record.Provider),
		quoteLiteral(record.Model),
		record.RequestTokens,
		record.ResponseTokens,
		record.Status,
		record.Duration.Nanoseconds(),
	)

	if _, err := s.runSQLite(ctx, false, stmt); err != nil {
		return fmt.Errorf("insert usage record: %w", err)
	}
	return nil
}

func (s *sqliteStore) QueryUsage(ctx context.Context, limit int) ([]UsageRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = 100
	}

	stmt := fmt.Sprintf(
		"SELECT id, created_at, path, provider, model, request_tokens, response_tokens, status, duration FROM usage_records ORDER BY datetime(created_at) DESC LIMIT %d",
		limit,
	)
	out, err := s.runSQLite(ctx, true, stmt)
	if err != nil {
		return nil, fmt.Errorf("query usage records: %w", err)
	}

	var rows []map[string]any
	if len(out) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("decode usage records: %w", err)
	}

	records := make([]UsageRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, mapToUsageRecord(row))
	}
	return records, nil
}

func (s *sqliteStore) Close(ctx context.Context) error {
	return nil
}

func (s *sqliteStore) initSchema(ctx context.Context) error {
	schema := []string{
		`CREATE TABLE IF NOT EXISTS usage_records (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        created_at TEXT NOT NULL,
        path TEXT,
        provider TEXT,
        model TEXT,
        request_tokens INTEGER NOT NULL DEFAULT 0,
        response_tokens INTEGER NOT NULL DEFAULT 0,
        status INTEGER NOT NULL DEFAULT 0,
        duration INTEGER NOT NULL DEFAULT 0
)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_records_created_at ON usage_records (created_at DESC)`,
	}
	if _, err := s.runSQLite(ctx, false, schema...); err != nil {
		return fmt.Errorf("init sqlite schema: %w", err)
	}
	return nil
}

func (s *sqliteStore) runSQLite(ctx context.Context, jsonOutput bool, statements ...string) ([]byte, error) {
	args := []string{}
	if jsonOutput {
		args = append(args, "-json")
	}
	args = append(args, s.path)

	var script strings.Builder
	for _, pragma := range s.pragmas {
		if strings.TrimSpace(pragma) == "" {
			continue
		}
		script.WriteString("PRAGMA ")
		script.WriteString(pragma)
		script.WriteString(";\n")
	}
	for _, stmt := range statements {
		trimmed := strings.TrimSpace(stmt)
		if trimmed == "" {
			continue
		}
		script.WriteString(trimmed)
		if !strings.HasSuffix(trimmed, ";") {
			script.WriteString(";")
		}
		script.WriteString("\n")
	}

	cmd := exec.CommandContext(ctx, "sqlite3", args...)
	cmd.Stdin = strings.NewReader(script.String())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return nil, errors.New(errMsg)
	}
	return stdout.Bytes(), nil
}

func mapToUsageRecord(row map[string]any) UsageRecord {
	var record UsageRecord
	if v, ok := row["id"].(float64); ok {
		record.ID = int64(v)
	}
	if v, ok := row["created_at"].(string); ok {
		if parsed, err := parseSQLiteTime(v); err == nil {
			record.CreatedAt = parsed
		}
	}
	if v, ok := row["path"].(string); ok {
		record.Path = v
	}
	if v, ok := row["provider"].(string); ok {
		record.Provider = v
	}
	if v, ok := row["model"].(string); ok {
		record.Model = v
	}
	if v, ok := row["request_tokens"].(float64); ok {
		record.RequestTokens = int(v)
	}
	if v, ok := row["response_tokens"].(float64); ok {
		record.ResponseTokens = int(v)
	}
	if v, ok := row["status"].(float64); ok {
		record.Status = int(v)
	}
	if v, ok := row["duration"].(float64); ok {
		record.Duration = time.Duration(int64(v))
	}
	return record
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
		if strings.HasPrefix(path, "//") {
			path = path[2:]
		}
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

func (f *fileStore) QueryUsage(_ context.Context, limit int) ([]UsageRecord, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}

	records := make([]UsageRecord, len(f.records))
	copy(records, f.records)
	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
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

func quoteLiteral(value string) string {
	escaped := strings.ReplaceAll(value, "'", "''")
	return "'" + escaped + "'"
}

func quoteSQLiteTime(t time.Time) string {
	return quoteLiteral(t.UTC().Format(time.RFC3339Nano))
}

func parseSQLiteTime(value string) (time.Time, error) {
	layouts := []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("parse sqlite time: %s", value)
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
