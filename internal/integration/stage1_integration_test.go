package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"research-scout/internal/storage"
	"research-scout/internal/zotero"
)

type fakeCredentialStore struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (s *fakeCredentialStore) Put(_ context.Context, workspaceID, connectionID string, secret []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string][]byte{}
	}
	s.values[workspaceID+"/"+connectionID] = append([]byte(nil), secret...)
	return nil
}

func (s *fakeCredentialStore) Get(_ context.Context, workspaceID, connectionID string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[workspaceID+"/"+connectionID]
	if !ok {
		return nil, errors.New("credential not found")
	}
	return append([]byte(nil), v...), nil
}

func (s *fakeCredentialStore) Delete(_ context.Context, workspaceID, connectionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, workspaceID+"/"+connectionID)
	return nil
}

type fakeZoteroSource struct {
	mu             sync.Mutex
	libraryVersion int64
	collections    []zotero.Collection
	items          map[string]zotero.Item
	changes        map[string]zotero.Item
	deleted        []string
	fulltext       map[string]zotero.Fulltext
	attachments    map[string][]zotero.Attachment
	sinceCalls     []int64
	changeErr      error
}

func newFakeZoteroSource() *fakeZoteroSource {
	return &fakeZoteroSource{
		libraryVersion: 10,
		collections: []zotero.Collection{
			{Key: "col-a", Name: "Read"},
			{Key: "col-b", Name: "Methods", ParentKey: "col-a"},
		},
		items: map[string]zotero.Item{
			"item-a": {
				Key: "item-a", Version: 8, ItemType: "journalArticle", Title: "Full text paper", Abstract: "Abstract A",
				Year: 2024, Venue: "Journal A", DOI: "10.1000/a", Authors: []string{"Ada Lovelace"},
				Collections: []string{"col-a"}, AttachmentKeys: []string{"att-a"},
			},
			"item-b": {
				Key: "item-b", Version: 9, ItemType: "conferencePaper", Title: "Abstract paper", Abstract: "Abstract B",
				Year: 2023, Venue: "Conference B", DOI: "10.1000/b", Authors: []string{"Bob"},
				Collections: []string{"col-a", "col-b"},
			},
			"item-c": {
				Key: "item-c", Version: 9, ItemType: "webpage", Title: "Metadata only",
				Year: 2022, Collections: []string{"col-b"},
			},
		},
		changes: map[string]zotero.Item{
			"item-a": {
				Key: "item-a", Version: 11, ItemType: "journalArticle", Title: "Full text paper revised", Abstract: "Abstract A revised",
				Year: 2025, Venue: "Journal A", DOI: "10.1000/a", Authors: []string{"Ada Lovelace", "Grace Hopper"},
				Collections: []string{"col-b"}, AttachmentKeys: []string{"att-a"},
			},
		},
		deleted: []string{"item-c"},
		fulltext: map[string]zotero.Fulltext{
			"att-a": {Available: true, Content: "The complete paper body."},
		},
		attachments: map[string][]zotero.Attachment{
			"item-a": {{Key: "att-a", ContentType: "application/pdf", Filename: "paper.pdf"}},
		},
	}
}

func (s *fakeZoteroSource) Validate(_ context.Context, cred zotero.Credential) (zotero.ConnectionInfo, error) {
	return zotero.ConnectionInfo{UserID: "user-1", Username: "tester", LibraryID: cred.LibraryID, LibraryType: "user"}, nil
}

func (s *fakeZoteroSource) ListCollections(_ context.Context, _ zotero.Credential, _ zotero.CollectionCursor) (zotero.CollectionPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return zotero.CollectionPage{Items: append([]zotero.Collection(nil), s.collections...), LastModifiedVersion: s.libraryVersion}, nil
}

func (s *fakeZoteroSource) GetLibraryVersion(_ context.Context, _ zotero.Credential) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.libraryVersion, nil
}

func (s *fakeZoteroSource) ChangesSince(_ context.Context, _ zotero.Credential, since int64, _ zotero.ChangeCursor) (zotero.ChangePage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sinceCalls = append(s.sinceCalls, since)
	if s.changeErr != nil {
		return zotero.ChangePage{}, s.changeErr
	}
	if since == 0 {
		items := make([]zotero.Item, 0, len(s.items))
		for _, item := range s.items {
			items = append(items, item)
		}
		return zotero.ChangePage{Items: items, LastModifiedVersion: s.libraryVersion}, nil
	}
	items := make([]zotero.Item, 0, len(s.changes))
	for _, item := range s.changes {
		items = append(items, item)
	}
	return zotero.ChangePage{Items: items, Deleted: append([]string(nil), s.deleted...), LastModifiedVersion: s.libraryVersion}, nil
}

