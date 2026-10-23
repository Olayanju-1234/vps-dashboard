package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestETagAndCaching(t *testing.T) {
	var hits, notModified atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing user agent")
		}
		w.Header().Set("X-RateLimit-Remaining", "59")
		switch r.URL.Path {
		case "/repos/o/r/commits":
			if r.Header.Get("If-None-Match") == `"v1"` {
				notModified.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			_, _ = w.Write([]byte(`[{"sha":"bbb","commit":{"message":"feat: two\n\nbody","committer":{"date":"2026-10-22T10:00:00Z"},"tree":{"sha":"t2"}}},{"sha":"aaa","commit":{"message":"feat: one","committer":{"date":"2026-10-21T10:00:00Z"},"tree":{"sha":"t1"}}}]`))
		case "/repos/o/r/git/trees/t2":
			_, _ = w.Write([]byte(`{"sha":"t2","tree":[{"path":"a.txt","mode":"100644","type":"blob","sha":"x"}]}`))
		case "/repos/o/r/compare/aaa...bbb":
			_, _ = w.Write([]byte(`{"status":"ahead","ahead_by":1,"behind_by":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "test")
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		cs, err := c.Commits(ctx, "o/r", "main", 20)
		if err != nil || len(cs) != 2 || cs[0].Subject != "feat: two" || cs[0].Tree != "t2" {
			t.Fatalf("commits %+v %v", cs, err)
		}
	}
	if notModified.Load() != 1 {
		t.Fatalf("second list should be a conditional request")
	}
	for i := 0; i < 2; i++ {
		if tr, err := c.Tree(ctx, "o/r", "t2"); err != nil || len(tr.Entries) != 1 {
			t.Fatalf("tree %+v %v", tr, err)
		}
		if cmp, err := c.Compare(ctx, "o/r", "aaa", "bbb"); err != nil || cmp.AheadBy != 1 {
			t.Fatalf("compare %+v %v", cmp, err)
		}
		if cm, err := c.Commit(ctx, "o/r", "aaa"); err != nil || cm.Subject != "feat: one" {
			t.Fatalf("commit from list cache %+v %v", cm, err)
		}
	}
	if hits.Load() != 4 {
		t.Fatalf("immutable objects should be fetched once; hits=%d", hits.Load())
	}
	if _, err := c.Commits(ctx, "o/private", "main", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestRateLimitBacksOff(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "4102444800") // 2100-01-01
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c := New(srv.URL, "test")
	for i := 0; i < 3; i++ {
		if _, err := c.Commits(context.Background(), "o/r", "main", 1); !errors.Is(err, ErrRateLimited) {
			t.Fatalf("want rate limited, got %v", err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("should stop calling until reset, hits=%d", hits.Load())
	}
}
