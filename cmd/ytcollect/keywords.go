package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/kwstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/suggest"
)

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

const keywordsUsage = `Usage:
  ytcollect keywords import-seed            add or update the built-in place names
  ytcollect keywords list
  ytcollect keywords add -kind brand|person|org|topic|hashtag|place|term [-ambiguous] [-no-search]
                         [-variants "spelling|spelling"] [-note TEXT] <label>
  ytcollect keywords set [-active=true|false] [-ambiguous=true|false] [-search=true|false]
                         [-variants "spelling|spelling"] <id>
  ytcollect keywords remove <id>             (your own keywords; seed keywords are switched off with set)
  ytcollect keywords suggestions             what the system suggests (mined daily), best first
  ytcollect keywords accept <suggestion id>  turn a suggestion into a keyword
  ytcollect keywords reject <suggestion id>  reject it for good (and switch it off if it was activated)
  ytcollect keywords precision               how often each keyword's matches are confirmed; low ones are flagged

Your own keywords are stored only in the database, never in the repo.
`

func keywordsCmd(ctx context.Context, pool *pgxpool.Pool, out io.Writer, args []string) error {
	if len(args) == 0 {
		return errors.New(keywordsUsage)
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("keywords "+sub, flag.ContinueOnError)
	kind := fs.String("kind", "", "what the keyword names")
	variants := fs.String("variants", "", "spellings separated by |")
	note := fs.String("note", "", "a note for you")
	lang := fs.String("lang", "", "languages of the spellings, e.g. ar,ckb,en")
	noSearch := fs.Bool("no-search", false, "match only; never used as a search term")
	var ambiguous bool
	var active, amb, search optBool
	if sub == "set" {
		fs.Var(&active, "active", "true or false")
		fs.Var(&amb, "ambiguous", "true or false")
		fs.Var(&search, "search", "true or false")
	} else {
		fs.BoolVar(&ambiguous, "ambiguous", false, "needs a second keyword before a match counts")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	split := func(s string) []string {
		var vs []string
		for _, v := range strings.Split(s, "|") {
			if v = strings.TrimSpace(v); v != "" {
				vs = append(vs, v)
			}
		}
		return vs
	}
	switch sub {
	case "import-seed":
		n, err := kwstore.ImportSeed(ctx, pool)
		if err == nil {
			fmt.Fprintf(out, "%d seed keywords imported or updated\n", n)
		}
		return err
	case "list":
		kws, err := kwstore.List(ctx, pool)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tLABEL\tKIND\tSOURCE\tACTIVE\tSEARCHED\tAMBIGUOUS\tSPELLINGS")
		for _, k := range kws {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%v\t%v\t%v\t%s\n", k.ID, k.Label, k.Kind, k.Source, k.Active, k.Searched, k.Ambiguous,
				strings.Join(k.Variants, " | "))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Fprintf(out, "%d keyword(s)\n", len(kws))
		return nil
	case "add":
		if fs.NArg() != 1 {
			return errors.New(keywordsUsage)
		}
		id, err := kwstore.Add(ctx, pool, kwstore.New{Label: fs.Arg(0), Kind: *kind, Variants: split(*variants), Lang: *lang,
			Ambiguous: ambiguous, NoSearch: *noSearch, Note: *note})
		if err == nil {
			fmt.Fprintf(out, "keyword %d added; workers use it within a minute\n", id)
		}
		return err
	case "set":
		if fs.NArg() != 1 {
			return errors.New(keywordsUsage)
		}
		id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
		if err != nil {
			return errors.New(keywordsUsage)
		}
		return kwstore.Update(ctx, pool, id, kwstore.Change{Active: active.p, Ambiguous: amb.p, Searched: search.p,
			Variants: split(*variants), Lang: *lang})
	case "remove":
		if fs.NArg() != 1 {
			return errors.New(keywordsUsage)
		}
		id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
		if err != nil {
			return errors.New(keywordsUsage)
		}
		return kwstore.Remove(ctx, pool, id)
	case "suggestions":
		rows, err := pool.Query(ctx, `SELECT id, term, score, status, evidence->>'relevant', evidence->>'all', evidence->>'trending'
			FROM keyword_suggestions ORDER BY status = 'new' DESC, score DESC LIMIT 100`)
		if err != nil {
			return err
		}
		defer rows.Close()
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "ID	TERM	SCORE	STATUS	RELEVANT ITEMS	ALL ITEMS	TRENDING")
		for rows.Next() {
			var id int64
			var term, status string
			var score float32
			var rel, all, trend *string
			if err := rows.Scan(&id, &term, &score, &status, &rel, &all, &trend); err != nil {
				return err
			}
			fmt.Fprintf(tw, "%d\t%s\t%.2f\t%s\t%s\t%s\t%s\n", id, term, score, status, deref(rel), deref(all), deref(trend))
		}
		return tw.Flush()
	case "accept", "reject":
		if fs.NArg() != 1 {
			return errors.New(keywordsUsage)
		}
		id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
		if err != nil {
			return errors.New(keywordsUsage)
		}
		if sub == "accept" {
			return suggest.Accept(ctx, pool, id)
		}
		return suggest.Reject(ctx, pool, id)
	case "precision":
		ps, err := suggest.MeasurePrecision(ctx, pool, time.Now())
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "KEYWORD\tMATCHES (30 DAYS)\tCONFIRMED\tSHARE\tREVIEW")
		for _, p := range ps {
			flag := ""
			if p.Low {
				flag = "low precision: consider marking it ambiguous"
			}
			fmt.Fprintf(tw, "%s\t%d\t%d\t%.0f%%\t%s\n", p.Label, p.Matches, p.Confirmed, p.Share*100, flag)
		}
		return tw.Flush()
	}
	return errors.New(keywordsUsage)
}

// optBool is a flag that stays nil unless given.
type optBool struct{ p *bool }

func (o *optBool) String() string {
	if o.p == nil {
		return ""
	}
	return strconv.FormatBool(*o.p)
}

func (o *optBool) Set(s string) error {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return err
	}
	o.p = &v
	return nil
}

func (o *optBool) IsBoolFlag() bool { return true }