func (s *fakeZoteroSource) GetItem(_ context.Context, _ zotero.Credential, key string) (zotero.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[key]
	if !ok {
		return zotero.Item{}, &zotero.SourceError{Code: "not_found"}
	}
	return item, nil
}

func (s *fakeZoteroSource) ListAttachments(_ context.Context, _ zotero.Credential, itemKey string) ([]zotero.Attachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]zotero.Attachment(nil), s.attachments[itemKey]...), nil
}

func (s *fakeZoteroSource) GetFulltext(_ context.Context, _ zotero.Credential, key string) (zotero.Fulltext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fulltext[key], nil
}

func newStage1Syncer(store *storage.Store, source zotero.Source, now time.Time) *zotero.Syncer {
	return &zotero.Syncer{
		Store:       store,
		Source:      source,
		Credentials: &fakeCredentialStore{},
		Config: zotero.SyncConfig{
			EmbeddingVersion:       "stage1-test-embedding-v1",
			EmbeddingDim:           1024,
			RetrievalConfigVersion: "retrieval-v1",
			Now:                    func() time.Time { return now },
		},
	}
}

func TestSyncFullPublishesIndexAndIsIdempotent(t *testing.T) {
	store := openTestStore(t)
	workspaceID, actorID := newTestWorkspace(t, store)
	source := newFakeZoteroSource()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	syncer := newStage1Syncer(store, source, now)

	first, err := syncer.Sync(context.Background(), workspaceID, actorID, zotero.Credential{APIKey: "secret-test-key", LibraryID: "user-1"}, zotero.SyncModeFull)
	if err != nil {
		t.Fatalf("first full sync: %v", err)
	}
	if !first.NewEpoch || first.Documents != 3 || first.Fulltext != 1 || first.Abstracts != 1 || first.MetadataOnly != 1 {
		t.Fatalf("unexpected first result: %+v", first)
	}
	if got := source.sinceCalls; len(got) != 1 || got[0] != 0 {
		t.Fatalf("full sync since calls=%v", got)
	}
	assertCount(t, store, workspaceID, `SELECT count(*) FROM documents WHERE workspace_id=$1 AND removed_at IS NULL`, 3)
	assertCount(t, store, workspaceID, `SELECT count(*) FROM document_versions WHERE workspace_id=$1`, 3)
	assertCount(t, store, workspaceID, `SELECT count(*) FROM chunks WHERE workspace_id=$1`, 2)
	assertCount(t, store, workspaceID, `SELECT count(*) FROM index_epochs WHERE workspace_id=$1 AND status='active'`, 1)
	if err := store.Pool().QueryRow(context.Background(), `
		SELECT last_library_version FROM zotero_connections WHERE workspace_id=$1 AND library_id='user-1'
	`, workspaceID).Scan(new(int64)); err != nil {
		t.Fatalf("read connection cursor: %v", err)
	}

	second, err := syncer.Sync(context.Background(), workspaceID, actorID, zotero.Credential{APIKey: "secret-test-key", LibraryID: "user-1"}, zotero.SyncModeFull)
	if err != nil {
		t.Fatalf("second full sync: %v", err)
	}
	if second.NewEpoch || second.Documents != 3 || second.VersionsAdded != 0 || second.Chunks != 0 {
		t.Fatalf("second sync was not idempotent: %+v", second)
	}
	assertCount(t, store, workspaceID, `SELECT count(*) FROM documents WHERE workspace_id=$1 AND removed_at IS NULL`, 3)
	assertCount(t, store, workspaceID, `SELECT count(*) FROM document_versions WHERE workspace_id=$1`, 3)
	assertCount(t, store, workspaceID, `SELECT count(*) FROM chunks WHERE workspace_id=$1`, 2)
}

