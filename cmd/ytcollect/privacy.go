package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/erasure"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/media"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
)

func privacyService(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*erasure.Service, error) {
	keyer, err := privacy.LoadKeyer(pathFromEnv(EnvPrivacyKeyFile, DefaultPrivacyKeyFile))
	if err != nil {
		return nil, fmt.Errorf("privacy key: %w", err)
	}
	sup := privacy.NewSuppressor(keyer)
	if err := sup.Refresh(ctx, pool); err != nil {
		return nil, err
	}
	store, err := openStorage(log)
	if err != nil {
		return nil, err
	}
	return &erasure.Service{Pool: pool, Keyer: keyer, Sup: sup, Log: log, Media: store}, nil
}

// storagePath is the storage settings file.
func storagePath() string { return pathFromEnv(EnvStorageFile, media.DefaultConfigFile) }

// openStorage opens the object store; nil (and no error) when there is no
// storage settings file.
func openStorage(log *slog.Logger) (media.Store, error) {
	store, err := media.Load(storagePath())
	if errors.Is(err, media.ErrNotConfigured) {
		log.Info("no object storage configured: media and the archive are off", "settings", storagePath())
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	return store, nil
}

// eraseCmd records an erasure request; a worker carries it out at once.
func eraseCmd(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, out io.Writer, args []string) error {
	fs := flag.NewFlagSet("erase", flag.ContinueOnError)
	kind := fs.String("kind", "", "author, channel, video, comment or post")
	by := fs.String("by", "", "who asked, or a ticket reference (kept with the request)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *kind == "" || fs.NArg() != 1 {
		return errors.New("usage: ytcollect erase -kind author|channel|video|comment|post [-by TEXT] <ID, URL or @handle>")
	}
	s, err := privacyService(ctx, pool, log)
	if err != nil {
		return err
	}
	id, err := s.Request(ctx, *kind, fs.Arg(0), *by)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "erasure request %d recorded; a worker carries it out within minutes (see \"ytcollect erasures\")\n", id)
	return nil
}

