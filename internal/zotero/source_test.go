package zotero

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebAPISource_PaginatesCollectionsAndItems(t *testing.T) {
	t.Parallel()
	var collectionStarts, itemStarts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Zotero-API-Key"); got != "test-key" {
			t.Fatalf("missing Zotero API key header")
		}
		w.Header().Set("Last-Modified-Version", "42")
		switch {
		case r.URL.Path == "/users/user-1/collections":
			collectionStarts = append(collectionStarts, r.URL.Query().Get("start"))
			switch r.URL.Query().Get("start") {
			case "0":
				w.Header().Set("Total-Results", "3")
				_, _ = w.Write([]byte(`[{"key":"col-a","version":4,"data":{"key":"col-a","name":"Alpha","parentCollection":false}},{"key":"col-b","version":5,"data":{"key":"col-b","name":"Beta","parentCollection":"col-a"}}]`))
			case "2":
				w.Header().Set("Total-Results", "3")
				_, _ = w.Write([]byte(`[{"key":"col-c","version":6,"data":{"key":"col-c","name":"Gamma","parentCollection":false}}]`))
			default:
				t.Fatalf("unexpected collection start %q", r.URL.Query().Get("start"))
			}
		case r.URL.Path == "/users/user-1/items/top":
			itemStarts = append(itemStarts, r.URL.Query().Get("start"))
			if got := r.URL.Query().Get("since"); got != "7" {
				t.Fatalf("since=%q want 7", got)
			}
			switch r.URL.Query().Get("start") {
			case "0":
				w.Header().Set("Total-Results", "2")
				_, _ = w.Write([]byte(`[{"key":"item-a","version":8,"data":{"key":"item-a","itemType":"journalArticle","title":"First","abstractNote":"A","date":"2024-01-01","publicationTitle":"Journal","DOI":"10.1/a","collections":["col-a"],"creators":[{"firstName":"Ada","lastName":"Lovelace"}]},"links":{"attachment":{"href":"https://api.zotero.org/users/user-1/items/ATT-A"}}},{"key":"item-b","version":9,"data":{"key":"item-b","itemType":"conferencePaper","title":"Second","date":"2023","conferenceName":"Conf","collections":["col-b"]}}]`))
			case "2":
				w.Header().Set("Total-Results", "2")
				_, _ = w.Write([]byte(`[]`))
			default:
				t.Fatalf("unexpected item start %q", r.URL.Query().Get("start"))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	src := NewWebAPISource(server.URL, "3", server.Client())
	cred := Credential{APIKey: "test-key", LibraryType: "user", LibraryID: "user-1"}

	var collections []Collection
	cursor := CollectionCursor{}
	for {
		page, err := src.ListCollections(context.Background(), cred, cursor)
		if err != nil {
			t.Fatalf("ListCollections: %v", err)
		}
		collections = append(collections, page.Items...)
		if page.Next == nil {
			break
		}
		cursor = *page.Next
	}
	if len(collections) != 3 || collections[1].ParentKey != "col-a" {
		t.Fatalf("collections=%+v", collections)
	}

	var items []Item
	changeCursor := ChangeCursor{}
	for {
		page, err := src.ChangesSince(context.Background(), cred, 7, changeCursor)
		if err != nil {
			t.Fatalf("ChangesSince: %v", err)
		}
		items = append(items, page.Items...)
		if page.Next == nil {
			break
		}
		changeCursor = *page.Next
	}
	if len(items) != 2 {
		t.Fatalf("items=%+v", items)
	}
	if items[0].Title != "First" || items[0].Year != 2024 || items[0].Venue != "Journal" || items[0].DOI != "10.1/a" {
		t.Fatalf("normalized item=%+v", items[0])
	}
	if len(items[0].Authors) != 1 || items[0].Authors[0] != "Ada Lovelace" {
		t.Fatalf("authors=%v", items[0].Authors)
	}
	if len(items[0].AttachmentKeys) != 1 || items[0].AttachmentKeys[0] != "ATT-A" {
		t.Fatalf("attachment keys=%v", items[0].AttachmentKeys)
	}
	if strings.Join(collectionStarts, ",") != "0,2" || strings.Join(itemStarts, ",") != "0,2" {
		t.Fatalf("unexpected pagination starts collection=%v item=%v", collectionStarts, itemStarts)
	}
}

func TestWebAPISource_ChangesSinceReturnsDeletedKeys(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified-Version", "19")
		w.Header().Set("Total-Results", "2")
		_, _ = w.Write([]byte(`[{"key":"item-a","version":18,"data":{"key":"item-a","itemType":"journalArticle","title":"Changed"}},{"key":"item-b","version":19,"data":{"key":"item-b","deleted":1}}]`))
	}))
	defer server.Close()

	page, err := NewWebAPISource(server.URL, "3", server.Client()).ChangesSince(context.Background(), Credential{APIKey: "k", LibraryID: "u"}, 12, ChangeCursor{})
	if err != nil {
		t.Fatalf("ChangesSince: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Key != "item-a" {
		t.Fatalf("items=%+v", page.Items)
	}
	if len(page.Deleted) != 1 || page.Deleted[0] != "item-b" {
		t.Fatalf("deleted=%v", page.Deleted)
	}
	if page.LastModifiedVersion != 19 {
		t.Fatalf("version=%d", page.LastModifiedVersion)
	}
}