func TestSyncIncrementalUpdatesAndTombstones(t *testing.T) {
	store := openTestStore(t)
	workspaceID, actorID := newTestWorkspace(t, store)
	source := newFakeZoteroSource()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	syncer := newStage1Syncer(store, source, now)
	cred := zotero.Credential{APIKey: "secret-test-key", LibraryID: "user-1"}

	first, err := syncer.Sync(context.Background(), workspaceID, actorID, cred, zotero.SyncModeFull)
	if err != nil {
		t.Fatalf("full sync: %v", err)
	}
	source.mu.Lock()
	source.libraryVersion = 12
	source.items["item-a"] = source.changes["item-a"]
	source.fulltext["att-a"] = zotero.Fulltext{Available: true, Content: "The revised complete paper body."}
	source.mu.Unlock()

	second, err := syncer.Sync(context.Background(), workspaceID, actorID, cred, zotero.SyncModeIncremental)
	if err != nil {
		t.Fatalf("incremental sync: %v", err)
	}
	if !second.NewEpoch || second.EpochID == first.EpochID || second.VersionsAdded != 1 || second.Deleted != 1 {
		t.Fatalf("unexpected incremental result: %+v", second)
	}
	if calls := source.sinceCalls; len(calls) != 2 || calls[1] != 10 {
		t.Fatalf("since calls=%v", calls)
	}
	assertCount(t, store, workspaceID, `SELECT count(*) FROM documents WHERE workspace_id=$1 AND removed_at IS NULL`, 2)
	assertCount(t, store, workspaceID, `SELECT count(*) FROM documents WHERE workspace_id=$1 AND removed_at IS NOT NULL`, 1)
	assertCount(t, store, workspaceID, `SELECT count(*) FROM lifecycle_tombstones WHERE workspace_id=$1 AND object_type='document' AND status='active'`, 1)
	assertCount(t, store, workspaceID, `SELECT count(*) FROM document_versions WHERE workspace_id=$1 AND document_id=(SELECT id FROM documents WHERE workspace_id=$1 AND zotero_item_key='item-a')`, 2)

	var purgeAfter time.Time
	if err := store.Pool().QueryRow(context.Background(), `
		SELECT purge_after FROM lifecycle_tombstones WHERE workspace_id=$1 AND object_id=(SELECT id FROM documents WHERE workspace_id=$1 AND zotero_item_key='item-c')
	`, workspaceID).Scan(&purgeAfter); err != nil {
		t.Fatalf("read tombstone: %v", err)
	}
	if want := now.Add(30 * 24 * time.Hour); !purgeAfter.Equal(want) {
		t.Fatalf("purge_after=%s want %s", purgeAfter, want)
	}

	if _, err := zotero.PurgeDue(context.Background(), store, workspaceID, now.Add(31*24*time.Hour)); err != nil {
		t.Fatalf("purge due: %v", err)
	}
	assertCount(t, store, workspaceID, `SELECT count(*) FROM lifecycle_tombstones WHERE workspace_id=$1 AND status='active'`, 0)
	assertCount(t, store, workspaceID, `SELECT count(*) FROM document_versions WHERE workspace_id=$1 AND document_id=(SELECT id FROM documents WHERE workspace_id=$1 AND zotero_item_key='item-c')`, 0)
}

func TestSyncFailurePreservesActiveEpoch(t *testing.T) {
	store := openTestStore(t)
	workspaceID, actorID := newTestWorkspace(t, store)
	source := newFakeZoteroSource()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	syncer := newStage1Syncer(store, source, now)
	cred := zotero.Credential{APIKey: "secret-test-key", LibraryID: "user-1"}
	first, err := syncer.Sync(context.Background(), workspaceID, actorID, cred, zotero.SyncModeFull)
	if err != nil {
		t.Fatalf("full sync: %v", err)
	}
	source.mu.Lock()
	source.libraryVersion = 99
	source.changeErr = fmt.Errorf("upstream temporary failure")
	source.mu.Unlock()

	if _, err := syncer.Sync(context.Background(), workspaceID, actorID, cred, zotero.SyncModeIncremental); err == nil {
		t.Fatal("expected incremental failure")
	}
	active, err := store.ActiveEpochID(context.Background(), workspaceID)
	if err != nil {
		t.Fatalf("active epoch: %v", err)
	}
	if active != first.EpochID {
		t.Fatalf("active epoch changed on failure: got %s want %s", active, first.EpochID)
	}
}

func assertCount(t *testing.T, store *storage.Store, workspaceID, query string, want int) {
	t.Helper()
	var got int
	if err := store.Pool().QueryRow(context.Background(), query, workspaceID).Scan(&got); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if got != want {
		t.Fatalf("count=%d want %d", got, want)
	}
}