// erasuresCmd lists requests (finished ones show no target, only status).
func erasuresCmd(ctx context.Context, pool *pgxpool.Pool, out io.Writer) error {
	rows, err := pool.Query(ctx, `SELECT id, kind, status, COALESCE(requested_by, ''), received_at, done_at, COALESCE(error, '')
		FROM erasure_requests ORDER BY id DESC LIMIT 100`)
	if err != nil {
		return err
	}
	defer rows.Close()
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tKIND\tSTATUS\tREQUESTED BY\tRECEIVED\tDONE\tERROR")
	for rows.Next() {
		var id int64
		var kind, status, by, e string
		var received time.Time
		var done *time.Time
		if err := rows.Scan(&id, &kind, &status, &by, &received, &done, &e); err != nil {
			return err
		}
		d := ""
		if done != nil {
			d = done.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", id, kind, status, by, received.UTC().Format(time.RFC3339), d, e)
	}
	return tw.Flush()
}

// privacyAuditCmd runs the suppression audit once (the scheduler runs it daily).
func privacyAuditCmd(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, out io.Writer) error {
	s, err := privacyService(ctx, pool, log)
	if err != nil {
		return err
	}
	res, err := s.Audit(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "suppressed items found in the database and erased again: %d %v\n", res.Total(), res.Violations)
	return nil
}

// exportCmd writes comments as JSON lines. With -pseudonymize (or the
// retention setting "pseudonymize_exports") authors appear as a keyed hash
// instead of an ID, handle or name; unique-author counts still work.
func exportCmd(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	since := fs.Duration("since", 24*time.Hour, "comments first seen in this window")
	outPath := fs.String("out", "", "output file (default: standard output)")
	pseudo := fs.Bool("pseudonymize", false, "replace author identities with keyed hashes")
	relevantOnly := fs.Bool("relevant", false, "only comments on Iraq-relevant videos and posts")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := privacyService(ctx, pool, log)
	if err != nil {
		return err
	}
	ret, err := privacy.LoadRetention(ctx, pool)
	if err != nil {
		return err
	}
	pseudonymize := *pseudo || ret.PseudonymizeExports
	var w io.Writer = os.Stdout
	if *outPath != "" {
		f, err := os.OpenFile(*outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	rows, err := pool.Query(ctx, `SELECT c.id, c.parent_kind, c.parent_id, c.channel_id, COALESCE(c.thread_id, ''),
			COALESCE(c.author_id, ''), COALESCE(c.author_handle, ''), c.text, c.published_at, c.likes_min, c.reply_count,
			c.pinned, c.hearted, c.edited, c.deleted_at, c.first_seen
		FROM comments c
		LEFT JOIN videos v ON c.parent_kind = 'video' AND v.id = c.parent_id
		LEFT JOIN posts p ON c.parent_kind = 'post' AND p.id = c.parent_id
		WHERE c.first_seen >= $1 AND (NOT $2 OR COALESCE(v.relevant, p.relevant, false))
		ORDER BY c.first_seen`, time.Now().Add(-*since), *relevantOnly)
	if err != nil {
		return err
	}
	defer rows.Close()
	enc := json.NewEncoder(bw)
	n := 0
	for rows.Next() {
		var rec struct {
			ID         string     `json:"id"`
			ParentKind string     `json:"parent_kind"`
			ParentID   string     `json:"parent_id"`
			ChannelID  string     `json:"channel_id"`
			ThreadID   string     `json:"thread_id,omitempty"`
			AuthorID   string     `json:"author_id,omitempty"`
			Author     string     `json:"author,omitempty"`
			AuthorKey  string     `json:"author_key,omitempty"`
			Text       string     `json:"text"`
			Published  *time.Time `json:"published_at,omitempty"`
			Likes      *int64     `json:"likes_min,omitempty"`
			Replies    *int32     `json:"reply_count,omitempty"`
			Pinned     bool       `json:"pinned"`
			Hearted    bool       `json:"hearted"`
			Edited     bool       `json:"edited"`
			Deleted    *time.Time `json:"deleted_at,omitempty"`
			FirstSeen  time.Time  `json:"first_seen"`
		}
		if err := rows.Scan(&rec.ID, &rec.ParentKind, &rec.ParentID, &rec.ChannelID, &rec.ThreadID, &rec.AuthorID, &rec.Author,
			&rec.Text, &rec.Published, &rec.Likes, &rec.Replies, &rec.Pinned, &rec.Hearted, &rec.Edited, &rec.Deleted, &rec.FirstSeen); err != nil {
			return err
		}
		if pseudonymize && rec.AuthorID != "" {
			rec.AuthorKey, rec.AuthorID, rec.Author = s.Keyer.AuthorKey(rec.AuthorID), "", ""
		}
		if err := enc.Encode(rec); err != nil {
			return err
		}
		n++
	}
	log.Info("export done", "comments", n, "pseudonymized", pseudonymize)
	return rows.Err()
}

// Where the scheduler keeps its hourly copy of the suppression list (next
// to the daily database dumps, so it is copied off the server with them).
const (
	EnvBackupDir     = "YT_BACKUP_DIR"
	DefaultBackupDir = "/var/lib/securebrand-youtube/backups"
	suppressionCopy  = "suppression-list.jsonl"
)

// copySuppressionList writes the list copy to the backup folder and, when
// object storage is configured, to the store.
func copySuppressionList(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	var buf bytes.Buffer
	if _, err := privacy.ExportList(ctx, pool, &buf); err != nil {
		return err
	}
	dir := pathFromEnv(EnvBackupDir, DefaultBackupDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	p := filepath.Join(dir, suppressionCopy)
	if err := os.WriteFile(p+".tmp", buf.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(p+".tmp", p); err != nil {
		return err
	}
	if fileExists(storagePath()) {
		store, err := openStorage(log)
		if err != nil {
			return err
		}
		return store.Put(ctx, "backups/"+suppressionCopy, "application/x-ndjson", buf.Bytes())
	}
	return nil
}

// suppressionCmd exports or imports the suppression list.
func suppressionCmd(ctx context.Context, pool *pgxpool.Pool, out io.Writer, args []string) error {
	usage := errors.New("usage: ytcollect suppression export | import <file>   (keyed hashes only: no names or IDs)")
	if len(args) == 0 {
		return usage
	}
	switch args[0] {
	case "export":
		n, err := privacy.ExportList(ctx, pool, out)
		if err == nil {
			fmt.Fprintf(os.Stderr, "%d entries\n", n)
		}
		return err
	case "import":
		if len(args) != 2 {
			return usage
		}
		f, err := os.Open(args[1])
		if err != nil {
			return err
		}
		defer f.Close()
		n, err := privacy.ImportList(ctx, pool, f)
		if err == nil {
			fmt.Fprintf(out, "%d entries added; now run: ytcollect privacy-audit\n", n)
		}
		return err
	default:
		return usage
	}
}