func TestWebAPISource_FulltextUnavailableFallsBack(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/u/items/missing/fulltext":
			http.NotFound(w, r)
		case "/users/u/items/empty/fulltext":
			_, _ = w.Write([]byte(`{"content":""}`))
		case "/users/u/items/ok/fulltext":
			_, _ = w.Write([]byte(`{"content":"readable text"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	src := NewWebAPISource(server.URL, "3", server.Client())
	cred := Credential{APIKey: "k", LibraryID: "u"}
	for _, key := range []string{"missing", "empty"} {
		ft, err := src.GetFulltext(context.Background(), cred, key)
		if err != nil {
			t.Fatalf("GetFulltext(%s): %v", key, err)
		}
		if ft.Available {
			t.Fatalf("%s unexpectedly available", key)
		}
	}
	ft, err := src.GetFulltext(context.Background(), cred, "ok")
	if err != nil || !ft.Available || ft.Content != "readable text" {
		t.Fatalf("fulltext=%+v err=%v", ft, err)
	}
}

func TestWebAPISource_Retries429WithRetryAfter(t *testing.T) {
	t.Parallel()
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"userID":"u","username":"user","access":{"user":{"library":true}}}`))
	}))
	defer server.Close()

	src := NewWebAPISource(server.URL, "3", server.Client())
	var slept []time.Duration
	src.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	info, err := src.Validate(context.Background(), Credential{APIKey: "k"})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if info.UserID != "u" || attempts != 2 {
		t.Fatalf("info=%+v attempts=%d", info, attempts)
	}
	if len(slept) != 1 {
		t.Fatalf("sleep calls=%v", slept)
	}
}

func TestWebAPISource_AuthErrorIsNotRetriedAndDoesNotContainKey(t *testing.T) {
	t.Parallel()
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "no access", http.StatusForbidden)
	}))
	defer server.Close()

	src := NewWebAPISource(server.URL, "3", server.Client())
	_, err := src.Validate(context.Background(), Credential{APIKey: "super-secret-value"})
	if err == nil {
		t.Fatal("expected auth error")
	}
	var sourceErr *SourceError
	if !errors.As(err, &sourceErr) || sourceErr.Code != "unauthorized" {
		t.Fatalf("error=%T %v", err, err)
	}
	if strings.Contains(fmt.Sprint(err), "super-secret-value") {
		t.Fatalf("error leaked API key: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d want 1", attempts)
	}
}
